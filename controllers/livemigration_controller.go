package controllers

import (
	"bytes"
	"context"
	"fmt"
	"github.com/fsnotify/fsnotify"
	"k8s.io/apimachinery/pkg/util/intstr"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	api "github.com/leonardopoggiani/live-migration-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// LiveMigrationReconciler reconciles a LiveMigration object
type LiveMigrationReconciler struct {
	client.Client
	Scheme *runtime.Scheme
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
		template, err = r.getSourcePodTemplate(clientset, migratingPod.Spec.SourcePod)
		if err != nil || template == nil {
			klog.ErrorS(err, "sourcePod not exist", "pod", migratingPod.Spec.SourcePod)
			err = r.createDummyPod(clientset, ctx)
			if err != nil {
				klog.ErrorS(err, "failed to create dummy pod")
			} else {
				klog.Infof("dummy pod created")
			}

			// get the IP address of the Service
			err = r.createDummyService(clientset, ctx)
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

	klog.Infof("", "Migrate or restore a running pod")

	// Use a channel to signal when a migration has been triggered
	migrationDone := make(chan struct{})

	// Use a channel to signal when the function should stop retrying
	stop := make(chan struct{})

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

	go func() {
		for {
			select {
			case event := <-watcher.Events:
				if event.Op&fsnotify.Create == fsnotify.Create {
					klog.Infof("Event: %s", event.String())

					if strings.Contains(event.Name, "/checkpoints/") {
						klog.Infof("file created", "file", event.Name)

						restoredPod, err := r.buildahRestore(ctx, "/checkpoints/")
						if err != nil {
							klog.ErrorS(err, "failed to restore pod")
						} else {
							klog.Infof("restore result ", restoredPod.Status)
						}
					}
				}
			case err := <-watcher.Errors:
				klog.ErrorS(err, "failed to watch file")
			case <-stop:
				klog.Infof("stop watching for file")
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
			case <-stop:
				klog.Infof("stop checking for sourcePod")
				return
			default:
				sourcePod, err := r.checkPodExist(clientset, annotations["sourcePod"])
				if err != nil {
					klog.ErrorS(err, "failed to get sourcePod", "pod", annotations["sourcePod"])
					return
				}
				if sourcePod != nil {
					klog.Infof("sourcePod found", "pod", sourcePod.Name)
					// _, err = r.migratePod(ctx, clientset, &migratingPod)
					_, err = r.migratePodDirectly(ctx, clientset, &migratingPod)
					if err != nil {
						klog.ErrorS(err, "failed to migrate pod", "pod", sourcePod.Name)
						return
					}
					migrationDone <- struct{}{}
					klog.Infof("migration done")
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
		err := watcher.Close()
		if err != nil {
			return ctrl.Result{}, err
		}
		close(stop)
		return ctrl.Result{}, nil
	case <-migrationDone:
		// Stop watching for file events and checking for the sourcePod
		err := watcher.Close()
		if err != nil {
			return ctrl.Result{}, err
		}
		close(stop)
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
	klog.Infof("checkPodExist", "pod", name)

	existingPod, err := clientset.CoreV1().Pods("default").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		klog.ErrorS(err, "unable to fetch Pod", "pod", name)
		return nil, err
	} else {
		klog.Infof("", "pod exists", "pod", existingPod.Name)
		return existingPod, nil
	}
}

func (r *LiveMigrationReconciler) getSourcePodTemplate(clientset *kubernetes.Clientset, sourcePodName string) (*corev1.PodTemplateSpec, error) {
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

func (r *LiveMigrationReconciler) checkpointPodCrio(containers []Container, namespace string, podName string) error {
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

func (r *LiveMigrationReconciler) migrateCheckpoint(ctx context.Context, files []os.DirEntry, dir string) error {

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

func (r *LiveMigrationReconciler) migratePodDirectly(ctx context.Context, clientset *kubernetes.Clientset, migratingPod *api.LiveMigration) (ctrl.Result, error) {
	pipePath := "/tmp/checkpoints/pipe"

	offloadedIP, offloadedPort := r.getDummyServiceIPAndPort(clientset, ctx)
	offloadedServiceURL := fmt.Sprintf("http://%s:%d/checkpoints", offloadedIP, offloadedPort)
	podName := migratingPod.Name

	klog.Infof("migratePodDirectly", "podName", podName, "offloadedServiceURL", offloadedServiceURL)

	// create named pipe if it doesn't exist
	if _, err := os.Stat(pipePath); os.IsNotExist(err) {
		err := syscall.Mkfifo(pipePath, 0666)
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	// wait for offloaded service to become available
	for {
		if _, err := http.Get(offloadedServiceURL); err == nil {
			break
		}
		time.Sleep(1 * time.Second)
	}

	// initiate checkpoint for pod
	kubeletEndpoint := fmt.Sprintf("http://localhost:10255/pods/%s/checkpoint", podName)
	checkpointCmd := exec.Command("sh", "-c", fmt.Sprintf("cat %s | curl -X POST -T - %s", pipePath, offloadedServiceURL))
	checkpointCmd.Stdout = os.Stdout
	checkpointCmd.Stderr = os.Stderr
	if err := checkpointCmd.Start(); err != nil {
		fmt.Printf("Error starting checkpoint command: %s\n", err)
		os.Exit(1)
	}

	// redirect checkpoint data to named pipe
	kubeletCmd := exec.Command("/usr/bin/kubelet", fmt.Sprintf("--pod-checkpoint-dir=%s", pipePath), fmt.Sprintf("--checkpoint=%s", kubeletEndpoint))
	kubeletCmd.Stdout = os.Stdout
	kubeletCmd.Stderr = os.Stderr
	if err := kubeletCmd.Start(); err != nil {
		fmt.Printf("Error starting kubelet command: %s\n", err)
		os.Exit(1)
	}

	// wait for checkpoint command to finish
	if err := checkpointCmd.Wait(); err != nil {
		fmt.Printf("Error waiting for checkpoint command: %s\n", err)
		os.Exit(1)
	}

	// kill kubelet command
	if err := kubeletCmd.Process.Kill(); err != nil {
		fmt.Printf("Error killing kubelet command: %s\n", err)
	}

	return ctrl.Result{}, nil
}

func (r *LiveMigrationReconciler) migratePod(ctx context.Context, clientset *kubernetes.Clientset, migratingPod *api.LiveMigration) (ctrl.Result, error) {
	klog.Infof("", "Live-migration", "Step 1 - Check source pod is exist or not - completed")

	containers, err := PrintContainerIDs(clientset, "default")
	if err != nil {
		klog.ErrorS(err, "unable to get container IDs")
	}

	// Delete existing checkpoints folder
	pathToClear := "/tmp/checkpoints/checkpoints"
	if err := os.RemoveAll(pathToClear); err != nil {
		klog.ErrorS(err, "failed to delete existing checkpoints folder")
	} else {
		klog.InfoS("existing checkpoints folder deleted")
	}

	// Create new checkpoints folder
	if err := os.MkdirAll(pathToClear, 0777); err != nil {
		return ctrl.Result{}, err
	}

	err = r.checkpointPodCrio(containers, "default", migratingPod.Name)
	if err != nil {
		klog.ErrorS(err, "unable to checkpoint")
	}

	// Change owner of checkpoints folder to the current user
	if err := os.Chown(pathToClear, os.Getuid(), os.Getgid()); err != nil {
		klog.ErrorS(err, "failed to change owner of checkpoints folder")
	}

	klog.Infof("", "Live-migration", "Step 2 - checkpoint source Pod - completed")

	err = r.terminateCheckpointedPod(ctx, migratingPod.Name, clientset)
	if err != nil {
		klog.ErrorS(err, "unable to terminate checkpointed pod", "pod", migratingPod.Name)
	} else {
		klog.Infof("checkpointed pod terminated")
	}

	files, err := os.ReadDir(pathToClear)
	if err != nil {
		klog.ErrorS(err, "unable to read dir", "dir", pathToClear)
	}

	err = r.migrateCheckpoint(ctx, files, pathToClear)
	if err != nil {
		klog.ErrorS(err, "unable to restore", "pod", migratingPod.Name, "destinationHost", migratingPod.Spec.DestHost)
	} else {
		klog.Infof("pod restored")
	}

	return ctrl.Result{}, nil
}

func (r *LiveMigrationReconciler) createDummyPod(clientset *kubernetes.Clientset, ctx context.Context) error {
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

func (r *LiveMigrationReconciler) createDummyService(clientset *kubernetes.Clientset, ctx context.Context) error {
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
		klog.Infof("file found: %s", file.Name())
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

		podName = match[1]

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

		containersList = append(containersList, addContainer)

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
