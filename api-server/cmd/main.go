package main

import (
	"k8s.io/klog/v2"
	"os"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"

	apiserver "github.com/leonardopoggiani/live-migration-operator/api-server"
	livemigrationv1 "github.com/leonardopoggiani/live-migration-operator/api/v1alpha1"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	kubelog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

var (
	runLog = kubelog.Log.WithName("livemigration-cp").WithName("run")
	scheme = runtime.NewScheme()
)

func init() {
	// Initialize the scheme so that kubernetes dynamic client knows
	// how to work with new CRD and native kubernetes types
	_ = clientgoscheme.AddToScheme(scheme)
	_ = livemigrationv1.AddToScheme(scheme)
}

func main() {
	kubelog.SetLogger(zap.New(zap.UseDevMode(true)))

	mgr, err := apiserver.NewManager(ctrl.GetConfigOrDie(), apiserver.Options{
		Scheme:         scheme,
		Port:           5000,
		AllowedDomains: []string{},
	})
	if err != nil {
		runLog.Error(err, "unable to create api-server manager")
		os.Exit(1)
	}

	runLog.Info("starting api-server manager")

	ctx := signals.SetupSignalHandler()
	if err != nil {
		klog.ErrorS(err, "problem running api-server manager")
		os.Exit(1)
	}

	mgr.Start(ctx)
}
