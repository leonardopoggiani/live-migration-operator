package controllers

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/coocood/freecache"
	"github.com/fsnotify/fsnotify"
	"k8s.io/client-go/rest"

	api "github.com/leonardopoggiani/live-migration-operator/api/v1alpha1"
	dummy "github.com/leonardopoggiani/live-migration-operator/controllers/dummy"
	utils "github.com/leonardopoggiani/live-migration-operator/controllers/utils"
	corev1 "k8s.io/api/core/v1"
	run "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type LiveMigrationReconciler struct {
	client.Client
	Scheme *run.Scheme
}

var cacheSize = 100 * 1024 * 1024 // 100MB
var fileCache = freecache.NewCache(cacheSize)

// LiveMigrationReconciler reconciles a LiveMigration object

//+kubebuilder:rbac:groups=livemigration.liqo.io,resources=livemigrations,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=livemigration.liqo.io,resources=livemigrations/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=livemigration.liqo.io,resources=livemigrations/finalizers,verbs=update

func (r *LiveMigrationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	klog.Info("[INFO] ", "Reconciling LiveMigration %s \n", req.Name)

	namespace := os.Getenv("NAMESPACE")
	if namespace == "" {
		namespace = "test"
	}

	// Load Kubernetes config
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
		return ctrl.Result{}, err
	}

	// Create Kubernetes API client
	clientset, err := kubernetes.NewForConfig(kubeconfig)
	if err != nil {
		klog.ErrorS(err, "failed to create Kubernetes client")
		return ctrl.Result{}, err
	}

	// Load the LiveMigration resource object, if there is no Object, return directly
	var migratingPod api.LiveMigration
	if err := r.Get(ctx, req.NamespacedName, &migratingPod); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	var template *corev1.PodTemplateSpec
	var annotations map[string]string

	klog.Info("[INFO] ", "migratingPod.Spec.SourcePod %s, destHost %s \n", migratingPod.Spec.SourcePod, migratingPod.Spec.DestHost)

	if migratingPod.Spec.Template.ObjectMeta.Name != "" {
		_ = &migratingPod.Spec.Template
	} else {
		template, err = utils.GetSourcePodTemplate(clientset, migratingPod.Spec.SourcePod, namespace)
		if err != nil || template == nil {
			err = dummy.CreateDummyPod(clientset, ctx, namespace)
			if err != nil {
				klog.ErrorS(err, "failed to create dummy pod")
			} else {
				klog.Info("[INFO] ", "dummy pod created")
			}

			err = dummy.CreateDummyService(clientset, ctx, namespace)
			if err != nil {
				klog.ErrorS(err, "failed to create dummy service")
			}
		} else {
			if migratingPod.Spec.DestHost != "" {
				template.Spec.NodeSelector = map[string]string{"kubernetes.io/hostname": migratingPod.Spec.DestHost}
			}

			desiredLabels := getPodsLabelSet(template)
			desiredLabels["migratingPod"] = migratingPod.Name

			annotations = getPodsAnnotationSet(&migratingPod, template)

			// Then list all pods controlled by the LiveMigration resource object
			var childPods corev1.PodList
			if err := r.List(ctx, &childPods, client.InNamespace(req.Namespace), client.MatchingLabels(desiredLabels)); err != nil {
				klog.ErrorS(err, "unable to list child pods")
				return ctrl.Result{}, err
			}

			_, err := r.desiredPod(migratingPod, &migratingPod, req.Namespace, template)
			if err != nil {
				return ctrl.Result{}, err
			}

			_, err = r.desiredDeployment(migratingPod, &migratingPod, req.Namespace, template)
			if err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	// Use a channel to signal when a migration has been triggered
	migrationDone := make(chan struct{})

	// Start a goroutine to watch for file events
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		klog.ErrorS(err, "failed to create watcher")
	}
	defer func(watcher *fsnotify.Watcher) {
		err := watcher.Close()
		if err != nil {
			klog.ErrorS(err, "failed to close watcher")
		}
	}(watcher)

	// Use a channel to signal when the sourcePod has been detected
	sourcePodDetected := make(chan *corev1.Pod)

	// Use a context to cancel the loop that checks for sourcePod
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		for {
			select {
			case event := <-watcher.Events:
				if event.Op&fsnotify.Create == fsnotify.Create {
					klog.Info("[INFO] ", "Event: %s", event.String())

					if strings.Contains(event.Name, "/checkpoints/") && strings.Contains(event.Name, "dummy") {
						// restoredPod, err := r.buildahRestore(ctx, "/checkpoints/")
						// restoredPod, err := r.buildahRestoreParallelized(ctx, "/checkpoints/")
						restoredPod, err := r.BuildahRestorePipelined(ctx, "/checkpoints/", clientset, namespace)
						if err != nil {
							klog.ErrorS(err, "failed to restore pod")
						} else {
							klog.Info("[INFO] ", "restore result %s", restoredPod.Status)
						}
					}
				}
			case err := <-watcher.Errors:
				klog.ErrorS(err, "failed to watch file")
			case <-ctx.Done():
				klog.Info("[INFO] ", "stop checking for sourcePod")
				return
			}
		}
	}()

	err = watcher.Add("/checkpoints/")
	if err != nil {
		klog.ErrorS(err, "failed to add watcher")
	} else {
		klog.Info("[INFO] ", "watcher added")
	}

	// Start a goroutine to check for the sourcePod in a loop
	go func() {
		for {
			select {
			case <-ctx.Done():
				klog.Info("[INFO] ", "stop checking for sourcePod")
				return
			default:
				sourcePod, err := utils.CheckPodExist(clientset, annotations["sourcePod"], namespace)
				if err != nil {
					klog.ErrorS(err, "failed to get sourcePod ", annotations["sourcePod"])
					return
				}
				if sourcePod != nil {
					_, err := r.MigratePodPipelined(ctx, clientset, &migratingPod, namespace)
					// _, err = r.migratePod(ctx, clientset, &migratingPod)
					if err != nil {
						klog.ErrorS(err, "failed to migrate pod ", sourcePod.Name)
						return
					}
					return
				} else {
					klog.Info("[INFO] ", "sourcePod not found yet, wait and retry..")
					time.Sleep(5 * time.Second)
				}
			}
		}
	}()

	// Wait for either the file or the sourcePod to be detected
	select {
	case <-sourcePodDetected:
		// Stop watching for file events and checking for the sourcePod
		cancel()
		return ctrl.Result{}, nil
	case <-migrationDone:
		// Stop watching for file events and checking for the sourcePod
		cancel()
		return ctrl.Result{}, nil
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *LiveMigrationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&api.LiveMigration{}).
		Complete(r)
}

func (r *LiveMigrationReconciler) RemoveCheckpointPod(ctx context.Context, clientset *kubernetes.Clientset, pod *corev1.Pod, snapshotPathCurrent, newPodName string, namespace string) error {
	if newPodName != "" {
		for {
			ok, _ := utils.CheckPodExist(clientset, newPodName, namespace)
			if ok != nil {
				break
			}
		}
	}
	snapshotPolicyUpdate := ""
	snapshotPathUpdate := ""
	if err := r.updateAnnotations(ctx, pod, snapshotPolicyUpdate, snapshotPathUpdate); err != nil {
		return err
	}

	err := os.Chmod(snapshotPathCurrent, 0777)
	if err != nil {
		return err
	}
	if _, err := exec.Command("sudo", "rm", "-rf", snapshotPathCurrent).Output(); err != nil {
		return err
	}
	return nil
}

func (r *LiveMigrationReconciler) updateAnnotations(ctx context.Context, pod *corev1.Pod, snapshotPolicy, snapshotPath string) error {
	ann := pod.ObjectMeta.Annotations
	if ann == nil {
		ann = make(map[string]string)
	}
	ann["snapshotPolicy"] = snapshotPolicy
	ann["snapshotPath"] = snapshotPath
	pod.ObjectMeta.Annotations = ann
	if err := r.Update(ctx, pod); err != nil {
		return err
	}
	return nil
}

func processFile(file os.DirEntry, path string) ([]corev1.Container, string, error) {
	// Check if file is a dummy file
	if file.Name() == "dummy" {
		klog.Info("dummy file read")
		return nil, "dummy", nil
	}

	// Get the path to the checkpoint file
	checkpointPath := filepath.Join(path, file.Name())
	klog.Info("[INFO] ", "checkpointPath: ", checkpointPath)

	containerName := utils.RetrieveContainerName(checkpointPath)
	podName := utils.RetrievePodName(checkpointPath)

	results := make(chan string, 2)

	err := os.Chmod(checkpointPath, 0644)
	if err != nil {
		klog.ErrorS(err, "failed to change permissions of checkpoint file ", "checkpointPath", checkpointPath)
		return nil, "", err
	}

	var newContainer string

	// Create a new container
	newContainerCmd := exec.Command("sudo", "buildah", "from", "scratch")

	newContainerOutput, err := newContainerCmd.Output()
	if err != nil {
		klog.ErrorS(err, "failed to create new container ", "containerID", newContainerOutput)
		results <- "failed to create new container\n"
	}

	newContainerOutput = bytes.TrimRight(newContainerOutput, "\n") // remove trailing newline
	newContainer = string(newContainerOutput)

	klog.Infof("[INFO] new container name: %s", newContainer)

	addCheckpointCmd := exec.Command("sudo", "buildah", "add", newContainer, checkpointPath, "/")

	out, err := addCheckpointCmd.CombinedOutput()
	if err != nil {
		klog.ErrorS(err, "failed to add file to container")
		results <- string(out)
	}

	// Add an annotation to the container with the checkpoint name
	annotation := fmt.Sprintf("--annotation=io.kubernetes.cri-o.annotations.checkpoint.name=%s", containerName)
	klog.Infof("[INFO] annotation: %s", annotation)

	configCheckpointCmd := exec.Command("sudo", "buildah", "config", annotation, newContainer)

	out, err = configCheckpointCmd.CombinedOutput()
	if err != nil {
		klog.ErrorS(err, "failed to add checkpoint to container")
		results <- "failed to add checkpoint to container "
	} else {
		klog.Infof("[INFO] Checkpoint added to container: %s", string(out))
	}

	localCheckpointPath := "leonardopoggiani/checkpoint-images:" + containerName
	klog.Infof("[INFO] localCheckpointPath: %s", localCheckpointPath)

	commitCheckpointCmd := exec.Command("sudo", "buildah", "commit", newContainer, localCheckpointPath)

	out, err = commitCheckpointCmd.CombinedOutput()
	if err != nil {
		klog.ErrorS(err, "failed to commit checkpoint")
		results <- "failed to commit checkpoint"
	} else {
		klog.Infof("[INFO] Checkpoint committed %s", string(out))
	}

	removeContainerCmd := exec.Command("sudo", "buildah", "rm", newContainer)

	out, err = removeContainerCmd.CombinedOutput()
	if err != nil {
		klog.ErrorS(err, "failed to remove container")
		klog.Infof("[INFO] out: %s", out)
	}

	results <- "File added to container: " + string(out)

	addContainer := corev1.Container{
		Name:  containerName,
		Image: "localhost/leonardopoggiani/checkpoint-images:" + containerName,
	}

	var containersList []corev1.Container
	containersList = append(containersList, addContainer)

	return containersList, podName, nil
}

func (r *LiveMigrationReconciler) NewKubernetesClient() (*kubernetes.Clientset, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get in-cluster config: %v", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %v", err)
	}

	return clientset, nil
}
