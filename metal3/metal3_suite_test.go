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

package metal3_test

import (
	"context"
	"os"
	"os/signal"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sapcc/runtime-extension-maintenance-controller/metal3"
	"k8s.io/client-go/kubernetes/scheme"
	clusterv1beta1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

func TestMetal3(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Metal3 Suite")
}

var (
	managementEnv    *envtest.Environment
	stopController   context.CancelFunc
	managementClient client.Client
	k8sManager       ctrl.Manager
)

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	By("bootstrapping management cluster")
	Expect(clusterv1beta1.AddToScheme(scheme.Scheme)).To(Succeed())

	managementEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{filepath.Join("..", "crd")},
	}
	managementCfg, err := managementEnv.Start()
	Expect(err).To(Succeed())
	Expect(managementCfg).ToNot(BeNil())

	managementClient, err = client.New(managementCfg, client.Options{})
	Expect(err).To(Succeed())

	k8sManager, err = ctrl.NewManager(managementCfg, ctrl.Options{
		Scheme: scheme.Scheme,
		Metrics: metricsserver.Options{
			BindAddress: "0",
		},
	})
	Expect(err).To(Succeed())

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	stopController = cancel

	err = (&metal3.MachineReconciler{
		Client: managementClient,
		Log:    GinkgoLogr,
	}).SetupWithManager(k8sManager)
	Expect(err).To(Succeed())

	go func() {
		_ = k8sManager.Start(ctx)
	}()
})

var _ = AfterSuite(func() {
	stopController()
	By("tearing down management environment")
	Expect(managementEnv.Stop()).To(Succeed())
})
