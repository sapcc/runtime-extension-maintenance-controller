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

package management_test

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sapcc/runtime-extension-maintenance-controller/constants"
	"github.com/sapcc/runtime-extension-maintenance-controller/management"
	"github.com/sapcc/runtime-extension-maintenance-controller/state"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1beta1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	nodeName    string = "targetnode"
	machineName string = "targetmachine"
)

var _ = Describe("The MachineReconciler", func() {

	var node *corev1.Node
	var machine *clusterv1beta1.Machine

	BeforeEach(func() {
		node = &corev1.Node{}
		node.Name = nodeName
		node.Annotations = map[string]string{
			clusterv1beta1.MachineAnnotation:          machineName,
			clusterv1beta1.ClusterNamespaceAnnotation: metav1.NamespaceDefault,
		}
		Expect(workloadClient.Create(context.Background(), node)).To(Succeed())

		machine = &clusterv1beta1.Machine{}
		machine.Name = machineName
		machine.Namespace = metav1.NamespaceDefault
		machine.Labels = map[string]string{
			clusterv1beta1.ClusterNameLabel:          "management",
			management.MaintenanceControllerLabelKey: management.MaintenanceControllerLabelValue,
		}
		machine.Spec.ClusterName = "management"
		Expect(managementClient.Create(context.Background(), machine)).To(Succeed())
		machine.Status.NodeRef = &corev1.ObjectReference{
			Kind:       node.Kind,
			Name:       node.Name,
			UID:        node.UID,
			APIVersion: node.APIVersion,
		}
		Expect(managementClient.Status().Update(context.Background(), machine)).To(Succeed())
	})

	AfterEach(func() {
		originalMachine := machine.DeepCopy()
		if len(machine.Finalizers) > 0 {
			machine.Finalizers = []string{}
			Expect(managementClient.Patch(context.Background(), machine, client.MergeFrom(originalMachine))).To(Succeed())
		} else {
			Expect(managementClient.Delete(context.Background(), machine)).To(Succeed())
		}
		Expect(workloadClient.Delete(context.Background(), node)).To(Succeed())
	})

	It("does not attach the pre-drain hook", func() {
		Consistently(func(g Gomega) map[string]string {
			var result clusterv1beta1.Machine
			g.Expect(managementClient.Get(context.Background(), client.ObjectKeyFromObject(machine), &result)).To(Succeed())
			return result.Annotations
		}).ShouldNot(HaveKey(constants.PreDrainDeleteHookAnnotationKey))
	})

	It("does not add the machine deletion label", func() {
		Consistently(func(g Gomega) map[string]string {
			var result corev1.Node
			g.Expect(workloadClient.Get(context.Background(), client.ObjectKeyFromObject(node), &result)).To(Succeed())
			return result.Labels
		}).ShouldNot(HaveKey(state.MachineDeletedLabelKey))
	})

	When("the node is managed by the maintenance-controller", func() {

		BeforeEach(func() {
			originalNode := node.DeepCopy()
			node.Labels = map[string]string{state.MaintenanceStateLabelKey: "operational"}
			Expect(workloadClient.Patch(context.Background(), node, client.MergeFrom(originalNode))).To(Succeed())
			// need to patch the machine to trigger a reconcile
			originalMachine := machine.DeepCopy()
			machine.Labels["timestamp"] = fmt.Sprint(time.Now().Unix())
			Expect(managementClient.Patch(context.Background(), machine, client.MergeFrom(originalMachine))).To(Succeed())
		})

		It("attaches the machine deletion label to the node on machine deletion", func() {
			originalMachine := machine.DeepCopy()
			machine.Finalizers = []string{"test-finalizer"}
			Expect(managementClient.Patch(context.Background(), machine, client.MergeFrom(originalMachine))).To(Succeed())
			Expect(managementClient.Delete(context.Background(), machine)).To(Succeed())

			Eventually(func(g Gomega) map[string]string {
				var result corev1.Node
				g.Expect(workloadClient.Get(context.Background(), client.ObjectKeyFromObject(node), &result)).To(Succeed())
				return result.Labels
			}).Should(HaveKeyWithValue(state.MachineDeletedLabelKey, state.MachineDeletedLabelValue))
		})

		It("attaches the pre-drain hook", func() {
			Eventually(func(g Gomega) map[string]string {
				var result clusterv1beta1.Machine
				g.Expect(managementClient.Get(context.Background(), client.ObjectKeyFromObject(machine), &result)).To(Succeed())
				return result.Annotations
			}).Should(HaveKeyWithValue(constants.PreDrainDeleteHookAnnotationKey, constants.PreDrainDeleteHookAnnotationValue))
		})

		// this test works due to the workload node controller.
		// it does not test the machine controller in isolation.
		It("removes pre-drain hook when machine deletion is approved", func() {
			Eventually(func(g Gomega) map[string]string {
				var result clusterv1beta1.Machine
				g.Expect(managementClient.Get(context.Background(), client.ObjectKeyFromObject(machine), &result)).To(Succeed())
				return result.Annotations
			}).Should(HaveKeyWithValue(constants.PreDrainDeleteHookAnnotationKey, constants.PreDrainDeleteHookAnnotationValue))
			originalNode := node.DeepCopy()
			node.Labels = map[string]string{constants.ApproveDeletionLabelKey: constants.ApproveDeletionLabelValue}
			Expect(workloadClient.Patch(context.Background(), node, client.MergeFrom(originalNode))).To(Succeed())
			Eventually(func(g Gomega) map[string]string {
				var result clusterv1beta1.Machine
				g.Expect(managementClient.Get(context.Background(), client.ObjectKeyFromObject(machine), &result)).To(Succeed())
				return result.Annotations
			}).ShouldNot(HaveKey(constants.PreDrainDeleteHookAnnotationKey))
		})

	})

})
