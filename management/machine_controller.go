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

package management

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"github.com/sapcc/runtime-extension-maintenance-controller/clusters"
	"github.com/sapcc/runtime-extension-maintenance-controller/constants"
	"github.com/sapcc/runtime-extension-maintenance-controller/state"
	"github.com/sapcc/runtime-extension-maintenance-controller/workload"
	"k8s.io/apimachinery/pkg/types"
	corev1_informers "k8s.io/client-go/informers/core/v1"
	clusterv1beta1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
)

const (
	MaintenanceControllerLabelKey   string = "runtime-extension-maintenance-controller.cloud.sap/enabled"
	MaintenanceControllerLabelValue string = "true"
)

type MachineReconciler struct {
	client.Client
	Log                     logr.Logger
	WorkloadNodeControllers map[string]*workload.NodeController
	ClusterConnections      *clusters.Connections
	// A WorkloadNodeController needs an interruptable long-running context.
	// Reconcile may get a short context, so the long-running context is
	// fetched from a factory function.
	WorkloadContextFunc func() context.Context
	CancelFuncs         map[string]context.CancelFunc
}

func (r *MachineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var machine clusterv1beta1.Machine
	if err := r.Client.Get(ctx, req.NamespacedName, &machine); err != nil {
		return ctrl.Result{}, err
	}
	clusterName := machine.Spec.ClusterName
	isMaintenanceController, ok := machine.Labels[MaintenanceControllerLabelKey]
	// It should be safe to only perform cleanup in the machine controller, when the following occurs:
	// - The last relevant machine object is deleted, but cluster-api cleanup is blocked by pre-drain hook
	// - cloud.sap/maintenance-controller label is removed on the node
	// - This removes the pre-drain hook => causing reconciliation and cleanup
	if !ok || isMaintenanceController != MaintenanceControllerLabelValue {
		return ctrl.Result{}, r.cleanupMachine(ctx, &machine, clusterName)
	}
	_, ok = r.WorkloadNodeControllers[clusterName]
	clusterKey := types.NamespacedName{Namespace: machine.Namespace, Name: clusterName}
	if !ok {
		workloadController, err := makeNodeCtrl(ctx, NodeControllerParamaters{
			cluster:          clusterKey,
			managementClient: r.Client,
			log:              ctrl.Log.WithName("workload"),
			connections:      r.ClusterConnections,
		})
		if err != nil {
			return ctrl.Result{}, err
		}
		r.WorkloadNodeControllers[clusterName] = workloadController
		workloadCtx, cancel := context.WithCancel(r.WorkloadContextFunc())
		r.CancelFuncs[clusterName] = cancel
		go workloadController.Run(workloadCtx)
	}
	if machine.Status.NodeRef == nil {
		r.Log.Info("machine has no nodeRef", "machine", req.String())
		return ctrl.Result{}, nil
	}
	nodeName := machine.Status.NodeRef.Name
	node, err := r.ClusterConnections.GetNode(ctx, clusters.GetNodeParams{Log: r.Log, Cluster: clusterKey, Name: nodeName})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get node %s in workload cluster %s node: %w", nodeName, clusterName, err)
	}
	reconciler := state.Reconciler{
		Log:              r.Log,
		ManagementClient: r.Client,
		Connections:      r.ClusterConnections,
		Cluster:          clusterKey,
	}
	return ctrl.Result{}, reconciler.PatchState(ctx, &machine, node)
}

func (r *MachineReconciler) cleanupMachine(ctx context.Context, machine *clusterv1beta1.Machine, cluster string) error {
	original := machine.DeepCopy()
	// cleanup pre-drain hook
	delete(machine.Annotations, constants.PreDrainDeleteHookAnnotationKey)
	if err := r.Client.Patch(ctx, machine, client.MergeFrom(original)); err != nil {
		return fmt.Errorf("failed to remove pre-drain hook for machine %s", machine.Name)
	}
	r.Log.Info("removed pre-drain hook")
	// cleanup workload node reconciler, if no machine uses maintenance-controller
	selector := client.MatchingLabels{
		clusterv1beta1.ClusterNameLabel: cluster,
		MaintenanceControllerLabelKey:   MaintenanceControllerLabelValue,
	}
	var machineList clusterv1beta1.MachineList
	if err := r.Client.List(ctx, &machineList, selector); err != nil {
		return err
	}
	if len(machineList.Items) > 0 {
		return nil
	}
	_, hasController := r.WorkloadNodeControllers[cluster]
	cancel, hasCancel := r.CancelFuncs[cluster]
	if !hasController && !hasCancel {
		return nil
	}
	r.Log.Info("stopping workload node reconciler, no machines enabled", "cluster", cluster)
	if !hasCancel {
		return fmt.Errorf("expected cancel func for cluster %s, but it does not exist", cluster)
	}
	cancel()
	delete(r.CancelFuncs, cluster)
	delete(r.WorkloadNodeControllers, cluster)
	r.ClusterConnections.DeleteConn(r.Log, types.NamespacedName{
		Namespace: machine.Namespace,
		Name:      cluster,
	})
	r.Log.Info("stopped workload node reconciler, no machines enabled", "cluster", cluster)
	return nil
}

type NodeControllerParamaters struct {
	cluster          types.NamespacedName
	connections      *clusters.Connections
	managementClient client.Client
	log              logr.Logger
}

// RBAC-Limited kubeconfigs are currently not possible: https://github.com/kubernetes-sigs/cluster-api/issues/5553
// and https://github.com/kubernetes-sigs/cluster-api/issues/3661
func makeNodeCtrl(ctx context.Context, params NodeControllerParamaters) (*workload.NodeController, error) {
	controller, err := workload.NewNodeController(workload.NodeControllerOptions{
		Log:              params.log,
		ManagementClient: params.managementClient,
		Connections:      params.connections,
		Cluster:          params.cluster,
	})
	if err != nil {
		return nil, err
	}
	workloadClient, err := clusters.MakeClient(ctx, params.managementClient, params.cluster)
	if err != nil {
		return nil, fmt.Errorf(
			"failed to initialize node controller for workload cluster %s: %w",
			params.cluster.String(),
			err,
		)
	}
	conn := clusters.NewConnection(
		workloadClient,
		func(ni corev1_informers.NodeInformer) {
			err := controller.AttachTo(ni)
			if err != nil {
				params.log.Error(err, "failed to attach workload node controller to informer", "cluster", params.cluster.String())
			}
		},
	)
	params.connections.AddConn(ctx, params.log, params.cluster, conn)
	return &controller, nil
}

func (r *MachineReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 1,
		}).
		For(&clusterv1beta1.Machine{}).
		Complete(r)
}
