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
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"github.com/go-logr/logr"
	"github.com/sapcc/runtime-extension-maintenance-controller/constants"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/informers"
	corev1_informers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	corev1_listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	clusterv1beta1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	baseDelay time.Duration = 5 * time.Second
	maxDelay  time.Duration = 5 * time.Minute
)

type NodeController struct {
	log              logr.Logger
	managementClient client.Client
	workloadClient   kubernetes.Interface
	queue            workqueue.RateLimitingInterface
	factory          informers.SharedInformerFactory
	informer         corev1_informers.NodeInformer
}

type NodeControllerOptions struct {
	Log              logr.Logger
	ManagementClient client.Client
	WorkloadClient   kubernetes.Interface
}

func NewNodeController(opts NodeControllerOptions) (NodeController, error) {
	// shared informers are not shared for WorkloadNodeControllers
	// as nodes from different clusters may end up in the shared cache
	factory := informers.NewSharedInformerFactory(opts.WorkloadClient, time.Minute)
	informer := factory.Core().V1().Nodes()
	queue := workqueue.NewRateLimitingQueue(workqueue.NewItemExponentialFailureRateLimiter(baseDelay, maxDelay))
	_, err := informer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			node, ok := obj.(*corev1.Node)
			if !ok {
				opts.Log.Info("node informer received non-node object")
				return
			}
			queue.Add(client.ObjectKeyFromObject(node))
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			node, ok := newObj.(*corev1.Node)
			if !ok {
				opts.Log.Info("node informer received non-node object")
				return
			}
			queue.Add(client.ObjectKeyFromObject(node))
		},
		DeleteFunc: func(obj interface{}) {
			if deleted, ok := obj.(cache.DeletedFinalStateUnknown); ok {
				obj = deleted.Obj
			}
			node, ok := obj.(*corev1.Node)
			if !ok {
				opts.Log.Info("node informer received non-node object")
				return
			}
			key := client.ObjectKeyFromObject(node)
			queue.Forget(key)
			queue.Done(key)
		},
	})
	return NodeController{
		log:              opts.Log,
		managementClient: opts.ManagementClient,
		workloadClient:   opts.WorkloadClient,
		factory:          factory,
		informer:         informer,
		queue:            queue,
	}, err
}

// TODO: rotate when kubeconfig expires.
func (c *NodeController) Run(ctx context.Context) {
	c.factory.Start(ctx.Done())
	defer c.factory.Shutdown()
	c.factory.WaitForCacheSync(ctx.Done())
	defer c.queue.ShutDown()
	go func() {
		for {
			key, quit := c.queue.Get()
			if quit {
				return
			}
			name, ok := key.(types.NamespacedName)
			if !ok {
				c.log.Info("something other than types.NamespacedName fetched from queue", "type", reflect.TypeOf(key).Name())
				c.queue.Done(key)
				continue
			}
			if err := c.Reconcile(ctx, ctrl.Request{NamespacedName: name}); err != nil {
				c.log.Error(err, "failed to reconcile workload cluster node")
			}
			c.queue.Done(key)
		}
	}()
	<-ctx.Done()
}

func (c *NodeController) Reconcile(ctx context.Context, req ctrl.Request) error {
	node, err := c.informer.Lister().Get(req.Name)
	if err != nil {
		return err
	}
	if node.Labels == nil {
		node.Labels = make(map[string]string)
	}
	approved, ok := node.Labels[constants.ApproveDeletionLabelKey]
	if !ok || approved != constants.ApproveDeletionLabelValue {
		return nil
	}

	c.log.Info("machine deletion was approved", "node", node.Name)
	if node.Annotations == nil {
		node.Annotations = make(map[string]string)
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
	originalMachine := machine.DeepCopy()
	delete(machine.Annotations, constants.PreDrainDeleteHookAnnotationKey)
	return c.managementClient.Patch(ctx, &machine, client.MergeFrom(originalMachine))
}

func (c *NodeController) Lister() corev1_listers.NodeLister {
	return c.informer.Lister()
}

func (c *NodeController) PatchNode(ctx context.Context, current *corev1.Node, original *corev1.Node) error {
	if equality.Semantic.DeepEqual(current, original) {
		return nil
	}
	originalMarshaled, err := json.Marshal(original)
	if err != nil {
		return err
	}
	currentMarshaled, err := json.Marshal(current)
	if err != nil {
		return err
	}
	patch, err := jsonpatch.CreateMergePatch(originalMarshaled, currentMarshaled)
	if err != nil {
		return err
	}
	_, err = c.workloadClient.CoreV1().Nodes().Patch(ctx, current.Name, types.MergePatchType, patch, v1.PatchOptions{})
	return err
}
