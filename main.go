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

package main

import (
	"context"
	"flag"
	"os"

	"github.com/sapcc/runtime-extension-maintenance-controller/clusters"
	"github.com/sapcc/runtime-extension-maintenance-controller/management"
	"github.com/sapcc/runtime-extension-maintenance-controller/metal3"
	"github.com/sapcc/runtime-extension-maintenance-controller/workload"
	"go.uber.org/zap/zapcore"
	"k8s.io/apimachinery/pkg/runtime"

	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	clusterv1beta1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(clusterv1beta1.AddToScheme(scheme))
}

func main() {
	var kubecontext string
	var metal3Integration bool
	opts := zap.Options{
		Development: true,
		TimeEncoder: zapcore.ISO8601TimeEncoder,
	}
	flag.StringVar(&kubecontext, "kubecontext", "", "The context to use from the kubeconfig (defaults to current-context)")
	flag.BoolVar(&metal3Integration, "enable-metal3-maintenance", false, "Enables an additional controller that manages reboot annotations on metal3 BareMetalHost objects")
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	restConfig := getKubeconfigOrDie(kubecontext)
	setupLog.Info("loaded kubeconfig", "context", kubecontext, "host", restConfig.Host)

	mgr, err := ctrl.NewManager(restConfig, ctrl.Options{
		Scheme:           scheme,
		LeaderElection:   true,
		LeaderElectionID: "runtime-extension-maintenance-controller",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	ctx := ctrl.SetupSignalHandler()
	connections := clusters.NewConnections(mgr.GetClient(), func() context.Context { return ctx })
	err = (&management.MachineReconciler{
		Client:                  mgr.GetClient(),
		Log:                     ctrl.Log.WithName("management"),
		WorkloadNodeControllers: map[string]*workload.NodeController{},
		CancelFuncs:             map[string]context.CancelFunc{},
		WorkloadContextFunc: func() context.Context {
			return ctx
		},
		ClusterConnections: connections,
	}).SetupWithManager(mgr)
	if err != nil {
		setupLog.Error(err, "unable to create machine management controller")
		os.Exit(1)
	}

	if metal3Integration {
		err = (&metal3.MachineReconciler{
			Client: mgr.GetClient(),
			Log:    ctrl.Log.WithName("metal3"),
		}).SetupWithManager(mgr)
		if err != nil {
			setupLog.Error(err, "unable to create machine metal3 controller")
			os.Exit(1)
		}
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
	setupLog.Info("received SIGTERM or SIGINT. See you later.")
}

func getKubeconfigOrDie(kubecontext string) *rest.Config {
	if kubecontext == "" {
		kubecontext = os.Getenv("KUBECONTEXT")
	}
	restConfig, err := ctrlconfig.GetConfigWithContext(kubecontext)
	if err != nil {
		setupLog.Error(err, "Failed to load kubeconfig")
		os.Exit(1)
	}
	return restConfig
}
