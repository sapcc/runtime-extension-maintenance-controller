// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package state

import (
	"context"
	"encoding/json"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/types"
	clusterv1beta1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/sapcc/runtime-extension-maintenance-controller/clusters"
	"github.com/sapcc/runtime-extension-maintenance-controller/constants"
)

const (
	MaintenanceStateLabelKey string = "cloud.sap/maintenance-state"

	MachineDeletedLabelKey       string = "runtime-extension-maintenance-controller.cloud.sap/machine-deleted"
	MachineDeletedLabelValue     string = "true"
	MachineMaintenanceLabelKey   string = "runtime-extension-maintenance-controller.cloud.sap/machine-maintenance"
	MachineMaintenanceLabelValue string = "true"
	ApproveMaintenanceLabelKey   string = "runtime-extension-maintenance-controller.cloud.sap/approve-maintenance"
	ApproveMaintenanceLabelValue string = "true"
)

// Is currently invoked in parallel by management and workload controller.
func propagate(log logr.Logger, machine *clusterv1beta1.Machine, node *corev1.Node) {
	log = log.WithValues("node", node.Name, "machine", machine.Namespace+"/"+machine.Name)
	if machine.Status.NodeRef == nil {
		log.Info("machine has no nodeRef")
		return
	}
	if machine.Annotations == nil {
		machine.Annotations = make(map[string]string)
	}
	if node.Labels == nil {
		node.Labels = make(map[string]string)
	}

	if machineRequiresPreDrainHook(machine, node) {
		machine.Annotations[constants.PreDrainDeleteHookAnnotationKey] = constants.PreDrainDeleteHookAnnotationValue
		log.Info("queueing pre-drain hook attachment")
	} else {
		delete(machine.Annotations, constants.PreDrainDeleteHookAnnotationKey)
		log.Info("queueing pre-drain hook removal")
	}

	if nodeRequiresDeletionLabel(machine, node) {
		node.Labels[MachineDeletedLabelKey] = MachineDeletedLabelValue
		log.Info("queueing node machine deletion label attachment")
	} else {
		delete(node.Labels, MachineDeletedLabelKey)
		log.Info("queueing node machine deletion label removal")
	}

	if nodeRequiresMaintenanceLabel(machine, node) {
		node.Labels[MachineMaintenanceLabelKey] = MachineMaintenanceLabelValue
		log.Info("queueing node machine maintenance label attachment")
	} else {
		delete(node.Labels, MachineMaintenanceLabelKey)
		log.Info("queueing node machine maintenance label removal")
	}

	if machineRequiresMaintenanceApproved(machine, node) {
		// An external controller is expected to act on and delete this label
		machine.Labels[constants.MaintenanceLabelKey] = constants.MaintenanceLabelApproved
		log.Info("queueing machine maintenance label approval attachment")
	}
}

// Add pre-drain hook to machines that have a noderef and the cloud.sap/maintenance-state label.
func machineRequiresPreDrainHook(machine *clusterv1beta1.Machine, node *corev1.Node) bool {
	_, hasMaintenanceState := node.Labels[MaintenanceStateLabelKey]
	approved, ok := node.Labels[constants.ApproveDeletionLabelKey]
	isApproved := ok && approved == constants.ApproveDeletionLabelValue
	enabledValue, ok := machine.Labels[constants.EnabledLabelKey]
	isEnabled := ok && enabledValue == constants.EnabledLabelValue
	return hasMaintenanceState && isEnabled && !isApproved
}

// For to be deleted machines with hook deliver label onto the node (deletion timestamp).
func nodeRequiresDeletionLabel(machine *clusterv1beta1.Machine, node *corev1.Node) bool {
	_, hasMaintenanceState := node.Labels[MaintenanceStateLabelKey]
	enabledValue, ok := machine.Labels[constants.EnabledLabelKey]
	isEnabled := ok && enabledValue == constants.EnabledLabelValue
	return hasMaintenanceState && isEnabled && machine.DeletionTimestamp != nil
}

func nodeRequiresMaintenanceLabel(machine *clusterv1beta1.Machine, node *corev1.Node) bool {
	_, hasMaintenanceState := node.Labels[MaintenanceStateLabelKey]
	enabledValue, ok := machine.Labels[constants.EnabledLabelKey]
	isEnabled := ok && enabledValue == constants.EnabledLabelValue
	maintenanceValue, ok := machine.Labels[constants.MaintenanceLabelKey]
	isRequested := ok &&
		(maintenanceValue == constants.MaintenanceLabelRequested || maintenanceValue == constants.MaintenanceLabelApproved)
	return hasMaintenanceState && isEnabled && isRequested
}

func machineRequiresMaintenanceApproved(machine *clusterv1beta1.Machine, node *corev1.Node) bool {
	_, hasMaintenanceState := node.Labels[MaintenanceStateLabelKey]
	enabledValue, ok := machine.Labels[constants.EnabledLabelKey]
	isEnabled := ok && enabledValue == constants.EnabledLabelValue
	approveValue, ok := node.Labels[ApproveMaintenanceLabelKey]
	isApproved := ok && approveValue == ApproveMaintenanceLabelValue
	maintenanceValue, ok := machine.Labels[constants.MaintenanceLabelKey]
	isRequested := ok && maintenanceValue == constants.MaintenanceLabelRequested
	return hasMaintenanceState && isEnabled && isRequested && isApproved
}

type Reconciler struct {
	Log              logr.Logger
	ManagementClient client.Client
	Connections      *clusters.Connections
	Cluster          types.NamespacedName
}

func (r *Reconciler) PatchState(ctx context.Context, machine *clusterv1beta1.Machine, node *corev1.Node) error {
	originalNode := node.DeepCopy()
	originalMachine := machine.DeepCopy()
	propagate(r.Log, machine, node)

	if !equality.Semantic.DeepEqual(node, originalNode) {
		originalMarshaled, err := json.Marshal(originalNode)
		if err != nil {
			return err
		}
		currentMarshaled, err := json.Marshal(node)
		if err != nil {
			return err
		}
		patch, err := jsonpatch.CreateMergePatch(originalMarshaled, currentMarshaled)
		if err != nil {
			return err
		}
		patchParams := clusters.PatchNodeParams{
			Log:        r.Log,
			Cluster:    r.Cluster,
			Name:       node.Name,
			MergePatch: patch,
		}
		if err := r.Connections.PatchNode(ctx, patchParams); err != nil {
			return err
		}
		r.Log.Info("patched node", "node", node.Name)
	}

	if !equality.Semantic.DeepEqual(machine, originalMachine) {
		if err := r.ManagementClient.Patch(ctx, machine, client.MergeFrom(originalMachine)); err != nil {
			return err
		}
		r.Log.Info("patched machine", "machine", client.ObjectKeyFromObject(machine))
	}
	return nil
}
