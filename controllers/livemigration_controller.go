package controllers

import (
	"bytes"
	"context"
	"fmt"
	"k8s.io/apimachinery/pkg/util/intstr"
	"log"
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
	if err := r.Get(ctx, req.NamespacedName, &migratingPod); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	var template *corev1.PodTemplateSpec
	var liveMigratingPod *corev1.Pod
	var depl *appsv1.Deployment
	var annotations map[string]string

	klog.Infof("migratingPod.Spec.SourcePod ", migratingPod.Spec.SourcePod, "migratingPod.Spec.DestHost ", migratingPod.Spec.DestHost)

	if migratingPod.Spec.Template.ObjectMeta.Name != "" {
		template = &migratingPod.Spec.Template
	} else {
		template, err = r.getSourcePodTemplate(clientset, migratingPod.Spec.SourcePod)
		if err != nil || template == nil {
			klog.ErrorS(err, "sourcePod not exist", "pod", migratingPod.Spec.SourcePod)
			// return ctrl.Result{}, Err
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

			liveMigratingPod, err = r.desiredPod(migratingPod, &migratingPod, req.Namespace, template)
			if err != nil {
				return ctrl.Result{}, err
			}

			depl, err = r.desiredDeployment(migratingPod, &migratingPod, req.Namespace, template)
			if err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	klog.Infof("", "Migrate or restore a running pod")

	for {
		klog.Infof("", "check if file exists")
		fileExists := checkFileExist("/checkpoints")

		if fileExists {
			// file exists, check if sourcePod exists
			klog.Infof("", "file exists, check if sourcePod exists")
			klog.Infof("", "annotations ", annotations["sourcePod"])
			sourcePod, err := r.checkPodExist(clientset, annotations["sourcePod"])
			if err != nil || sourcePod == nil {
				klog.ErrorS(err, "failed to get sourcePod", "pod", annotations["sourcePod"])
				klog.Infof("But file exists, so it's a restore process")

				// TODO: restore process
				err = r.createDummyPod(clientset, ctx)
				if err != nil {
					klog.ErrorS(err, "failed to create dummy pod")
				}

				// r.restore(..)
			}

			if sourcePod != nil {
				// sourcePod exists, trigger migration
				klog.Infof("", "sourcePod exists, trigger migration")
				_, err = r.migratePod(ctx, liveMigratingPod, depl, clientset, sourcePod, &migratingPod)
				if err != nil {
					klog.ErrorS(err, "failed to migrate pod", "pod", sourcePod.Name)
					return ctrl.Result{}, err
				}

				klog.InfoS("Pod migration triggered", "pod", sourcePod.Name)
				return ctrl.Result{}, nil
			}
		} else {
			klog.Infof("", "file not exists, check if sourcePod exists")
			klog.Infof("", "annotations ", annotations["sourcePod"])
			sourcePod, err := r.checkPodExist(clientset, annotations["sourcePod"])
			if sourcePod != nil {
				// sourcePod exists, trigger migration
				klog.Infof("", "sourcePod exists, trigger migration")
				_, err = r.migratePod(ctx, liveMigratingPod, depl, clientset, sourcePod, &migratingPod)
				if err != nil {
					klog.ErrorS(err, "failed to migrate pod", "pod", sourcePod.Name)
					return ctrl.Result{}, err
				}

				klog.InfoS("Pod migration triggered", "pod", sourcePod.Name)
				return ctrl.Result{}, nil
			}
		}

		klog.Infof("wait and retry..")
		time.Sleep(10 * time.Second)
	}

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

func checkFileExist(path string) bool {
	files, err := os.ReadDir(path)
	if err != nil {
		log.Fatal(err)
	}

	return len(files) > 0
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

func (r *LiveMigrationReconciler) buildahCheckpointRestore(ctx context.Context, pod *corev1.Pod, podName string, namespace string, files []os.DirEntry, dir string) (*corev1.Pod, error) {

	var containersList []corev1.Container

	for _, file := range files {
		klog.Infof("file found: %s", file.Name())
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

		// get the IP address of the Service
		err = r.createDummyService(clientset, ctx)
		if err != nil {
			klog.ErrorS(err, "failed to create dummy service")
		}

		dummyIp, dummyPort := r.getDummyServiceIPAndPort(clientset, ctx)

		// create a byte buffer and write the file content to it

		/*
			fileData, err := os.ReadFile(checkpointPath)
			if err != nil {
				klog.ErrorS(err, "failed to read checkpoint file")
			} else {
				klog.Info("checkpoint file read")
			}

			buffer := bytes.NewBuffer(fileData)

		*/
		// send a POST request with the file content as the body

		// curl -X POST -F 'file=@log_restore.txt' http://10.104.4.80:80/upload

		/*httpClient := http.Client{
			Timeout: 60 * time.Second,
		}

		req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://%s:%d/upload", serviceIP, service.Spec.Ports[0].Port), buffer)
		if err != nil {
			klog.ErrorS(err, "failed to create request")
		} else {
			klog.Info("request created")
		}

		res, err := httpClient.Do(req)
		if err != nil {
			klog.ErrorS(err, "failed to post on the service", "service", "dummy-service")
		} else {
			klog.Info("post on the service", "service", "dummy-service")
		}
		defer func(Body io.ReadCloser) {
			err := Body.Close()
			if err != nil {
				klog.ErrorS(err, "failed to close response body")
			}
		}(res.Body)

		*/

		// resp, err := http.Post(fmt.Sprintf("http://%s:%d/upload", serviceIP, service.Spec.Ports[0].Port), "application/octet-stream", buffer)

		postCmd := exec.Command("curl", "-X", "POST", "-F", fmt.Sprintf("file=@%s", checkpointPath), fmt.Sprintf("http://%s:%d/upload", dummyIp, dummyPort))
		klog.Infof("post command", "cmd", postCmd.String())
		postOut, err := postCmd.CombinedOutput()
		if err != nil {
			klog.ErrorS(err, "failed to post on the service", "service", "dummy-service")
		} else {
			klog.Infof("post on the service", "service", "dummy-service", "out", string(postOut))
		}

		/*
			defer func(Body io.ReadCloser) {
				err := Body.Close()
				if err != nil {

				}
			}(resp.Body)

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				klog.ErrorS(err, "failed to read response body")
			} else {
				klog.Info("response body read", "body", string(body))
			}

		*/

		/*
			kubectlCmd := exec.Command("kubectl", "apply", "-f", "/home/fedora/live-migration-operator/config/liqo/dummy-pod.yaml")
			out, err = kubectlCmd.CombinedOutput()
			if err != nil {
				klog.ErrorS(err, "failed to create dummy pod")
				klog.Infof("out: %s", out)
			} else {
				klog.Infof("dummy pod created")
			}
		*/

		/*
			var str string = "test-csi-provisioner"
			var ptr *string = &str
			// Create the dummy pod programatically
			// Create the PersistentVolumeClaim
			pvc := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "dummy-pvc",
					Namespace: "default",
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
					StorageClassName: ptr,
				},
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

			// Create the Service
			service := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "dummy-service",
					Namespace: "default",
				},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{
						{
							Name:       "http",
							Port:       8080,
							TargetPort: intstr.FromInt(8080),
						},
					},
					Selector: map[string]string{
						"app": "dummy-pod",
					},
				},
			}


		*/
		/*
			service := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "dummy-service",
					Namespace: "liqo-demo",
				},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{
						{
							Name:       "http",
							Port:       8080,
							TargetPort: intstr.FromInt(8080),
						},
					},
					Selector: map[string]string{
						"app": "dummy-pod",
					},
				},
			}

		*/

		/*
			_, err = clientset.CoreV1().Services("default").Create(ctx, service, metav1.CreateOptions{})
			if err != nil {
				panic(err)
			}

			_, err = clientset.CoreV1().PersistentVolumeClaims("default").Create(ctx, pvc, metav1.CreateOptions{})
			if err != nil {
				panic(err)
			}

			// Create the Pod
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "dummy-pod",
					Namespace: "default",
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

		*/

		/*
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "dummy-pod",
					Namespace: "liqo-demo",
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
		*/

		/* _, err = clientset.CoreV1().Pods("default").Create(ctx, pod, metav1.CreateOptions{})
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
			Image: "localhost/leonardopoggiani/checkpoint-images:" + containerName,
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

func (r *LiveMigrationReconciler) migratePod(ctx context.Context, pod *corev1.Pod, depl *appsv1.Deployment, clientset *kubernetes.Clientset, sourcePod *corev1.Pod, migratingPod *api.LiveMigration) (ctrl.Result, error) {
	klog.Infof("", "Live-migration", "Step 1 - Check source pod is exist or not - completed")
	klog.Infof("", "sourcePod status ", sourcePod.Status.Phase)

	containers, err := PrintContainerIDs(clientset, "default")
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

	// Get the current user's UID and GID
	uid := os.Getuid()
	gid := os.Getgid()

	// Change the ownership of the directory to the current user
	err = os.Mkdir(pathToClear, 0777)
	if err != nil {
		return ctrl.Result{}, err
	}
	err = r.checkpointPodCrio(containers, "default", migratingPod.Name)
	if err != nil {
		klog.ErrorS(err, "unable to checkpoint")
	}

	err = os.Chown(pathToClear, uid, gid)
	if err != nil {
		klog.ErrorS(err, "unable to change checkpoint folder permission")
	} else {
		klog.InfoS("changed checkpoint folder permission")
	}

	klog.Infof("", "Live-migration", "Step 2 - checkpoint source Pod - completed")

	err = r.terminateCheckpointedPod(ctx, migratingPod.Name, clientset)
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
		status, _ := r.checkPodExist(clientset, depl.Name)
		if status != nil {
			klog.Infof("", "Live-migration", "Step 4.1 - Check whether if newPod is Running or not - completed"+status.Name+string(status.Status.Phase))
			break
		} else {
			time.Sleep(200 * time.Millisecond)
		}
	}

	klog.Infof("", "Live-migration", "Step 4.1 - Check whether if newPod is Running or not - completed")
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
