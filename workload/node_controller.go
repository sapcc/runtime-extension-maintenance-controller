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

package workload

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	corev1_informers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	clusterv1beta1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/sapcc/runtime-extension-maintenance-controller/clusters"
	"github.com/sapcc/runtime-extension-maintenance-controller/state"
)

const (
	baseDelay time.Duration = 5 * time.Second
	maxDelay  time.Duration = 5 * time.Minute
)

// NodeController is a controller that watches nodes in a workload cluster.
// After creation attachTo must be called to attach the controller to a node informer.
// The informer management is separated from the controller to allow swapping the informer
// for a different one, when authentication expires.
type NodeController struct {
	log              logr.Logger
	managementClient client.Client
	connections      *clusters.Connections
	queue            workqueue.TypedRateLimitingInterface[types.NamespacedName]
	cluster          types.NamespacedName
	reauthChan       chan struct{}
}

type NodeControllerOptions struct {
	Log              logr.Logger
	ManagementClient client.Client
	Connections      *clusters.Connections
	Cluster          types.NamespacedName
}

func NewNodeController(opts NodeControllerOptions) (NodeController, error) {
	// shared informers are not shared for WorkloadNodeControllers
	// as nodes from different clusters may end up in the shared cache
	rateLimiter := workqueue.NewTypedItemExponentialFailureRateLimiter[types.NamespacedName](baseDelay, maxDelay)
	queue := workqueue.NewTypedRateLimitingQueue(rateLimiter)
	controller := NodeController{
		log:              opts.Log.WithValues("cluster", opts.Cluster.String()),
		managementClient: opts.ManagementClient,
		queue:            queue,
		connections:      opts.Connections,
		cluster:          opts.Cluster,
		reauthChan:       make(chan struct{}),
	}
	return controller, nil
}

func (c *NodeController) AttachTo(nodeInformer corev1_informers.NodeInformer) error {
	_, err := nodeInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			node, ok := obj.(*corev1.Node)
			if !ok {
				c.log.Info("node informer received non-node object")
				return
			}
			c.queue.Add(client.ObjectKeyFromObject(node))
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			node, ok := newObj.(*corev1.Node)
			if !ok {
				c.log.Info("node informer received non-node object")
				return
			}
			c.queue.Add(client.ObjectKeyFromObject(node))
		},
		DeleteFunc: func(obj interface{}) {
			if deleted, ok := obj.(cache.DeletedFinalStateUnknown); ok {
				obj = deleted.Obj
			}
			node, ok := obj.(*corev1.Node)
			if !ok {
				c.log.Info("node informer received non-node object")
				return
			}
			key := client.ObjectKeyFromObject(node)
			c.queue.Forget(key)
			c.queue.Done(key)
		},
	})
	if err != nil {
		return err
	}
	err = nodeInformer.Informer().SetWatchErrorHandler(func(r *cache.Reflector, err error) {
		if err == nil {
			return
		}
		if !errors.IsUnauthorized(err) {
			c.log.Error(err, "failed to watch/list workload cluster nodes")
			return
		}
		c.log.Info("auth expired on node informer")
		// ReauthConn needs to be moved to another goroutine
		// because within the handler the reflector goroutine
		// needs to termiante for inforerFactory.Shutdown() to
		// unblock, which ReauthConn would call causing a deadlock.
		c.reauthChan <- struct{}{}
	})
	return err
}

func (c *NodeController) reauth(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.reauthChan:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second) //nolint:gomnd
			defer cancel()
			err := c.connections.ReauthConn(ctx, clusters.ReauthParams{
				Cluster: c.cluster,
				Log:     c.log,
			})
			if err != nil {
				c.log.Error(err, "failed to reauthenticate")
				return
			}
			c.log.Info("reauthentication successful")
		}
	}
}

func (c *NodeController) Run(ctx context.Context) {
	go c.reauth(ctx)
	defer c.queue.ShutDown()
	go func() {
		for {
			key, quit := c.queue.Get()
			if quit {
				return
			}
			if err := c.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
				c.log.Error(err, "failed to reconcile workload cluster node")
			}
			c.queue.Done(key)
		}
	}()
	<-ctx.Done()
	close(c.reauthChan)
}

func (c *NodeController) Reconcile(ctx context.Context, req ctrl.Request) error {
	node, err := c.connections.GetNode(ctx, clusters.GetNodeParams{Log: c.log, Cluster: c.cluster, Name: req.Name})
	if errors.IsNotFound(err) {
		c.log.Info("failed to get workload node, was it deleted?", "node", req.Name)
		return nil
	}
	if err != nil {
		return err
	}

	machineName, ok := node.Annotations[clusterv1beta1.MachineAnnotation]
	if !ok {
		return fmt.Errorf("node %s is missing the %s annotation", node.Name, clusterv1beta1.MachineAnnotation)
	}
	clusterNamespace, ok := node.Annotations[clusterv1beta1.ClusterNamespaceAnnotation]
	if !ok {
		return fmt.Errorf("node %s is missing the %s annotation", node.Name, clusterv1beta1.ClusterNamespaceAnnotation)
	}
	machineKey := types.NamespacedName{Namespace: clusterNamespace, Name: machineName}
	var machine clusterv1beta1.Machine
	if err := c.managementClient.Get(ctx, machineKey, &machine); err != nil {
		return err
	}

	reconciler := state.Reconciler{
		Log:              c.log,
		ManagementClient: c.managementClient,
		Connections:      c.connections,
		Cluster:          c.cluster,
	}
	return reconciler.PatchState(ctx, &machine, node)
}
