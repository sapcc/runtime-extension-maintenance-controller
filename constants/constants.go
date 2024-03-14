package constants

const (
	ApproveDeletionLabelKey           string = "runtime-extension-maintenance-controller.cloud.sap/approve-deletion"
	ApproveDeletionLabelValue         string = "true"
	PreDrainDeleteHookAnnotationKey   string = "pre-drain.delete.hook.machine.cluster.x-k8s.io/maintenance-controller"
	PreDrainDeleteHookAnnotationValue string = "runtime-extension-maintenance-controller"
)
