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

package state

import (
	"context"
	"encoding/json"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/go-logr/logr"
	"github.com/sapcc/runtime-extension-maintenance-controller/clusters"
	"github.com/sapcc/runtime-extension-maintenance-controller/constants"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/types"
	clusterv1beta1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	MaintenanceStateLabelKey string = "cloud.sap/maintenance-state"
	MachineDeletedLabelKey   string = "runtime-extension-maintenance-controller.cloud.sap/machine-deleted"
	MachineDeletedLabelValue string = "true"
)

// Is currently invoked in parallel by management and workload controller.
func propagate(log logr.Logger, machine *clusterv1beta1.Machine, node *corev1.Node) {
	log = log.WithValues("node", node.Name, "machine", machine.Namespace+"/"+machine.Name)
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
