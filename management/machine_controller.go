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
	"github.com/sapcc/runtime-extension-maintenance-controller/constants"
	"github.com/sapcc/runtime-extension-maintenance-controller/workload"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	clusterv1beta1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
)

const (
	MaintenanceControllerLabelKey   string = "runtime-extension-maintenance-controller.cloud.sap/enabled"
	MaintenanceControllerLabelValue string = "true"
	MaintenanceStateLabelKey        string = "cloud.sap/maintenance-state"
	MachineDeletedLabelKey          string = "runtime-extension-maintenance-controller.cloud.sap/machine-deleted"
	MachineDeletedLabelValue        string = "true"
)

type MachineReconciler struct {
	client.Client
	Log                     logr.Logger
	Scheme                  *runtime.Scheme
	WorkloadNodeControllers map[string]*workload.NodeController
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
	if !ok || isMaintenanceController != MaintenanceControllerLabelValue {
		return ctrl.Result{}, r.cleanupMachine(ctx, &machine, clusterName)
	}
	workloadController, ok := r.WorkloadNodeControllers[clusterName]
	if !ok {
		clusterKey := types.NamespacedName{Namespace: machine.Namespace, Name: clusterName}
		var err error
		workloadController, err = makeNodeCtrl(ctx, NodeControllerParamaters{
			cluster:          clusterKey,
			managementClient: r.Client,
			log:              r.Log,
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
	node, err := workloadController.Lister().Get(nodeName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get node %s in workload cluster %s node: %w", nodeName, clusterName, err)
	}
	originalNode := node.DeepCopy()
	originalMachine := machine.DeepCopy()
	r.propagateState(&machine, node)

	if err := workloadController.PatchNode(ctx, node, originalNode); err != nil {
		return ctrl.Result{}, err
	}
	r.Log.Info("patched node", "node", node.Name)

	return ctrl.Result{}, r.patchMachine(ctx, &machine, originalMachine)
}

func (r *MachineReconciler) cleanupMachine(ctx context.Context, machine *clusterv1beta1.Machine, cluster string) error {
	original := machine.DeepCopy()
	// cleanup pre-drain hook
	delete(machine.Annotations, constants.PreDrainDeleteHookAnnotationKey)
	if err := r.Client.Patch(ctx, machine, client.MergeFrom(original)); err != nil {
		return fmt.Errorf("failed to remove pre-drain hook for machine %s", machine.Name)
	}
	// cleanup workload node reconciler, if no machine uses maintenance-controller
	selector := client.MatchingLabels{
		clusterv1beta1.ClusterNameLabel: cluster,
		MaintenanceControllerLabelKey:   MaintenanceControllerLabelValue,
	}
	var machineList clusterv1beta1.MachineList
	if err := r.Client.List(ctx, &machineList, selector); err != nil {
		return err
	}
	if len(machineList.Items) == 0 {
		cancel, ok := r.CancelFuncs[cluster]
		if !ok {
			return fmt.Errorf("expected workload node controller for cluster %s, but it does not exist", cluster)
		}
		cancel()
		delete(r.CancelFuncs, cluster)
		delete(r.WorkloadNodeControllers, cluster)
		r.Log.Info("stopped workload node reconciler, no machines enabled", "cluster", cluster)
	}
	return nil
}

type NodeControllerParamaters struct {
	cluster          types.NamespacedName
	managementClient client.Client
	log              logr.Logger
}

// RBAC-Limited kubeconfigs are currently not possible: https://github.com/kubernetes-sigs/cluster-api/issues/5553
// and https://github.com/kubernetes-sigs/cluster-api/issues/3661
func makeNodeCtrl(ctx context.Context, params NodeControllerParamaters) (*workload.NodeController, error) {
	// name string, ns string
	secretKey := types.NamespacedName{Namespace: params.cluster.Namespace, Name: params.cluster.Name + "-kubeconfig"}
	var kubeConfigSecret corev1.Secret
	if err := params.managementClient.Get(ctx, secretKey, &kubeConfigSecret); err != nil {
		return nil, err
	}
	kubeconfig, ok := kubeConfigSecret.Data["value"]
	if !ok {
		return nil, fmt.Errorf("secret %s has no value key", params.cluster.String())
	}
	restConfig, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to load workload cluster %s kubeconfig: %w", params.cluster.String(), err)
	}
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create clientset workload cluster %s: %w", params.cluster.String(), err)
	}
	controller, err := workload.NewNodeController(workload.NodeControllerOptions{
		Log:              params.log.WithName(params.cluster.Name),
		ManagementClient: params.managementClient,
		WorkloadClient:   clientset,
	})
	if err != nil {
		return nil, fmt.Errorf(
			"failed to initialize node controller for workload cluster %s: %w",
			params.cluster.String(),
			err,
		)
	}
	return &controller, nil
}

func (r *MachineReconciler) propagateState(machine *clusterv1beta1.Machine, node *corev1.Node) {
	log := r.Log.WithValues("node", node.Name, "machine", machine.Namespace+"/"+machine.Name)
	if machine.Status.NodeRef == nil {
		log.Info("machine has no nodeRef")
		return
	}
	_, hasMaintenanceState := node.Labels[MaintenanceStateLabelKey]
	if machine.Annotations == nil {
		machine.Annotations = make(map[string]string)
	}
	if node.Labels == nil {
		node.Labels = make(map[string]string)
	}
	approved, ok := node.Labels[constants.ApproveDeletionLabelKey]
	// Add deletion hook to machines that have a noderef and the cloud.sap/maintenance-state label
	// Remove deletion hooks from machines whose nodes don't have the cloud.sap/maintenance-state label
	if hasMaintenanceState && (!ok || approved != constants.ApproveDeletionLabelValue) {
		machine.Annotations[constants.PreDrainDeleteHookAnnotationKey] = constants.PreDrainDeleteHookAnnotationValue
		log.Info("queueing pre-drain hook attachment")
	} else {
		delete(machine.Annotations, constants.PreDrainDeleteHookAnnotationKey)
		log.Info("queueing pre-drain hook removal")
	}

	// For to be deleted machines with hook deliver label onto the node (deletion timestamp)
	if machine.DeletionTimestamp == nil {
		delete(node.Labels, MachineDeletedLabelKey)
		log.Info("queueing machine deletion label removal")
	} else {
		node.Labels[MachineDeletedLabelKey] = MachineDeletedLabelValue
		log.Info("queueing machine deletion label attachment")
	}
}

func (r *MachineReconciler) patchMachine(ctx context.Context, current, original *clusterv1beta1.Machine) error {
	if !equality.Semantic.DeepEqual(current, original) {
		if err := r.Client.Patch(ctx, current, client.MergeFrom(original)); err != nil {
			return err
		}
		r.Log.Info("patched machine", "machine", client.ObjectKeyFromObject(current))
	}
	return nil
}

func (r *MachineReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 1,
		}).
		For(&clusterv1beta1.Machine{}).
		Complete(r)
}
