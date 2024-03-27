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
	"os"
	"os/signal"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sapcc/runtime-extension-maintenance-controller/clusters"
	"github.com/sapcc/runtime-extension-maintenance-controller/workload"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	corev1_informers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	clusterv1beta1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

func TestWorkload(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Workload Suite")
}

var (
	workloadEnv      *envtest.Environment
	managementEnv    *envtest.Environment
	stopController   context.CancelFunc
	workloadClient   client.Client
	managementClient client.Client
)

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	By("bootstrapping workload cluster")
	workloadEnv = &envtest.Environment{}

	Expect(corev1.AddToScheme(scheme.Scheme)).To(Succeed())
	Expect(clusterv1beta1.AddToScheme(scheme.Scheme)).To(Succeed())

	var err error

	workloadCfg, err := workloadEnv.Start()
	Expect(err).To(Succeed())
	Expect(workloadCfg).ToNot(BeNil())

	workloadClientset, err := kubernetes.NewForConfig(workloadCfg)
	Expect(err).To(Succeed())

	workloadClient, err = client.New(workloadCfg, client.Options{})
	Expect(err).To(Succeed())

	By("bootstrapping management cluster")
	managementEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{filepath.Join("..", "crd")},
	}
	managementCfg, err := managementEnv.Start()
	Expect(err).To(Succeed())
	Expect(managementCfg).ToNot(BeNil())

	managementClient, err = client.New(managementCfg, client.Options{})
	Expect(err).To(Succeed())

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	stopController = cancel

	clusterKey := types.NamespacedName{Namespace: metav1.NamespaceDefault, Name: "management"}
	connections := clusters.NewConnections(managementClient, func() context.Context { return ctx })
	controller, err := workload.NewNodeController(workload.NodeControllerOptions{
		Log:              GinkgoLogr,
		ManagementClient: managementClient,
		Connections:      connections,
		Cluster:          clusterKey,
	})
	connections.AddConn(ctx, clusterKey, clusters.NewConnection(workloadClientset, func(ni corev1_informers.NodeInformer) {
		Expect(controller.AttachTo(ni)).To(Succeed())
	}))

	Expect(err).To(Succeed())
	go func() {
		controller.Run(ctx)
	}()
})

var _ = AfterSuite(func() {
	stopController()
	By("tearing down management environment")
	Expect(managementEnv.Stop()).To(Succeed())
	By("tearing down workload environment")
	Expect(workloadEnv.Stop()).To(Succeed())
})
