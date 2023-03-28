package main

import (
	"context"
	"flag"
	"github.com/containers/buildah"
	"github.com/containers/storage/pkg/unshare"
	livemigrationv1 "github.com/leonardopoggiani/live-migration-operator/api/v1alpha1"
	"github.com/leonardopoggiani/live-migration-operator/controllers"
	"github.com/leonardopoggiani/live-migration-operator/storage-provisioner"
	"k8s.io/klog/v2"
	"os"

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

	klog.Infof("Creating the LiveMigration controller manager")

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:             scheme,
		MetricsBindAddress: metricsAddr,
		Port:               9443,
		LeaderElection:     enableLeaderElection,
		LeaderElectionID:   "a9ee352c.livemigration.liqo.io",
	})
	if err != nil {
		os.Exit(1)
	}

	klog.Infof("Starting the LiveMigration controller manager")

	//ctx context.Context, cl client.Client,
	//	virtualStorageClassName, storageNamespace, localRealStorageClass string) (controller.Provisioner, error) {
	prov, err := storage_provisioner.NewCheckpointProvisioner(context.Background(), mgr.GetClient(), "liqo-demo-storage")
	if err != nil {
		klog.Error(err, "unable to create controller", "controller", "CheckpointProvisioner")
		os.Exit(1)
	} else {
		// if err = prov.SetupWithManager(mgr); err != nil {
		//	setupLog.Error(err, "unable to create controller", "controller", "CheckpointProvisioner")
		//	os.Exit(1)
		//}
		klog.Infof("", "CheckpointProvisioner created", prov)
	}

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
