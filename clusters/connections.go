// Copyright 2024 SAP SE
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package clusters

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/informers"
	corev1_informers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The purpose of the Connection and ClusterConnections types is to manage
// connections to workload clusters, which are baked by expiring authentication.

// MakeClient creates a kubernetes client for the given workload cluster.
// The client is created using the kubeconfig secret of the workload cluster,
// which is fetched from the management cluster using client.
func MakeClient(ctx context.Context, client client.Client, cluster types.NamespacedName) (kubernetes.Interface, error) {
	secretKey := types.NamespacedName{Namespace: cluster.Namespace, Name: cluster.Name + "-kubeconfig"}
	var kubeConfigSecret corev1.Secret
	if err := client.Get(ctx, secretKey, &kubeConfigSecret); err != nil {
		return nil, err
	}
	kubeconfig, ok := kubeConfigSecret.Data["value"]
	if !ok {
		return nil, fmt.Errorf("secret %s has no value key", cluster.String())
	}
	restConfig, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to load workload cluster %s kubeconfig: %w", cluster.String(), err)
	}
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create clientset workload cluster %s: %w", cluster.String(), err)
	}
	return clientset, nil
}

// Connection is a connection to a workload cluster.
// A connection can be shared between multiple controllers.
type Connection struct {
	client       kubernetes.Interface
	nodeInformer corev1_informers.NodeInformer
	factory      informers.SharedInformerFactory
	nodeAttacher func(ni corev1_informers.NodeInformer)
}

// NewConnection creates a new connection to a workload cluster.
// The nodeAttacher function is called when the connection is started.
// It's purpose is to attach reconciliation loops to the node informer.
func NewConnection(client kubernetes.Interface, nodeAttacher func(ni corev1_informers.NodeInformer)) *Connection {
	factory := informers.NewSharedInformerFactory(client, time.Minute)
	informer := factory.Core().V1().Nodes()
	return &Connection{
		client:       client,
		nodeInformer: informer,
		factory:      factory,
		nodeAttacher: nodeAttacher,
	}
}

func (conn *Connection) Start(ctx context.Context) {
	if conn.nodeAttacher != nil {
		conn.nodeAttacher(conn.nodeInformer)
	}
	conn.factory.Start(ctx.Done())
	conn.factory.WaitForCacheSync(ctx.Done())
}

func (conn *Connection) Shutdown() {
	conn.factory.Shutdown()
}

// Connections is a collection of connections to workload clusters.
// It providers wrappers for getting and patching nodes in workload clusters
// that automatically recreate connections when the authentication expires.
type Connections struct {
	clusters         map[types.NamespacedName]*Connection
	mutex            sync.Mutex
	managementClient client.Client
	makeContext      func() context.Context
}

func NewConnections(client client.Client, contextFactory func() context.Context) *Connections {
	return &Connections{
		clusters:         make(map[types.NamespacedName]*Connection),
		mutex:            sync.Mutex{},
		managementClient: client,
		makeContext:      contextFactory,
	}
}

func (cc *Connections) AddConn(ctx context.Context, cluster types.NamespacedName, conn *Connection) {
	cc.mutex.Lock()
	defer cc.mutex.Unlock()
	if old, ok := cc.clusters[cluster]; ok {
		old.Shutdown()
	}
	cc.clusters[cluster] = conn
	conn.Start(ctx)
}

func (cc *Connections) GetConn(cluster types.NamespacedName) *Connection {
	cc.mutex.Lock()
	defer cc.mutex.Unlock()
	if conn, ok := cc.clusters[cluster]; ok {
		return conn
	}
	return nil
}

func (cc *Connections) DeleteConn(cluster types.NamespacedName) {
	cc.mutex.Lock()
	defer cc.mutex.Unlock()
	if old, ok := cc.clusters[cluster]; ok {
		old.Shutdown()
	}
	delete(cc.clusters, cluster)
}

type GetNodeParams struct {
	Log     logr.Logger
	Cluster types.NamespacedName
	Name    string
}

func (cc *Connections) GetNode(ctx context.Context, params GetNodeParams) (*corev1.Node, error) {
	conn := cc.GetConn(params.Cluster)
	if conn == nil {
		return nil, fmt.Errorf("no connection for cluster %s", params.Cluster)
	}
	node, err := conn.nodeInformer.Lister().Get(params.Name)
	if err == nil {
		return node, nil
	}
	if !errors.IsUnauthorized(err) {
		return nil, err
	}
	params.Log.Info("authentication expired, trying reauth", "cluster", params.Cluster)
	workloadClient, err := MakeClient(ctx, cc.managementClient, types.NamespacedName{})
	if err != nil {
		return nil, err
	}
	attacher := conn.nodeAttacher
	conn = NewConnection(workloadClient, attacher)
	cc.AddConn(cc.makeContext(), params.Cluster, conn)
	return conn.nodeInformer.Lister().Get(params.Name)
}

type PatchNodeParams struct {
	Log        logr.Logger
	Cluster    types.NamespacedName
	Name       string
	MergePatch []byte
}

func (cc *Connections) PatchNode(ctx context.Context, params PatchNodeParams) error {
	conn := cc.GetConn(params.Cluster)
	if conn == nil {
		return fmt.Errorf("no connection for cluster %s", params.Cluster)
	}
	_, err := conn.client.CoreV1().Nodes().Patch(
		ctx,
		params.Name,
		types.MergePatchType,
		params.MergePatch,
		v1.PatchOptions{},
	)
	if err == nil {
		return nil
	}
	if !errors.IsUnauthorized(err) {
		return err
	}
	params.Log.Info("authentication expired, trying reauth", "cluster", params.Cluster)
	workloadClient, err := MakeClient(ctx, cc.managementClient, types.NamespacedName{})
	if err != nil {
		return err
	}
	attacher := conn.nodeAttacher
	conn = NewConnection(workloadClient, attacher)
	cc.AddConn(cc.makeContext(), params.Cluster, conn)
	_, err = conn.client.CoreV1().Nodes().Patch(
		ctx,
		params.Name,
		types.MergePatchType,
		params.MergePatch,
		v1.PatchOptions{},
	)
	return err
}
