package controllers

import (
	"bytes"
	"context"
	"fmt"
	"github.com/fsnotify/fsnotify"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/rest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	api "github.com/leonardopoggiani/live-migration-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	run "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// LiveMigrationReconciler reconciles a LiveMigration object
type LiveMigrationReconciler struct {
	client.Client
	Scheme *run.Scheme
}

//+kubebuilder:rbac:groups=livemigration.liqo.io,resources=livemigrations,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=livemigration.liqo.io,resources=livemigrations/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=livemigration.liqo.io,resources=livemigrations/finalizers,verbs=update

func (r *LiveMigrationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	klog.Infof("Reconciling LiveMigration %s", req.Name)

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

	klog.Infof("migratingPod.Spec.SourcePod ", migratingPod.Spec.SourcePod, "migratingPod.Spec.DestHost ", migratingPod.Spec.DestHost)

	if migratingPod.Spec.Template.ObjectMeta.Name != "" {
		template = &migratingPod.Spec.Template
	} else {
		template, err = r.GetSourcePodTemplate(clientset, migratingPod.Spec.SourcePod)
		if err != nil || template == nil {
			err = r.CreateDummyPod(clientset, ctx)
			if err != nil {
				klog.ErrorS(err, "failed to create dummy pod")
			} else {
				klog.Infof("dummy pod created")
			}

			// get the IP address of the Service
			err = r.CreateDummyService(clientset, ctx)
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
					klog.Infof("Event: %s", event.String())

					if strings.Contains(event.Name, "/checkpoints/") && strings.Contains(event.Name, "dummy") {
						// restoredPod, err := r.buildahRestore(ctx, "/checkpoints/")
						// restoredPod, err := r.buildahRestoreParallelized(ctx, "/checkpoints/")
						restoredPod, err := r.buildahRestorePipelined(ctx, "/checkpoints/")
						if err != nil {
							klog.ErrorS(err, "failed to restore pod")
						} else {
							klog.Infof("restore result ", restoredPod.Status)
						}
					}
				}
			case err := <-watcher.Errors:
				klog.ErrorS(err, "failed to watch file")
			case <-ctx.Done():
				klog.Infof("stop checking for sourcePod")
				return
			}
		}
	}()

	err = watcher.Add("/checkpoints/")
	if err != nil {
		klog.ErrorS(err, "failed to add watcher")
	} else {
		klog.Infof("watcher added")
	}

	// Start a goroutine to check for the sourcePod in a loop
	go func() {
		for {
			select {
			case <-ctx.Done():
				klog.Infof("stop checking for sourcePod")
				return
			default:
				sourcePod, err := r.checkPodExist(clientset, annotations["sourcePod"])
				if err != nil {
					klog.ErrorS(err, "failed to get sourcePod", "pod", annotations["sourcePod"])
					return
				}
				if sourcePod != nil {
					_, err := r.migratePodPipelined(ctx, clientset, &migratingPod)
					// _, err = r.migratePod(ctx, clientset, &migratingPod)
					if err != nil {
						klog.ErrorS(err, "failed to migrate pod", "pod", sourcePod.Name)
						return
					}
					return
				} else {
					klog.Infof("sourcePod not found yet, wait and retry..")
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

func (r *LiveMigrationReconciler) getActualRunningPod(childPods *corev1.PodList) (int, corev1.PodList, corev1.PodList) {
	// if a pod is deleted, remove it from Actual running pod list
	count := 0
	var actualRunningPod, isDeletingPod corev1.PodList
	for _, pod := range childPods.Items {
		if !pod.DeletionTimestamp.IsZero() {
			isDeletingPod.Items = append(isDeletingPod.Items, pod)
		} else {
			actualRunningPod.Items = append(actualRunningPod.Items, pod)
			count++
		}
	}
	return count, actualRunningPod, isDeletingPod
}

func (r *LiveMigrationReconciler) createMultiPod(ctx context.Context, depl *appsv1.Deployment) error {
	if err := r.Create(ctx, depl); err != nil {
		return err
	}
	return nil
}

func (r *LiveMigrationReconciler) updateMultiPod(ctx context.Context, depl *appsv1.Deployment) error {
	if err := r.Update(ctx, depl); err != nil {
		return err
	}
	return nil
}

func (r *LiveMigrationReconciler) deletePod(ctx context.Context, pod *corev1.Pod) error {
	if err := r.Delete(ctx, pod); err != nil {
		return err
	}
	return nil
}

func (r *LiveMigrationReconciler) checkPodExist(clientset *kubernetes.Clientset, name string) (*corev1.Pod, error) {

	existingPod, err := clientset.CoreV1().Pods("default").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		klog.ErrorS(err, "unable to fetch Pod", "pod", name)
		return nil, err
	} else {
		klog.Infof("", "pod exists", "pod", existingPod.Name)
		return existingPod, nil
	}
}

func (r *LiveMigrationReconciler) GetSourcePodTemplate(clientset *kubernetes.Clientset, sourcePodName string) (*corev1.PodTemplateSpec, error) {
	klog.Infof("", "getSourcePodTemplate", "sourcePodName", sourcePodName)
	retrievedPod, err := r.checkPodExist(clientset, sourcePodName)
	if retrievedPod == nil {
		return nil, err
	} else {
		klog.Infof("", "getSourcePodTemplate", "sourcePod", retrievedPod.Name)
	}

	copyPod := retrievedPod.DeepCopy()
	containerList := copyPod.Spec.Containers
	template := &corev1.PodTemplateSpec{
		ObjectMeta: copyPod.ObjectMeta,
		Spec: corev1.PodSpec{
			Containers: containerList,
			Volumes:    copyPod.Spec.Volumes,
		},
	}
	return template, nil
}

func (r *LiveMigrationReconciler) removeCheckpointPod(ctx context.Context, clientset *kubernetes.Clientset, pod *corev1.Pod, snapshotPathCurrent, newPodName string) error {
	if newPodName != "" {
		for {
			ok, _ := r.checkPodExist(clientset, newPodName)
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

func checkpointPodCrio(containers []Container, namespace string, podName string) error {
	// curl -sk -XPOST "https://localhost:10250/checkpoint/liqo-demo/tomcat-pod/tomcat"

	for _, container := range containers {
		curlPath := "https://localhost:10250/checkpoint/" + namespace + "/" + podName + "/" + container.Name
		checkpointCmd := exec.Command("curl", "-sk", "-XPOST", curlPath)

		output, err := checkpointCmd.CombinedOutput()
		if err != nil {
			klog.ErrorS(err, "failed to checkpoint container", "output", string(output))
		} else {
			klog.InfoS("checkpointed pod", "output", string(output))
		}

	}

	return nil
}

func checkpointPodPipelined(containers []Container, namespace string, podName string) error {
	var wg sync.WaitGroup
	for _, container := range containers {
		wg.Add(1)
		go func(containerName string) {
			defer wg.Done()
			curlPath := "https://localhost:10250/checkpoint/" + namespace + "/" + podName + "/" + containerName
			checkpointCmd := exec.Command("curl", "-sk", "-XPOST", curlPath)

			output, err := checkpointCmd.CombinedOutput()
			if err != nil {
				klog.ErrorS(err, "failed to checkpoint container", "output", string(output))
			} else {
				klog.InfoS("checkpointed pod", "output", string(output))
			}
		}(container.Name)
	}
	wg.Wait()
	return nil
}

type Container struct {
	ID   string
	Name string
}

func PrintContainerIDs(clientset *kubernetes.Clientset, namespace string) ([]Container, error) {
	pods, err := clientset.CoreV1().Pods(namespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods: %v", err)
	}

	// Create a slice of Container structs
	var containers []Container

	// Append the container ID and name for each container in each pod
	for _, pod := range pods.Items {
		for _, containerStatus := range pod.Status.ContainerStatuses {
			idParts := strings.Split(containerStatus.ContainerID, "//")
			if len(idParts) < 2 {
				return nil, fmt.Errorf("invalid container ID format: %v", containerStatus.ContainerID)
			}
			containerID := idParts[1]

			container := Container{
				ID:   containerID,
				Name: containerStatus.Name,
			}
			containers = append(containers, container)
		}
	}

	return containers, nil
}

func (r *LiveMigrationReconciler) waitForContainerReady(podName string, namespace string, containerName string, clientset *kubernetes.Clientset) error {
	timeout := 60 * time.Second
	interval := 1 * time.Second
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case <-timer.C:
			return fmt.Errorf("timed out waiting for container to become ready")
		default:
			pod, err := clientset.CoreV1().Pods(namespace).Get(context.Background(), podName, metav1.GetOptions{})
			if err != nil {
				return err
			}

			for i := range pod.Status.ContainerStatuses {
				if pod.Status.ContainerStatuses[i].Name == containerName {
					if pod.Status.ContainerStatuses[i].Ready {
						return nil
					}
					break
				}
			}
		}

		time.Sleep(interval)
	}
}

func (r *LiveMigrationReconciler) terminateCheckpointedPod(ctx context.Context, podName string, clientset *kubernetes.Clientset) error {
	// get the pod by name
	klog.Infof("", "Terminating pod ", podName)

	pod, err := clientset.CoreV1().Pods("default").Get(context.Background(), podName, metav1.GetOptions{})
	if err != nil {
		klog.ErrorS(err, "unable to get pod ", pod.Name)
	} else {
		klog.Info("pod ", podName)
	}

	// delete the pod
	err = clientset.CoreV1().Pods("default").Delete(context.Background(), podName, metav1.DeleteOptions{})
	if err != nil {
		klog.ErrorS(err, "unable to delete pod", pod.Name)
	} else {
		klog.Info("pod deleted ", podName)
	}

	err = waitForPodDeletion(ctx, podName, "default", clientset)
	if err != nil {
		klog.ErrorS(err, "unable to finish delete pod", "pod", pod.Name)
	} else {
		klog.Info("pod deletetion completed ", podName)
	}

	klog.Infof("Pod terminated ", podName)
	return nil
}

func waitForPodDeletion(ctx context.Context, podName string, namespace string, clientset *kubernetes.Clientset) error {

	fieldSelector := fmt.Sprintf("metadata.name=%s", podName)

	opts := metav1.ListOptions{
		TypeMeta:      metav1.TypeMeta{},
		LabelSelector: "",
		FieldSelector: fieldSelector,
	}

	w, err := clientset.CoreV1().Pods(namespace).Watch(ctx, opts)
	if err != nil {
		klog.ErrorS(err, "unable to watch pod", "pod", podName)
	}
	defer w.Stop()

	// wait for the pod to be deleted
	for {
		event, ok := <-w.ResultChan()
		if !ok {
			// watcher closed, assume pod is deleted
			break
		}

		if event.Type == watch.Deleted {
			klog.Infof("pod %s deleted", podName)
			return nil
		}
	}

	return fmt.Errorf("pod %s not found or already deleted", podName)
}

func (r *LiveMigrationReconciler) migrateCheckpoint(ctx context.Context, files []os.DirEntry, dir string, clientset *kubernetes.Clientset) error {

	dummyIp, dummyPort := r.getDummyServiceIPAndPort(clientset, ctx)
	for _, file := range files {

		checkpointPath := filepath.Join(dir, file.Name())
		klog.Infof("checkpointPath: %s", checkpointPath)

		// change permissions of checkpoint file
		// sudo chmod +r /tmp/checkpoints/checkpoints/checkpoint-tomcat-pod_liqo-demo-tomcat-2023-04-18T09:39:13Z.tar
		chmodCmd := exec.Command("sudo", "chmod", "+rwx", checkpointPath)
		chmodOutput, err := chmodCmd.Output()
		if err != nil {
			klog.ErrorS(err, "failed to change permissions of checkpoint file", "checkpointPath", checkpointPath)
		} else {
			klog.Infof("checkpoint file permissions changed: %s", chmodOutput)
		}

		postCmd := exec.Command("curl", "-X", "POST", "-F", fmt.Sprintf("file=@%s", checkpointPath), fmt.Sprintf("http://%s:%d/upload", dummyIp, dummyPort))
		klog.Infof("post command", "cmd", postCmd.String())
		postOut, err := postCmd.CombinedOutput()
		if err != nil {
			klog.ErrorS(err, "failed to post on the service", "service", "dummy-service")
		} else {
			klog.Infof("post on the service", "service", "dummy-service", "out", string(postOut))
		}
	}

	return nil
}

func (r *LiveMigrationReconciler) migrateCheckpointParallelized(ctx context.Context, files []os.DirEntry, dir string) error {

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

	// inside the worker function for uploading files
	dummyIp, dummyPort := r.getDummyServiceIPAndPort(clientset, ctx)

	var wg sync.WaitGroup
	for _, entry := range files {
		wg.Add(1)
		go func(entry os.DirEntry) {
			defer wg.Done()
			file, err := entry.Info()
			if err != nil {
				klog.ErrorS(err, "failed to get file info", "entry", entry.Name())
				return
			}
			checkpointPath := filepath.Join(dir, file.Name())
			klog.Infof("checkpointPath: %s", checkpointPath)

			// change permissions of checkpoint file
			// sudo chmod +r /tmp/checkpoints/checkpoints/checkpoint-tomcat-pod_liqo-demo-tomcat-2023-04-18T09:39:13Z.tar
			chmodCmd := exec.Command("sudo", "chmod", "+rwx", checkpointPath)
			_, err = chmodCmd.Output()
			if err != nil {
				klog.ErrorS(err, "failed to change permissions of checkpoint file", "checkpointPath", checkpointPath)
			} else {
				klog.Infof("checkpoint file permissions changed: %s", checkpointPath)
			}

			postCmd := exec.Command("curl", "-X", "POST", "-F", fmt.Sprintf("file=@%s", checkpointPath), fmt.Sprintf("http://%s:%d/upload", dummyIp, dummyPort))
			klog.Infof("post command", "cmd", postCmd.String())
			postOut, err := postCmd.CombinedOutput()
			if err != nil {
				klog.ErrorS(err, "failed to post on the service", "service", "dummy-service")
			} else {
				klog.Infof("post on the service", "service", "dummy-service", "out", string(postOut))
			}
		}(entry)
	}
	wg.Wait()

	// send a dummy file at the end to signal the end of the migration
	createDummyFile := exec.Command("sudo", "touch", "/tmp/checkpoints/checkpoints/dummy")
	createDummyFileOutput, err := createDummyFile.Output()
	if err != nil {
		klog.ErrorS(err, "failed to create dummy file", "output", createDummyFileOutput)
	}

	dummyPath := "/tmp/checkpoints/checkpoints/dummy"

	postCmd := exec.Command("curl", "-X", "POST", "-F", fmt.Sprintf("file=@%s", dummyPath), fmt.Sprintf("http://%s:%d/upload", dummyIp, dummyPort))
	klog.Infof("post command", "cmd", postCmd.String())
	postOut, err := postCmd.CombinedOutput()
	if err != nil {
		klog.ErrorS(err, "failed to post on the service", "service", "dummy-service")
	} else {
		klog.Infof("post on the service", "service", "dummy-service", "out", string(postOut))
	}

	return nil
}

func (r *LiveMigrationReconciler) migratePod(ctx context.Context, clientset *kubernetes.Clientset, migratingPod *api.LiveMigration) (ctrl.Result, error) {
	containers, err := PrintContainerIDs(clientset, "default")
	if err != nil {
		klog.ErrorS(err, "unable to get container IDs")
	}

	// Delete existing checkpoints folder
	pathToClear := "/tmp/checkpoints/checkpoints"
	if err := os.RemoveAll(pathToClear); err != nil {
		klog.ErrorS(err, "failed to delete existing checkpoints folder")
	}

	// Create new checkpoints folder
	if err := os.MkdirAll(pathToClear, 0777); err != nil {
		return ctrl.Result{}, err
	}

	err = checkpointPodCrio(containers, "default", migratingPod.Name)
	if err != nil {
		klog.ErrorS(err, "unable to checkpoint")
	}

	// Change owner of checkpoints folder to the current user
	if err := os.Chown(pathToClear, os.Getuid(), os.Getgid()); err != nil {
		klog.ErrorS(err, "failed to change owner of checkpoints folder")
	}

	err = r.terminateCheckpointedPod(ctx, migratingPod.Name, clientset)
	if err != nil {
		klog.ErrorS(err, "unable to terminate checkpointed pod", "pod", migratingPod.Name)
	}

	files, err := os.ReadDir(pathToClear)
	if err != nil {
		klog.ErrorS(err, "unable to read dir", "dir", pathToClear)
	}

	klog.Infof("pathToClear", "pathToClear", pathToClear)

	err = r.migrateCheckpointParallelized(ctx, files, pathToClear)
	if err != nil {
		klog.ErrorS(err, "migration failed")
	}

	return ctrl.Result{}, nil
}

func (r *LiveMigrationReconciler) migratePodPipelined(ctx context.Context, clientset *kubernetes.Clientset, migratingPod *api.LiveMigration) (ctrl.Result, error) {
	containers, err := PrintContainerIDs(clientset, "default")
	if err != nil {
		klog.ErrorS(err, "unable to get container IDs")
	}

	// Stage 1: Checkpoint pods
	checkpoints := make(chan string)
	go func() {
		defer close(checkpoints)
		pathToClear := "/tmp/checkpoints/checkpoints"
		if err := os.RemoveAll(pathToClear); err != nil {
			klog.ErrorS(err, "failed to delete existing checkpoints folder")
			return
		}
		if err := os.MkdirAll(pathToClear, 0777); err != nil {
			klog.ErrorS(err, "failed to create checkpoints folder")
			return
		}
		if err := checkpointPodPipelined(containers, "default", migratingPod.Name); err != nil {
			klog.ErrorS(err, "unable to checkpoint pod", "pod", migratingPod.Name)
			return
		}
		if err := os.Chown(pathToClear, os.Getuid(), os.Getgid()); err != nil {
			klog.ErrorS(err, "failed to change owner of checkpoints folder")
			return
		}
		checkpoints <- pathToClear

	}()

	// Stage 2: Migrate checkpoints in parallel
	migratedCheckpoints := make(chan string, len(checkpoints))
	var wg sync.WaitGroup
	for pathToClear := range checkpoints {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			files, err := os.ReadDir(path)
			if err != nil {
				klog.ErrorS(err, "unable to read dir", "dir", path)
				return
			}
			if err := r.migrateCheckpointParallelized(ctx, files, path); err != nil {
				klog.ErrorS(err, "migration failed", "dir", path)
				return
			}
			migratedCheckpoints <- path
		}(pathToClear)
	}

	go func() {
		wg.Wait()
		close(migratedCheckpoints)
	}()

	if err := r.terminateCheckpointedPod(ctx, migratingPod.Name, clientset); err != nil {
		klog.ErrorS(err, "unable to terminate checkpointed pod", "pod", migratingPod.Name)
		return ctrl.Result{}, err
	} else {
		klog.Infof("terminated pod", "pod", migratingPod.Name)
	}

	// Stage 3: Clean up checkpoints folder
	for range migratedCheckpoints {
		pathToClear := "/tmp/checkpoints/checkpoints"
		if err := os.RemoveAll(pathToClear); err != nil {
			klog.ErrorS(err, "failed to delete checkpoints folder", "dir", pathToClear)
			continue
		} else {
			klog.Infof("deleted checkpoints folder", "dir", pathToClear)
		}
	}

	return ctrl.Result{}, nil
}

func (r *LiveMigrationReconciler) CreateDummyPod(clientset *kubernetes.Clientset, ctx context.Context) error {
	_, err := clientset.CoreV1().Pods("liqo-demo").Get(ctx, "dummy-pod", metav1.GetOptions{})
	if err != nil {
		klog.ErrorS(err, "failed to get dummy pod")

		// Create the Pod
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "dummy-pod",
				Namespace: "liqo-demo",
				Labels:    map[string]string{"app": "dummy-pod"},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "dummy-container",
						Image: "docker.io/leonardopoggiani/file-handler:latest",
						Ports: []corev1.ContainerPort{
							{
								Name:          "http",
								ContainerPort: 8080,
							},
						},
						VolumeMounts: []corev1.VolumeMount{
							{
								Name:      "checkpoint-files",
								MountPath: "/mnt/data",
							},
						},
					},
				},
				Volumes: []corev1.Volume{
					{
						Name: "checkpoint-files",
						VolumeSource: corev1.VolumeSource{
							HostPath: &corev1.HostPathVolumeSource{
								Path: "/checkpoints",
							},
						},
					},
				},
			},
		}

		_, err = clientset.CoreV1().Pods("liqo-demo").Create(ctx, pod, metav1.CreateOptions{})
		if err != nil {
			klog.ErrorS(err, "failed to create pod")
			return err
		}

		err = r.waitForContainerReady(pod.Name, "liqo-demo", "dummy-container", clientset)
		if err != nil {
			klog.ErrorS(err, "failed to wait for container ready")
		} else {
			klog.Infof("Container ready")
		}

	} else {
		klog.Infof("dummy pod found")
	}

	return nil
}

func (r *LiveMigrationReconciler) CreateDummyService(clientset *kubernetes.Clientset, ctx context.Context) error {
	_, err := clientset.CoreV1().Services("liqo-demo").Get(ctx, "dummy-service", metav1.GetOptions{})
	if err != nil {
		klog.ErrorS(err, "failed to get dummy service")

		service := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "dummy-service",
				Namespace: "liqo-demo",
			},
			Spec: corev1.ServiceSpec{
				Ports: []corev1.ServicePort{
					{
						Name:       "http",
						Port:       80,
						TargetPort: intstr.FromInt(8080),
					},
				},
				Selector: map[string]string{
					"app": "dummy-pod",
				},
			},
		}

		_, err = clientset.CoreV1().Services("liqo-demo").Create(ctx, service, metav1.CreateOptions{})
		if err != nil {
			klog.ErrorS(err, "failed to create service")
			return err
		} else {
			klog.Infof("Service created", "service", service.Spec.ClusterIP)
		}
	} else {
		klog.Infof("dummy service found")
	}

	return nil
}

func (r *LiveMigrationReconciler) getDummyServiceIPAndPort(clientset *kubernetes.Clientset, ctx context.Context) (string, int32) {
	dummyService, err := clientset.CoreV1().Services("liqo-demo").Get(ctx, "dummy-service", metav1.GetOptions{})
	if err != nil {
		klog.ErrorS(err, "failed to get dummy service")
	} else {
		klog.Infof("dummy service found", "service", dummyService.Spec.ClusterIP)
	}

	return dummyService.Spec.ClusterIP, dummyService.Spec.Ports[0].Port
}

func (r *LiveMigrationReconciler) buildahRestore(ctx context.Context, path string) (*corev1.Pod, error) {
	var containersList []corev1.Container
	var podName string

	files, err := os.ReadDir(path)
	if err != nil {
		klog.ErrorS(err, "failed to read directory", "path", path)
	}

	for _, file := range files {

		if file.Name() == "dummy" {
			klog.Infof("dummy file read")
			break
		} else {
			klog.Infof("file found: %s, but that's not the last one", file.Name())
		}

		checkpointPath := filepath.Join(path, file.Name())
		klog.Infof("checkpointPath: %s", checkpointPath)

		// Change file permissions
		if err := os.Chmod(checkpointPath, 0777); err != nil {
			klog.ErrorS(err, "failed to change permissions of checkpoint file", "checkpointPath", checkpointPath)
			return nil, err
		}

		newContainerCmd := exec.Command("sudo", "buildah", "from", "scratch")
		newContainerOutput, err := newContainerCmd.Output()
		if err != nil {
			klog.ErrorS(err, "failed to create new container", "containerID", podName)
			return nil, err
		}

		newContainerOutput = bytes.TrimRight(newContainerOutput, "\n") // remove trailing newline
		newContainer := string(newContainerOutput)

		klog.Infof("", "new container name", newContainer)
		// checkpointPath := "/home/ubuntu/live-migration-operator/checkpoint/" + container.ID + ".tar"
		addCheckpointCmd := exec.Command("sudo", "buildah", "add", newContainer, checkpointPath, "/")
		klog.Infof(addCheckpointCmd.String())

		out, err := addCheckpointCmd.CombinedOutput()
		if err != nil {
			klog.ErrorS(err, "failed to add checkpoint to container")
			return nil, err
		} else {
			klog.Infof("Checkpoint added to container", string(out))
		}

		// split the string by "-"
		parts := strings.Split(checkpointPath, "-")

		// iterate over the parts in reverse order
		var containerName string

		for i := len(parts) - 1; i >= 0; i-- {
			part := parts[i]

			// check if the part is a valid date/time
			if part == "2023" {
				// the previous part is the container name
				if i > 0 {
					containerName = parts[i-1]
				} else {
					break
				}
			}
		}

		// Use regular expression to extract string between "checkpoint-" and the next "_" character
		re := regexp.MustCompile(`checkpoint-(.+?)_`)
		match := re.FindStringSubmatch(checkpointPath)
		if len(match) > 1 {
			klog.Infof("pod name:", match[1])
		} else {
			fmt.Println("No match found")
		}

		annotation := "--annotation=io.kubernetes.cri-o.annotations.checkpoint.name=" + containerName
		klog.Infof("", "annotation", annotation)

		configCheckpointCmd := exec.Command("sudo", "buildah", "config", annotation, newContainer)
		out, err = configCheckpointCmd.CombinedOutput()
		if err != nil {
			klog.ErrorS(err, "failed to add checkpoint to container")
			return nil, err
		} else {
			klog.Infof("Checkpoint added to container", string(out))
		}

		localCheckpointPath := "leonardopoggiani/checkpoint-images:" + containerName
		klog.Infof("", "localCheckpointPath", localCheckpointPath)
		commitCheckpointCmd := exec.Command("sudo", "buildah", "commit", newContainer, localCheckpointPath)
		out, err = commitCheckpointCmd.CombinedOutput()
		if err != nil {
			klog.ErrorS(err, "failed to commit checkpoint")
			klog.Infof("out: %s", out)
		}

		addContainer := corev1.Container{
			Name:  containerName,
			Image: "localhost/leonardopoggiani/checkpoint-images:" + containerName,
		}

		var mutex sync.Mutex
		mutex.Lock()
		podName = match[1]
		containersList = append(containersList, addContainer)
		mutex.Unlock()

		removeContainerCmd := exec.Command("sudo", "buildah", "rm", newContainer)
		out, err = removeContainerCmd.CombinedOutput()
		if err != nil {
			klog.ErrorS(err, "failed to remove container")
			klog.Infof("out: %s", out)
		}
	}

	klog.Infof("", "pod", podName)
	klog.Infof("", "containersList", containersList)

	// Create the Pod
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			Containers:            containersList,
			ShareProcessNamespace: &[]bool{true}[0],
		},
	}

	if err := r.Create(ctx, pod); err != nil {
		klog.ErrorS(err, "failed to create restored pod", "podName", podName)
	} else {
		klog.InfoS("restored pod", "podName", podName)
	}

	return pod, nil
}

func (r *LiveMigrationReconciler) buildahRestoreParallelized(ctx context.Context, path string) (*corev1.Pod, error) {
	var containersList []corev1.Container
	var podName string

	var wg sync.WaitGroup
	files := getFiles(path) // Get list of files to process
	// Create a channel to receive the results
	results := make(chan error, len(files))
	// Process each file in a separate goroutine
	for _, file := range files {
		wg.Add(1)
		go func(file os.DirEntry) {
			defer wg.Done()
			var tmpList []corev1.Container
			var err error

			tmpList, podName, err = processFile(file, path)
			if podName != "dummy" {
				var mutex sync.Mutex
				mutex.Lock()
				containersList = append(containersList, tmpList...)
				mutex.Unlock()
			} else {
				return
			}
			if err != nil {
				klog.ErrorS(err, "failed to process file", "file", file.Name())
			}
			results <- err
		}(file)
	}
	// Wait for all goroutines to finish
	wg.Wait()
	close(results)
	// Check if any errors occurred
	for err := range results {
		if err != nil {
			klog.ErrorS(err, "error processing files")
			return nil, err
		}
	}

	files, err := os.ReadDir(path)
	if err != nil {
		klog.ErrorS(err, "failed to read directory", "path", path)
	}

	// Create the Pod
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			Containers:            containersList,
			ShareProcessNamespace: &[]bool{true}[0],
		},
	}

	if err := r.Create(ctx, pod); err != nil {
		klog.ErrorS(err, "failed to create restored pod", "podName", podName)
	} else {
		klog.InfoS("restored pod", "podName", podName)
	}

	return pod, nil
}

func (r *LiveMigrationReconciler) buildahRestorePipelined(ctx context.Context, path string) (*corev1.Pod, error) {
	files := getFiles(path) // Get list of files to process
	podName := retrievePodName(files[0].Name())

	// Channel to send files to workers
	fileChan := make(chan os.DirEntry)

	// Start the generator
	go func() {
		defer close(fileChan)
		for _, file := range files {
			fileChan <- file
		}
	}()

	// Channel to receive the processed results
	resultChan := make(chan []corev1.Container)

	// Determine the number of workers to use based on the number of files and system resources
	numWorkers := runtime.NumCPU()
	if len(files) < numWorkers {
		numWorkers = len(files)
	}

	// Start the worker pool
	var wg sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for file := range fileChan {
				if containers, _, err := processFile(file, path); err != nil {
					klog.ErrorS(err, "failed to process file", "file", file.Name())
				} else {
					resultChan <- containers
				}
			}
		}()
	}

	// Close the result channel when all workers are done
	go func() {
		wg.Wait()
		close(resultChan)
	}()

	// Fan-in the results from workers and build the container list
	var containersList []corev1.Container
	for containers := range resultChan {
		if len(containers) > 0 {
			containersList = append(containersList, containers...)
		}
	}

	// Create the Pod
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			Containers:            containersList,
			ShareProcessNamespace: &[]bool{true}[0],
		},
	}

	if err := r.Create(ctx, pod); err != nil {
		klog.ErrorS(err, "failed to create restored pod", "podName", pod.Name)
		return nil, err
	}

	klog.InfoS("restored pod", "podName", pod.Name)
	return pod, nil
}

func getFiles(path string) []os.DirEntry {
	files, err := os.ReadDir(path)
	if err != nil {
		klog.ErrorS(err, "failed to read directory", "path", path)
	}
	return files
}

/*
// Change file permissions

	if err := os.Chmod(checkpointPath, 0777); err != nil {
		klog.ErrorS(err, "failed to change permissions of checkpoint file", "checkpointPath", checkpointPath)
		return nil, "", err
	}

// Create a new container
newContainerCmd := exec.Command("sudo", "buildah", "from", "scratch")
newContainerOutput, err := newContainerCmd.Output()

	if err != nil {
		klog.ErrorS(err, "failed to create new container", "containerID", newContainerOutput)
		return nil, "", err
	}

newContainerOutput = bytes.TrimRight(newContainerOutput, "\n") // remove trailing newline
newContainer := string(newContainerOutput)

klog.Infof("", "new container name", newContainer)

// Add the checkpoint file to the new container
addCheckpointCmd := exec.Command("sudo", "buildah", "add", newContainer, checkpointPath, "/")
klog.Infof(addCheckpointCmd.String())

out, err := addCheckpointCmd.CombinedOutput()

	if err != nil {
		klog.ErrorS(err, "failed to add checkpoint to container")
		return nil, "", err
	} else {

		klog.Infof("Checkpoint added to container", string(out))
	}
*/

func processFile(file os.DirEntry, path string) ([]corev1.Container, string, error) {
	// Check if file is a dummy file
	if file.Name() == "dummy" {
		klog.Infof("dummy file read")
		return nil, "dummy", nil
	}

	// Get the path to the checkpoint file
	checkpointPath := filepath.Join(path, file.Name())
	klog.Infof("checkpointPath: %s", checkpointPath)

	containerName := retrieveContainerName(checkpointPath)
	podName := retrievePodName(checkpointPath)

	results := make(chan string, 2)

	err := os.Chmod(checkpointPath, 0644)
	if err != nil {
		klog.ErrorS(err, "failed to change permissions of checkpoint file", "checkpointPath", checkpointPath)
		return nil, "", err
	}

	var newContainer string

	go func() {

		// Create a new container
		newContainerCmd := exec.Command("sudo", "buildah", "from", "scratch")
		newContainerOutput, err := newContainerCmd.Output()
		if err != nil {
			klog.ErrorS(err, "failed to create new container", "containerID", newContainerOutput)
			results <- "failed to create new container"
			return
		}
		newContainerOutput = bytes.TrimRight(newContainerOutput, "\n") // remove trailing newline
		newContainer = string(newContainerOutput)

		klog.Infof("", "new container name", newContainer)

		addCheckpointCmd := exec.Command("sudo", "buildah", "add", newContainer, checkpointPath, "/")
		out, err := addCheckpointCmd.CombinedOutput()
		if err != nil {
			klog.ErrorS(err, "failed to add file to container")
			results <- "failed to add file to container"
			return
		}
		// Add an annotation to the container with the checkpoint name
		annotation := "--annotation=io.kubernetes.cri-o.annotations.checkpoint.name=" + containerName
		klog.Infof("", "annotation", annotation)

		configCheckpointCmd := exec.Command("sudo", "buildah", "config", annotation, newContainer)
		out, err = configCheckpointCmd.CombinedOutput()
		if err != nil {
			klog.ErrorS(err, "failed to add checkpoint to container")
			results <- "failed to add checkpoint to container"
			return
		} else {
			klog.Infof("Checkpoint added to container", string(out))
		}

		go func() {
			localCheckpointPath := "leonardopoggiani/checkpoint-images:" + containerName
			klog.Infof("", "localCheckpointPath", localCheckpointPath)
			commitCheckpointCmd := exec.Command("sudo", "buildah", "commit", newContainer, localCheckpointPath)
			out, err = commitCheckpointCmd.CombinedOutput()
			if err != nil {
				klog.ErrorS(err, "failed to commit checkpoint")
				results <- "failed to commit checkpoint"
				return
			} else {
				klog.Infof("Checkpoint committed", string(out))
			}

			removeContainerCmd := exec.Command("sudo", "buildah", "rm", newContainer)
			out, err = removeContainerCmd.CombinedOutput()
			if err != nil {
				klog.ErrorS(err, "failed to remove container")
				klog.Infof("out: %s", out)
			}
		}()

		results <- "File added to container: " + string(out)
	}()

	addContainer := corev1.Container{
		Name:  containerName,
		Image: "localhost/leonardopoggiani/checkpoint-images:" + containerName,
	}

	var containersList []corev1.Container
	containersList = append(containersList, addContainer)

	return containersList, podName, nil
}

func retrievePodName(path string) string {
	// Use regular expression to extract string between "checkpoint-" and the next "_" character
	re := regexp.MustCompile(`checkpoint-(.+?)_`)
	match := re.FindStringSubmatch(path)
	if len(match) > 1 {
		klog.Infof("pod name:", match[1])
	}

	return match[1]
}

func retrieveContainerName(path string) string {
	// Extract the container name from the checkpoint file name
	parts := strings.Split(path, "-")

	for i := len(parts) - 1; i >= 0; i-- {
		part := parts[i]

		// check if the part is a valid date/time
		if part == "2023" {
			// the previous part is the container name
			if i > 0 {
				return parts[i-1]
			} else {
				break
			}
		}
	}

	return ""
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

func (r *LiveMigrationReconciler) pushDockerImage(localCheckpointPath string, containerName string, podName string) {
	authFile := "/run/user/1000/containers/auth.json"
	localCheckpointPath = "localhost/" + localCheckpointPath
	remoteCheckpointPath := "docker.io/leonardopoggiani/checkpoint-images:" + containerName
	pushCheckpointCmd := exec.Command("sudo", "buildah", "push", "--authfile", authFile, localCheckpointPath, remoteCheckpointPath)
	klog.Infof(pushCheckpointCmd.String())
	if err := pushCheckpointCmd.Run(); err != nil {
		klog.ErrorS(err, "failed to push checkpoint")
	} else {
		klog.Infof("", "pushed image")
	}

	klog.Infof("", "newPod", podName)
}
