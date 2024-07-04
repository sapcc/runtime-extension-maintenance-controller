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

package workload_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/sapcc/runtime-extension-maintenance-controller/clusters"
	"github.com/sapcc/runtime-extension-maintenance-controller/constants"
	"github.com/sapcc/runtime-extension-maintenance-controller/state"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clusterv1beta1 "sigs.k8s.io/cluster-api/api/v1beta1"
)

const (
	nodeName    string = "targetnode"
	machineName string = "targetmachine"
)

var _ = Describe("The NodeController", func() {

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
			constants.EnabledLabelKey: constants.EnabledLabelValue,
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
		Expect(managementClient.Delete(context.Background(), machine)).To(Succeed())
		Expect(workloadClient.Delete(context.Background(), node)).To(Succeed())
	})

	It("attaches the pre-drain hook when the maintenance-state label is attached", func() {
		originalNode := node.DeepCopy()
		node.Labels = map[string]string{state.MaintenanceStateLabelKey: "whatever"}
		Expect(workloadClient.Patch(context.Background(), node, client.MergeFrom(originalNode))).To(Succeed())
		Eventually(func(g Gomega) map[string]string {
			var result clusterv1beta1.Machine
			g.Expect(managementClient.Get(context.Background(), client.ObjectKeyFromObject(machine), &result)).To(Succeed())
			return result.Annotations
		}).Should(HaveKeyWithValue(constants.PreDrainDeleteHookAnnotationKey, constants.PreDrainDeleteHookAnnotationValue))
	})

	It("removes the pre-drain hook when approved", func() {
		originalNode := node.DeepCopy()
		node.Labels = map[string]string{state.MaintenanceStateLabelKey: "whatever"}
		Expect(workloadClient.Patch(context.Background(), node, client.MergeFrom(originalNode))).To(Succeed())
		Eventually(func(g Gomega) map[string]string {
			var result clusterv1beta1.Machine
			g.Expect(managementClient.Get(context.Background(), client.ObjectKeyFromObject(machine), &result)).To(Succeed())
			return result.Annotations
		}).Should(HaveKeyWithValue(constants.PreDrainDeleteHookAnnotationKey, constants.PreDrainDeleteHookAnnotationValue))
		originalMachine := machine.DeepCopy()
		machine.Annotations = map[string]string{
			constants.PreDrainDeleteHookAnnotationKey: constants.PreDrainDeleteHookAnnotationValue,
		}
		Expect(managementClient.Patch(context.Background(), machine, client.MergeFrom(originalMachine))).To(Succeed())
		Consistently(func(g Gomega) map[string]string {
			var result clusterv1beta1.Machine
			g.Expect(managementClient.Get(context.Background(), client.ObjectKeyFromObject(machine), &result)).To(Succeed())
			return result.Annotations
		}).Should(HaveKeyWithValue(constants.PreDrainDeleteHookAnnotationKey, constants.PreDrainDeleteHookAnnotationValue))
		originalNode = node.DeepCopy()
		node.Labels[constants.ApproveDeletionLabelKey] = constants.ApproveDeletionLabelValue
		Expect(workloadClient.Patch(context.Background(), node, client.MergeFrom(originalNode))).To(Succeed())
		Eventually(func(g Gomega) map[string]string {
			var result clusterv1beta1.Machine
			g.Expect(managementClient.Get(context.Background(), client.ObjectKeyFromObject(machine), &result)).To(Succeed())
			return result.Annotations
		}).ShouldNot(HaveKey(constants.PreDrainDeleteHookAnnotationKey))
	})

	It("removes the pre-drain hook when the controller is disabled", func() {
		originalNode := node.DeepCopy()
		node.Labels = map[string]string{state.MaintenanceStateLabelKey: "whatever"}
		Expect(workloadClient.Patch(context.Background(), node, client.MergeFrom(originalNode))).To(Succeed())
		Eventually(func(g Gomega) map[string]string {
			var result clusterv1beta1.Machine
			g.Expect(managementClient.Get(context.Background(), client.ObjectKeyFromObject(machine), &result)).To(Succeed())
			return result.Annotations
		}).Should(HaveKeyWithValue(constants.PreDrainDeleteHookAnnotationKey, constants.PreDrainDeleteHookAnnotationValue))
		originalMachine := machine.DeepCopy()
		machine.Labels = map[string]string{}
		Expect(managementClient.Patch(context.Background(), machine, client.MergeFrom(originalMachine))).To(Succeed())
		originalNode = node.DeepCopy()
		node.Labels = map[string]string{state.MaintenanceStateLabelKey: "banana"}
		Expect(workloadClient.Patch(context.Background(), node, client.MergeFrom(originalNode))).To(Succeed())
		Eventually(func(g Gomega) map[string]string {
			var result clusterv1beta1.Machine
			g.Expect(managementClient.Get(context.Background(), client.ObjectKeyFromObject(machine), &result)).To(Succeed())
			return result.Annotations
		}).ShouldNot(HaveKey(constants.PreDrainDeleteHookAnnotationKey))
	})

	// independent of this test the controllers informer still uses the original node informer
	// with the original connection
	It("can reauthenticate with different credentials", func(ctx SpecContext) {
		var secret corev1.Secret
		secret.Name = "management-kubeconfig"
		secret.Namespace = metav1.NamespaceDefault
		secret.Data = map[string][]byte{
			"value": extraKubeCfg,
		}
		Expect(managementClient.Create(ctx, &secret)).To(Succeed())

		cluster := types.NamespacedName{Namespace: metav1.NamespaceDefault, Name: "management"}
		err := connections.ReauthConn(ctx, clusters.ReauthParams{
			Cluster: cluster,
			Log:     GinkgoLogr,
		})
		Expect(err).To(Succeed())
		_, err = connections.GetNode(ctx, clusters.GetNodeParams{Log: GinkgoLogr, Cluster: cluster, Name: nodeName})
		Expect(err).To(Succeed())

		Expect(managementClient.Delete(ctx, &secret)).To(Succeed())
	}, NodeTimeout(5*time.Second))

})
