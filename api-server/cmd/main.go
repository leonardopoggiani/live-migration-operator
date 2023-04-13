package main

import (
	"flag"
	"github.com/containers/buildah"
	"github.com/containers/storage/pkg/unshare"
	livemigrationv1 "github.com/leonardopoggiani/live-migration-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"
	"os"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

var (
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

	/*
		prov, err := storageprovisioner.NewCheckpointProvisioner(context.Background(), mgr.GetClient(), "liqo-demo-storage")
		if err != nil {
			klog.Error(err, "unable to create controller", "controller", "CheckpointProvisioner")
			os.Exit(1)
		}

		kubeconfigPath := os.Getenv("KUBECONFIG")
		if kubeconfigPath == "" {
			kubeconfigPath = "~/.kube/config"
		}

		kubeconfigPath = os.ExpandEnv(kubeconfigPath)
		if _, err := os.Stat(kubeconfigPath); os.IsNotExist(err) {
			klog.ErrorS(err, "kubeconfig file not existing")
		}

		kubeconfig, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
		if err != nil {
			klog.ErrorS(err, "Failed to retrieve kubeconfig")
		}

		// Create Kubernetes API client
		clientset, err := kubernetes.NewForConfig(kubeconfig)
		if err != nil {
			klog.ErrorS(err, "failed to create Kubernetes client")
		}

		provisionController := controller.NewProvisionController(clientset, "checkpoint-provisioner", prov, controller.LeaderElection(false))
		err = mgr.Add(storageprovisioner.StorageControllerRunnable{
			Ctrl: provisionController,
		})

		if err != nil {
			klog.Fatal(err)
		} else {
			klog.Info("Checkpoint provisioner controller started")
			klog.Infof("controller hasRun -> %v", provisionController.HasRun())
		}

		if err = (&controllers.LiveMigrationReconciler{
			Client: mgr.GetClient(),
			Scheme: mgr.GetScheme(),
		}).SetupWithManager(mgr); err != nil {
			os.Exit(1)
		}

	*/
	// +kubebuilder:scaffold:builder

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		os.Exit(1)
	}

}
