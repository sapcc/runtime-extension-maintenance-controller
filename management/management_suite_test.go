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
	"os"
	"os/signal"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sapcc/runtime-extension-maintenance-controller/management"
	"github.com/sapcc/runtime-extension-maintenance-controller/workload"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	clusterv1beta1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

func TestManagement(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Management Suite")
}

var (
	workloadEnv      *envtest.Environment
	managementEnv    *envtest.Environment
	stopController   context.CancelFunc
	workloadClient   client.Client
	managementClient client.Client
	k8sManager       ctrl.Manager
)

func KubeconfigForRestConfig(restConfig *rest.Config) ([]byte, error) {
	clusters := make(map[string]*clientcmdapi.Cluster)
	clusters["default-cluster"] = &clientcmdapi.Cluster{
		Server:                   restConfig.Host,
		CertificateAuthorityData: restConfig.CAData,
	}
	contexts := make(map[string]*clientcmdapi.Context)
	contexts["default-context"] = &clientcmdapi.Context{
		Cluster:  "default-cluster",
		AuthInfo: "default-user",
	}
	authinfos := make(map[string]*clientcmdapi.AuthInfo)
	authinfos["default-user"] = &clientcmdapi.AuthInfo{
		ClientCertificateData: restConfig.CertData,
		ClientKeyData:         restConfig.KeyData,
	}
	clientConfig := clientcmdapi.Config{
		Kind:           "Config",
		APIVersion:     "v1",
		Clusters:       clusters,
		Contexts:       contexts,
		CurrentContext: "default-context",
		AuthInfos:      authinfos,
	}
	return clientcmd.Write(clientConfig)
}

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

	kubeConfig, err := KubeconfigForRestConfig(workloadCfg)
	Expect(err).To(Succeed())
	var kubeConfigSecret corev1.Secret
	kubeConfigSecret.Name = "management-kubeconfig"
	kubeConfigSecret.Namespace = metav1.NamespaceDefault
	kubeConfigSecret.Type = corev1.SecretTypeOpaque
	kubeConfigSecret.Data = map[string][]byte{
		"value": kubeConfig,
	}
	Expect(managementClient.Create(context.Background(), &kubeConfigSecret)).To(Succeed())

	k8sManager, err = ctrl.NewManager(managementCfg, ctrl.Options{
		Scheme: scheme.Scheme,
		Metrics: metricsserver.Options{
			BindAddress: "0",
		},
	})
	Expect(err).To(Succeed())

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	stopController = cancel

	connections := workload.NewClusterConnections(managementClient, func() context.Context { return ctx })
	err = (&management.MachineReconciler{
		Client:                  managementClient,
		Log:                     GinkgoLogr,
		WorkloadNodeControllers: map[string]*workload.NodeController{},
		CancelFuncs:             map[string]context.CancelFunc{},
		WorkloadContextFunc: func() context.Context {
			return ctx
		},
		ClusterConnections: connections,
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
	By("tearing down workload environment")
	Expect(workloadEnv.Stop()).To(Succeed())
})
