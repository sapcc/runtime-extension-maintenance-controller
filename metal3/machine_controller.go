// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package metal3

import (
	"context"
	"errors"
	"strings"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clusterv1beta2 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	"github.com/sapcc/runtime-extension-maintenance-controller/constants"
)

const (
	RebootAnnotation            string = "reboot.metal3.io/runtime-extension-maintenance-controller"
	baremetalHostNamespaceIndex int    = 2
	baremetalHostNameIndex      int    = 3
)

var BaremetalHostGVK = schema.GroupVersionKind{
	Group:   "metal3.io",
	Version: "v1alpha1",
	Kind:    "BareMetalHost",
}

type MachineReconciler struct {
	client.Client
	Log logr.Logger
}

func (r *MachineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var machine clusterv1beta2.Machine
	err := r.Get(ctx, req.NamespacedName, &machine)
	if apierrors.IsNotFound(err) {
		r.Log.Info("failed to get machine, was it deleted?", "machine", req.String())
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	baremetalHost, err := hostFromProviderID(machine.Spec.ProviderID)
	if err != nil {
		r.Log.Info("failed to derive baremetal host from machine object", "reason", err)
		return ctrl.Result{}, nil
	}
	err = r.Get(ctx, client.ObjectKeyFromObject(baremetalHost), baremetalHost)
	originalBaremetalHost := baremetalHost.DeepCopy()
	if apierrors.IsNotFound(err) {
		r.Log.Info("failed to get baremetalhost, was it deleted?", "baremetalhost", baremetalHost.GetName())
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	annotations := baremetalHost.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	maintenanceValue, ok := machine.Labels[constants.MaintenanceLabelKey]
	if ok && maintenanceValue == constants.MaintenanceLabelApproved {
		annotations[RebootAnnotation] = ""
	} else {
		delete(annotations, RebootAnnotation)
	}
	baremetalHost.SetAnnotations(annotations)
	if err = r.Patch(ctx, baremetalHost, client.MergeFrom(originalBaremetalHost)); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func hostFromProviderID(providerID string) (*unstructured.Unstructured, error) {
	providerParts := strings.Split(providerID, "/")
	if providerParts[0] != "metal3:" {
		return nil, errors.New("machine is not managed by metal3")
	}
	if len(providerParts) < baremetalHostNameIndex+1 {
		return nil, errors.New("machine providerID is malformed")
	}
	baremetalHost := &unstructured.Unstructured{}
	baremetalHost.SetName(providerParts[baremetalHostNameIndex])
	baremetalHost.SetNamespace(providerParts[baremetalHostNamespaceIndex])
	baremetalHost.SetGroupVersionKind(BaremetalHostGVK)
	return baremetalHost, nil
}

func (r *MachineReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 1,
		}).
		For(&clusterv1beta2.Machine{}).
		Named("metal3").
		Complete(r)
}
