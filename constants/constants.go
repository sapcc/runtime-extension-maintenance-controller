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

package constants

const (
	ApproveDeletionLabelKey           string = "runtime-extension-maintenance-controller.cloud.sap/approve-deletion"
	ApproveDeletionLabelValue         string = "true"
	PreDrainDeleteHookAnnotationKey   string = "pre-drain.delete.hook.machine.cluster.x-k8s.io/maintenance-controller"
	PreDrainDeleteHookAnnotationValue string = "runtime-extension-maintenance-controller"
)
