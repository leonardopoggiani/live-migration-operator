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
	// ctx = context.Background()
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
	}

	// Load Kubernetes config
	// kubeconfig, err := clientcmd.BuildConfigFromFlags("", "/home/fedora/.kube/config")
	// kubeconfig, err := clientcmd.BuildConfigFromFlags("", "/home/ubuntu/.kube/config")
	// if err != nil {
	//	klog.ErrorS(err, "failed to load Kubernetes config")
	//}

	// Create Kubernetes API client
	clientset, err := kubernetes.NewForConfig(kubeconfig)
	if err != nil {
		klog.ErrorS(err, "failed to create Kubernetes client")
	}

	// Load the LiveMigration resource object, if there is no Object, return directly
	var migratingPod api.LiveMigration
	klog.Infof("", "namespaced name", req.NamespacedName)
	if err := r.Get(ctx, req.NamespacedName, &migratingPod); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	klog.Infof("", "print test", migratingPod.Spec)

	var template *corev1.PodTemplateSpec
	if migratingPod.Spec.Template.ObjectMeta.Name != "" {
		template = &migratingPod.Spec.Template
	} else {
		var Err error
		template, Err = r.getSourcePodTemplate(ctx, migratingPod.Spec.SourcePod, req.Namespace)
		if Err != nil || template == nil {
			klog.ErrorS(Err, "sourcePod not exist", "pod", migratingPod.Spec.SourcePod)
			return ctrl.Result{}, Err
		}
	}

	if migratingPod.Spec.DestHost != "" {
		template.Spec.NodeSelector = map[string]string{"kubernetes.io/hostname": migratingPod.Spec.DestHost}
	}

	desiredLabels := getPodsLabelSet(template)
	klog.Infof("", "desired labels: ", desiredLabels)
	klog.Infof("", "migratingPod.Name: ", migratingPod.Name)
	klog.Infof("", "migratingPod.Spec.DestHost: ", migratingPod.Spec.DestHost)

	desiredLabels["migratingPod"] = migratingPod.Name

	annotations := getPodsAnnotationSet(&migratingPod, template)
	klog.Infof("", "annotations: ", annotations)

	// Then list all pods controlled by the LiveMigration resource object
	var childPods corev1.PodList
	if err := r.List(ctx, &childPods, client.InNamespace(req.Namespace), client.MatchingLabels(desiredLabels)); err != nil {
		klog.ErrorS(err, "unable to list child pods")
		return ctrl.Result{}, err
	}

	pod, err := r.desiredPod(migratingPod, &migratingPod, req.Namespace, template)
	klog.Infof("", "pod: ", pod.Name)
	if err != nil {
		return ctrl.Result{}, err
	}

	depl, err := r.desiredDeployment(migratingPod, &migratingPod, req.Namespace, template)
	klog.Infof("", "depl: ", depl.Name)

	if err != nil {
		return ctrl.Result{}, err
	}

	klog.Infof("", "annotations ", annotations["snapshotPath"])
	klog.Infof("", "number of existing pod ", len(childPods.Items))
	klog.Infof("", "number of desired pod ", migratingPod.Spec.Replicas)

	count, _, _ := r.getActualRunningPod(&childPods)
	klog.Infof("", "number of actual running pod ", count)

	if annotations["snapshotPolicy"] == "live-migration" && annotations["sourcePod"] != "" {
		// We are live-migrate a running pod here - Hot scale
		klog.Infof("", "live-migrate a running pod")
		// Step1: Check source pod is exist or not clean previous source pod checkpoint/rtore annotations and snapshotPath
		sourcePod, err := r.checkPodExist(ctx, annotations["sourcePod"], req.Namespace)
		if err != nil || sourcePod == nil {
			klog.ErrorS(err, "sourcePod not exist", "pod", annotations["sourcePod"])
			return ctrl.Result{}, err
		}

		klog.Infof("", "Live-migration", "Step 1 - Check source pod is exist or not - completed")
		klog.Infof("", "sourcePod status ", sourcePod.Status.Phase)

		containers, err := PrintContainerIDs(clientset, req.Namespace)
		pathToClear := "/tmp/checkpoints/checkpoints"

		err = os.Chmod(pathToClear, 0777)
		if err != nil {
			klog.ErrorS(err, "unable to change checkpoint folder permission")
		} else {
			klog.InfoS("changed checkpoint folder permission")
		}

		if _, err = exec.Command("sudo", "rm", "-rf", pathToClear).CombinedOutput(); err != nil {
			klog.ErrorS(err, "unable to delete checkpoint folder")
		} else {
			klog.InfoS("deleted checkpoint folder")
		}

		err = os.Mkdir(pathToClear, 0777)
		if err != nil {
			return ctrl.Result{}, err
		}
		err = r.checkpointPodCrio(containers, req.Namespace, migratingPod.Name)
		if err != nil {
			klog.ErrorS(err, "unable to checkpoint")
		}
	}

	klog.Infof("", "Live-migration", "Step 2 - checkpoint source Pod - completed")

	err = r.terminateCheckpointedPod(ctx, migratingPod.Name, req.Namespace, clientset)
	if err != nil {
		klog.ErrorS(err, "unable to terminate checkpointed pod", "pod", migratingPod.Name)
	} else {
		klog.Infof("checkpointed pod terminated")
	}

	dir := "/tmp/checkpoints/checkpoints"
	files, err := os.ReadDir(dir)
	if err != nil {
		klog.ErrorS(err, "unable to read dir", "dir", dir)
	}

	_, err = r.buildahCheckpointRestore(ctx, pod, depl.Name, depl.Namespace, files, dir)
	if err != nil {
		klog.ErrorS(err, "unable to restore", "pod", migratingPod.Name, "destinationHost", migratingPod.Spec.DestHost)
	} else {
		klog.Infof("pod restored")
	}

	klog.Infof("", "Live-migration", "Step 4 - Restore destPod from sourcePod's checkpointed info - completed")

	for {
		status, _ := r.checkPodExist(ctx, depl.Name, req.Namespace)
		if status != nil {
			klog.Infof("", "Live-migration", "Step 4.1 - Check whether if newPod is Running or not - completed"+status.Name+string(status.Status.Phase))
			break
		} else {
			time.Sleep(200 * time.Millisecond)
		}
	}

	klog.Infof("", "Live-migration", "Step 4.1 - Check whether if newPod is Running or not - completed")
	// Step5: Clean checkpointpod process and checkpointPath
	// if err := r.removeCheckpointPod(ctx, sourcePod, "/var/lib/kubelet/migration/", newPod.Name, req.Namespace); err != nil {
	// 	klog.ErrorS(err, "unable to remove checkpoint", "pod", sourcePod)
	// 	return ctrl.Result{}, err
	// }
	// klog.Infof("", "Live-migration", "Step 5 - Clean checkpointPod process and checkpointPath - completed")

	// // Step6: Delete source Pod
	/*
			if err := r.deletePod(ctx, sourcePod); err != nil {
				klog.ErrorS(err, "unable to delete", "source pod", sourcePod)
				return ctrl.Result{}, err
			}
			klog.Infof("", "Live-migration", "Step 6 - Delete the source pod - completed")
			return ctrl.Result{}, nil
		}
		if count == 0 && annotations["snapshotPolicy"] == "restore" {
			// We are restoring pods here - Warm scale
			_, err := os.Stat(annotations["snapshotPath"])
			if annotations["snapshotPolicy"] != "restore" && os.IsNotExist(err) {
				pod.ObjectMeta.Annotations["snapshotPolicy"] = ""
				pod.ObjectMeta.Annotations["snapshotPath"] = ""
			}
			if err := r.createMultiPod(ctx, migratingPod.Spec.Replicas, depl); err != nil {
				klog.ErrorS(err, "unable to create Pod for restore", "pod", pod)
				return ctrl.Result{}, err
			}
			klog.Infof("", "Restore", "Step 0 - Create multiple pods from checkpoint infomation - completed")
		} else if count != 0 && count != migratingPod.Spec.Replicas {
			_, err := os.Stat(annotations["snapshotPath"])
			if annotations["snapshotPolicy"] != "restore" && os.IsNotExist(err) {
				pod.ObjectMeta.Annotations["snapshotPolicy"] = ""
				pod.ObjectMeta.Annotations["snapshotPath"] = ""
			}
			if err := r.updateMultiPod(ctx, migratingPod.Spec.Replicas-count, depl); err != nil {
				klog.ErrorS(err, "unable to create Pod for restore", "pod", pod)
				return ctrl.Result{}, err
			}
			klog.Infof("", "Restore", "Step 0 - Scale multiple pods from checkpoint infomation - completed")
		} else {
			// We are checkpointing a running pod here
			if annotations["snapshotPolicy"] == "checkpoint" && annotations["sourcePod"] != "" {
				_, err := os.Stat(annotations["snapshotPath"])
				// Step1: Check source pod is exist or not
				sourcePod, err := r.checkPodExist(ctx, annotations["sourcePod"], req.Namespace)
				if err != nil || sourcePod == nil {
					klog.ErrorS(err, "sourcePod not exist", "pod", annotations["sourcePod"])
					return ctrl.Result{}, err
				}
				klog.Infof("", "Checkpoint", "Step 1 - Check the snapshotPaht is exist or not - completed")
				// Step2: Clean previous checkpoint folder if exist
				if err := r.removeCheckpointPod(ctx, sourcePod, annotations["snapshotPath"], "", req.Namespace); err != nil {
					klog.ErrorS(err, "unable to remove checkpoint", "pod", sourcePod)
					return ctrl.Result{}, err
				}
				klog.Infof("", "Checkpoint", "Step 2 - Clean previous checkpoint folder if exist - completed")
				// Step3: Checkpoint the source pod now
				if err := r.checkpointPod(ctx, sourcePod, annotations["snapshotPath"]); err != nil {
					klog.ErrorS(err, "unable to checkpoint", "pod", sourcePod)
					return ctrl.Result{}, err
				}
				klog.Infof("", "Checkpoint", "Step 3 - Checkpoint source Pod and save it - completed")
			}
		}

	*/
	return ctrl.Result{}, nil
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

func (r *LiveMigrationReconciler) checkPodExist(ctx context.Context, name, namespace string) (*corev1.Pod, error) {
	var childPods corev1.PodList
	if err := r.List(ctx, &childPods, client.InNamespace(namespace)); err != nil {
		return nil, err
	}
	if len(childPods.Items) > 0 {
		for _, pod := range childPods.Items {
			if pod.Name == name && pod.Status.Phase == "Running" {
				return &pod, nil
			}
		}

	}
	return nil, nil

	/* alternative function
	   // Watch for changes to the pod's status
	   podWatcher, err := clientset.CoreV1().Pods(req.Namespace).Watch(ctx, metav1.ListOptions{FieldSelector: "metadata.name=" + pod.Name})
	   if err != nil {
	       klog.ErrorS(err, "failed to watch pod")
	       return ctrl.Result{}, err
	   }
	   defer podWatcher.Stop()

	   // Wait for the pod to be running
	   for event := range podWatcher.ResultChan() {
	       if event.Type == watch.Modified {
	           updatedPod := event.Object.(*corev1.Pod)
	           if updatedPod.Status.Phase == corev1.PodRunning {
	               klog.InfoS("pod is running")
	               break
	           }
	       }
	   }

	*/
}

func (r *LiveMigrationReconciler) getSourcePodTemplate(ctx context.Context, sourcePodName string, namespace string) (*corev1.PodTemplateSpec, error) {
	sourcePod, err := r.checkPodExist(ctx, sourcePodName, namespace)
	if sourcePod == nil {
		return nil, err
	}

	pod := sourcePod.DeepCopy()
	container := pod.Spec.Containers[0]
	template := &corev1.PodTemplateSpec{
		ObjectMeta: pod.ObjectMeta,
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:         container.Name,
					Image:        container.Image,
					Ports:        container.Ports,
					VolumeMounts: container.VolumeMounts,
				},
			},
			Volumes: pod.Spec.Volumes,
		},
	}
	return template, nil
}

func (r *LiveMigrationReconciler) removeCheckpointPod(ctx context.Context, pod *corev1.Pod, snapshotPathCurrent, newPodName, namespace string) error {
	if newPodName != "" {
		for {
			ok, _ := r.checkPodExist(ctx, newPodName, namespace)
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
	// Get pods in liqo-demo namespace
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

func (r *LiveMigrationReconciler) terminateCheckpointedPod(ctx context.Context, podName string, namespace string, clientset *kubernetes.Clientset) error {
	// get the pod by name
	klog.Infof("", "Terminating pod ", podName)

	pod, err := clientset.CoreV1().Pods(namespace).Get(context.Background(), podName, metav1.GetOptions{})
	if err != nil {
		klog.ErrorS(err, "unable to get pod ", pod.Name)
	} else {
		klog.Info("pod ", podName)
	}

	// delete the pod
	err = clientset.CoreV1().Pods(namespace).Delete(context.Background(), podName, metav1.DeleteOptions{})
	if err != nil {
		klog.ErrorS(err, "unable to delete pod", pod.Name)
	} else {
		klog.Info("pod deleted ", podName)
	}

	err = waitForPodDeletion(ctx, podName, namespace, clientset)
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

func (r *LiveMigrationReconciler) buildahCheckpointRestore(ctx context.Context, pod *corev1.Pod, podName string, namespace string, files []os.DirEntry, dir string) (*corev1.Pod, error) {

	var containersList []corev1.Container

	for _, file := range files {
		klog.Infof("file found: %s", file.Name())
		checkpointPath := filepath.Join(dir, file.Name())
		klog.Infof("checkpointPath: %s", checkpointPath)

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

		// TODO: instead of pushing the image to the registry move the checkpoint using liqo
		lsCmd := exec.Command("ls", "-l")
		out, err = lsCmd.CombinedOutput()

		kubectlCmd := exec.Command("kubectl", "apply", "-f", "/home/fedora/live-migration-operator/config/liqo/dummy-pod.yaml")
		out, err = kubectlCmd.CombinedOutput()
		if err != nil {
			klog.ErrorS(err, "failed to create dummy pod")
			klog.Infof("out: %s", out)
		} else {
			klog.Infof("dummy pod created")
		}

		/* Create the dummy pod programatically
		// Create the PersistentVolumeClaim
			pvc := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "dummy-pvc",
					Namespace: "offloaded",
				},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{
						corev1.ReadWriteOnce,
					},
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							"storage": resource.MustParse("1Gi"),
						},
					},
					StorageClassName: "test-csi-provisioner",
				},
			}

			_, err = clientset.CoreV1().PersistentVolumeClaims("offloaded").Create(pvc)
			if err != nil {
				panic(err)
			}

			// Create the Pod
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "dummy-pod",
					Namespace: "offloaded",
				},
				Spec: corev1.PodSpec{
					NodeName: "poggianifedora-1.novalocal",
					Containers: []corev1.Container{
						{
							Name:  "dummy-container",
							Image: "nginx",
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "mypvc",
									MountPath: "/var/lib/www/html",
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "mypvc",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: "dummy-pvc",
									ReadOnly:  false,
								},
							},
						},
					},
				},
			}

			_, err = clientset.CoreV1().Pods("offloaded").Create(pod)
			if err != nil {
				panic(err)
			}

		*/
		/*
			authFile := "/run/user/1000/containers/auth.json"
			localCheckpointPath = "localhost/" + localCheckpointPath
			remoteCheckpointPath := "docker.io/leonardopoggiani/checkpoint-images:" + containerName
			pushCheckpointCmd := exec.Command("sudo", "buildah", "push", "--authfile", authFile, localCheckpointPath, remoteCheckpointPath)
			klog.Infof(pushCheckpointCmd.String())
			if err = pushCheckpointCmd.Run(); err != nil {
				klog.ErrorS(err, "failed to push checkpoint")
			} else {
				klog.Infof("", "pushed image")
			}

			klog.Infof("", "newPod", podName)

		*/

		addContainer := corev1.Container{
			Name:  containerName,
			Image: "leonardopoggiani/checkpoint-images:" + containerName,
		}

		containersList = append(containersList, addContainer)
	}

	klog.Infof("", "pod", podName)
	klog.Infof("", "containersList", containersList)

	pod.Name = podName
	pod.Namespace = namespace
	pod.Spec.Containers = containersList
	pod.Spec.ShareProcessNamespace = &[]bool{true}[0]

	pod.ObjectMeta.Annotations["snapshotPolicy"] = "restore"

	if err := r.Create(ctx, pod); err != nil {
		klog.ErrorS(err, "failed to create restored pod", "podName", podName, "namespace", namespace)
	} else {
		klog.InfoS("restored pod", "podName", podName, "namespace", namespace)
	}

	return pod, nil
}
