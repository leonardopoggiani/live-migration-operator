package main

import (
	"flag"
	"os"
	"time"

	"github.com/containers/buildah"
	"github.com/containers/storage/pkg/unshare"
	livemigrationv1 "github.com/leonardopoggiani/live-migration-operator/api/v1alpha1"
	"github.com/leonardopoggiani/live-migration-operator/controllers"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

var (
	scheme = runtime.NewScheme()
)

const (
	pollInterval = 30 * time.Second
)

func init() {
	// Initialize the scheme so that kubernetes dynamic client knows
	// how to work with new CRD and native kubernetes types
	_ = clientgoscheme.AddToScheme(scheme)
	_ = livemigrationv1.AddToScheme(scheme)
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	flag.StringVar(&metricsAddr, "metrics-addr", ":8082", "The address the metric endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "enable-leader-election", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.Parse()

	if buildah.InitReexec() {
		return
	}
	unshare.MaybeReexecUsingUserNamespace(false)

	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	klog.Info("Creating the LiveMigration controller manager")

	var mgr ctrl.Manager
	var configs *rest.Config
	var err error

	configs, err = ctrl.GetConfig()
	if err != nil {
		klog.Error(err, "error getting kubernetes configs")
	}

	mgr, err = ctrl.NewManager(configs, ctrl.Options{
		Scheme:           scheme,
		LeaderElection:   enableLeaderElection,
		LeaderElectionID: "a9d120af.livemigration.liqo.io",
	})

	if err != nil {
		klog.Error(err, "unable to start manager")
		time.Sleep(pollInterval)
	}

	klog.Info("Starting the LiveMigration controller manager")

	if err = (&controllers.LiveMigrationReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		os.Exit(1)
	}

	// +kubebuilder:scaffold:builder

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		os.Exit(1)
	}

}
