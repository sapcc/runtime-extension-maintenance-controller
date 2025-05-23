// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package management_test

import (
	"context"
	"strconv"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/sapcc/runtime-extension-maintenance-controller/constants"
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
			clusterv1beta1.ClusterNameLabel: "management",
			constants.EnabledLabelKey:       constants.EnabledLabelValue,
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
			machine.Labels["timestamp"] = strconv.FormatInt(time.Now().Unix(), 10)
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

		It("removes the pre-drain hook when the controller is disabled", func() {
			originalMachine := machine.DeepCopy()
			delete(machine.Labels, constants.EnabledLabelKey)
			Expect(managementClient.Patch(context.Background(), machine, client.MergeFrom(originalMachine))).To(Succeed())
			Eventually(func(g Gomega) map[string]string {
				var result clusterv1beta1.Machine
				g.Expect(managementClient.Get(context.Background(), client.ObjectKeyFromObject(machine), &result)).To(Succeed())
				return result.Annotations
			}).ShouldNot(HaveKey(constants.PreDrainDeleteHookAnnotationKey))
		})

		It("attaches the machine maintenance label to the node on maintenance request", func(ctx SpecContext) {
			originalMachine := machine.DeepCopy()
			machine.Labels[constants.MaintenanceLabelKey] = constants.MaintenanceLabelRequested
			Expect(managementClient.Patch(ctx, machine, client.MergeFrom(originalMachine))).To(Succeed())

			Eventually(func(g Gomega) map[string]string {
				var result corev1.Node
				g.Expect(workloadClient.Get(ctx, client.ObjectKeyFromObject(node), &result)).To(Succeed())
				return result.Labels
			}).Should(HaveKeyWithValue(state.MachineMaintenanceLabelKey, state.MachineMaintenanceLabelValue))
		})

		It("updates the machine maintenance label once the maintenance is approved in the node", func(ctx SpecContext) {
			originalMachine := machine.DeepCopy()
			machine.Labels[constants.MaintenanceLabelKey] = constants.MaintenanceLabelRequested
			Expect(managementClient.Patch(ctx, machine, client.MergeFrom(originalMachine))).To(Succeed())

			orginalNode := node.DeepCopy()
			node.Labels[state.ApproveMaintenanceLabelKey] = state.ApproveMaintenanceLabelValue
			Expect(workloadClient.Patch(ctx, node, client.MergeFrom(orginalNode))).To(Succeed())

			Eventually(func(g Gomega) map[string]string {
				var result clusterv1beta1.Machine
				g.Expect(managementClient.Get(ctx, client.ObjectKeyFromObject(machine), &result)).To(Succeed())
				return result.Labels
			}).Should(HaveKeyWithValue(constants.MaintenanceLabelKey, constants.MaintenanceLabelApproved))
		})

		It("removes the machine maintenance label from the node on deletion of maintenance request", func(ctx SpecContext) {
			originalMachine := machine.DeepCopy()
			machine.Labels[constants.MaintenanceLabelKey] = constants.MaintenanceLabelRequested
			Expect(managementClient.Patch(ctx, machine, client.MergeFrom(originalMachine))).To(Succeed())

			Eventually(func(g Gomega) map[string]string {
				var result corev1.Node
				g.Expect(workloadClient.Get(ctx, client.ObjectKeyFromObject(node), &result)).To(Succeed())
				return result.Labels
			}).Should(HaveKeyWithValue(state.MachineMaintenanceLabelKey, state.MachineMaintenanceLabelValue))

			var patchedMachine clusterv1beta1.Machine
			Expect(managementClient.Get(ctx, client.ObjectKeyFromObject(machine), &patchedMachine)).To(Succeed())
			readyMachine := patchedMachine.DeepCopy()
			delete(readyMachine.Labels, constants.MaintenanceLabelKey)
			Expect(managementClient.Patch(ctx, readyMachine, client.MergeFrom(&patchedMachine))).To(Succeed())

			Eventually(func(g Gomega) map[string]string {
				var result corev1.Node
				g.Expect(workloadClient.Get(ctx, client.ObjectKeyFromObject(node), &result)).To(Succeed())
				return result.Labels
			}).ShouldNot(HaveKey(state.MachineMaintenanceLabelKey))
		})

	})

})
