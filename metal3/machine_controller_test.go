// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package metal3_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/sapcc/runtime-extension-maintenance-controller/constants"
	"github.com/sapcc/runtime-extension-maintenance-controller/metal3"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"
	clusterv1beta1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("The MachineReconciler", func() {

	var baremetalHost *unstructured.Unstructured
	var machine *clusterv1beta1.Machine

	BeforeEach(func() {
		baremetalHost = &unstructured.Unstructured{}
		baremetalHost.SetName("the-host")
		baremetalHost.SetNamespace(metav1.NamespaceDefault)
		baremetalHost.SetGroupVersionKind(metal3.BaremetalHostGVK)
		Expect(managementClient.Create(context.Background(), baremetalHost)).To(Succeed())

		machine = &clusterv1beta1.Machine{}
		machine.Name = "the-machine"
		machine.Namespace = metav1.NamespaceDefault
		machine.Spec.ClusterName = "the-cluster"
		machine.Spec.ProviderID = ptr.To("metal3://default/the-host/the-machine")
		Expect(managementClient.Create(context.Background(), machine)).To(Succeed())
	})

	AfterEach(func() {
		Expect(managementClient.Delete(context.Background(), machine)).To(Succeed())
		Expect(managementClient.Delete(context.Background(), baremetalHost)).To(Succeed())
	})

	It("does not add the reboot annotation without approval", func(ctx SpecContext) {
		originalMachine := machine.DeepCopy()
		machine.Labels = map[string]string{constants.MaintenanceLabelKey: constants.MaintenanceLabelRequested}
		Expect(managementClient.Patch(ctx, machine, client.MergeFrom(originalMachine))).To(Succeed())

		Consistently(func(g Gomega) map[string]string {
			host := &unstructured.Unstructured{}
			host.SetGroupVersionKind(baremetalHost.GroupVersionKind())
			g.Expect(managementClient.Get(ctx, client.ObjectKeyFromObject(baremetalHost), host)).To(Succeed())
			return host.GetAnnotations()
		}).ShouldNot(HaveKey(metal3.RebootAnnotation))
	})

	It("adds the reboot annotation with approval", func(ctx SpecContext) {
		originalMachine := machine.DeepCopy()
		machine.Labels = map[string]string{constants.MaintenanceLabelKey: constants.MaintenanceLabelApproved}
		Expect(managementClient.Patch(ctx, machine, client.MergeFrom(originalMachine))).To(Succeed())

		Eventually(func(g Gomega) map[string]string {
			host := &unstructured.Unstructured{}
			host.SetGroupVersionKind(baremetalHost.GroupVersionKind())
			g.Expect(managementClient.Get(ctx, client.ObjectKeyFromObject(baremetalHost), host)).To(Succeed())
			return host.GetAnnotations()
		}).Should(HaveKey(metal3.RebootAnnotation))
	})

	It("removes the reboot annotation once the approval is revoked", func(ctx SpecContext) {
		originalMachine := machine.DeepCopy()
		machine.Labels = map[string]string{constants.MaintenanceLabelKey: constants.MaintenanceLabelApproved}
		Expect(managementClient.Patch(ctx, machine, client.MergeFrom(originalMachine))).To(Succeed())

		originalHost := baremetalHost.DeepCopy()
		baremetalHost.SetAnnotations(map[string]string{metal3.RebootAnnotation: ""})
		Expect(managementClient.Patch(ctx, baremetalHost, client.MergeFrom(originalHost))).To(Succeed())

		machine := &clusterv1beta1.Machine{}
		Expect(managementClient.Get(ctx, client.ObjectKeyFromObject(originalMachine), machine)).To(Succeed())
		originalMachine = machine.DeepCopy()
		machine.Labels = make(map[string]string)
		Expect(managementClient.Patch(ctx, machine, client.MergeFrom(originalMachine))).To(Succeed())

		Eventually(func(g Gomega) map[string]string {
			host := &unstructured.Unstructured{}
			host.SetGroupVersionKind(baremetalHost.GroupVersionKind())
			g.Expect(managementClient.Get(ctx, client.ObjectKeyFromObject(baremetalHost), host)).To(Succeed())
			return host.GetAnnotations()
		}).ShouldNot(HaveKey(metal3.RebootAnnotation))
	})

})
