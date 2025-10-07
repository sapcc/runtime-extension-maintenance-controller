// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package workload_test

import (
	"context"
	"os"
	"os/signal"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	corev1_informers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	clusterv1beta2 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/sapcc/runtime-extension-maintenance-controller/clusters"
	"github.com/sapcc/runtime-extension-maintenance-controller/workload"
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
	connections      *clusters.Connections

	extraKubeCfg []byte
)

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	By("bootstrapping workload cluster")
	workloadEnv = &envtest.Environment{}

	Expect(corev1.AddToScheme(scheme.Scheme)).To(Succeed())
	Expect(clusterv1beta2.AddToScheme(scheme.Scheme)).To(Succeed())

	workloadCfg, err := workloadEnv.Start()
	Expect(err).To(Succeed())
	Expect(workloadCfg).ToNot(BeNil())

	extraUser, err := workloadEnv.AddUser(envtest.User{Name: "extra", Groups: []string{}}, nil)
	Expect(err).To(Succeed())
	extraKubeCfg, err = extraUser.KubeConfig()
	Expect(err).To(Succeed())

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

	var clusterRoleBinding rbacv1.ClusterRoleBinding
	clusterRoleBinding.Name = "extra-binding"
	clusterRoleBinding.RoleRef = rbacv1.RoleRef{
		APIGroup: rbacv1.SchemeGroupVersion.Group,
		Kind:     "ClusterRole",
		Name:     "cluster-admin",
	}
	clusterRoleBinding.Subjects = []rbacv1.Subject{
		{
			Kind:     "User",
			Name:     "extra",
			APIGroup: rbacv1.SchemeGroupVersion.Group,
		},
	}
	Expect(workloadClient.Create(context.Background(), &clusterRoleBinding)).To(Succeed())

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	stopController = cancel

	clusterKey := types.NamespacedName{Namespace: metav1.NamespaceDefault, Name: "management"}
	connections = clusters.NewConnections(managementClient, func() context.Context { return ctx })
	controller, err := workload.NewNodeController(workload.NodeControllerOptions{
		Log:              GinkgoLogr,
		ManagementClient: managementClient,
		Connections:      connections,
		Cluster:          clusterKey,
	})
	Expect(err).To(Succeed())
	connections.AddConn(ctx, GinkgoLogr, clusterKey,
		clusters.NewConnection(
			workloadClientset,
			func(ni corev1_informers.NodeInformer) {
				Expect(controller.AttachTo(ni)).To(Succeed())
			},
		),
	)

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
