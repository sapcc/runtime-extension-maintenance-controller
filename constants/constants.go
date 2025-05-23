// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package constants

const (
	ApproveDeletionLabelKey           string = "runtime-extension-maintenance-controller.cloud.sap/approve-deletion"
	ApproveDeletionLabelValue         string = "true"
	EnabledLabelKey                   string = "runtime-extension-maintenance-controller.cloud.sap/enabled"
	EnabledLabelValue                 string = "true"
	PreDrainDeleteHookAnnotationKey   string = "pre-drain.delete.hook.machine.cluster.x-k8s.io/maintenance-controller"
	PreDrainDeleteHookAnnotationValue string = "runtime-extension-maintenance-controller"
	MaintenanceLabelKey               string = "runtime-extension-maintenance-controller.cloud.sap/maintenance"
	MaintenanceLabelRequested         string = "requested"
	MaintenanceLabelApproved          string = "approved"
)
