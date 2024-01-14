package utils

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	types "github.com/leonardopoggiani/live-migration-operator/controllers/types"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func GetActualRunningPod(childPods *corev1.PodList) (int, corev1.PodList, corev1.PodList) {
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

func CheckPodExist(clientset *kubernetes.Clientset, name string, namespace string) (*corev1.Pod, error) {
	existingPod, err := clientset.CoreV1().Pods(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		klog.ErrorS(err, "unable to fetch Pod", "pod", name)
		return nil, err
	} else {
		klog.Info("", "pod exists", "pod", existingPod.Name)
		return existingPod, nil
	}
}

func GetSourcePodTemplate(clientset *kubernetes.Clientset, sourcePodName string, namespace string) (*corev1.PodTemplateSpec, error) {
	klog.Info("", "getSourcePodTemplate", "sourcePodName", sourcePodName)
	retrievedPod, err := CheckPodExist(clientset, sourcePodName, namespace)
	if retrievedPod == nil {
		return nil, err
	} else {
		klog.Info("", "getSourcePodTemplate", "sourcePod", retrievedPod.Name)
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

func PrintContainerIDs(clientset *kubernetes.Clientset, namespace string) ([]types.Container, error) {
	pods, err := clientset.CoreV1().Pods(namespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods: %v", err)
	}

	// Create a slice of Container structs
	var containers []types.Container

	// Append the container ID and name for each container in each pod
	for _, pod := range pods.Items {
		for _, containerStatus := range pod.Status.ContainerStatuses {
			idParts := strings.Split(containerStatus.ContainerID, "//")
			if len(idParts) < 2 {
				return nil, fmt.Errorf("invalid container ID format: %v", containerStatus.ContainerID)
			}
			containerID := idParts[1]

			container := types.Container{
				ID:   containerID,
				Name: containerStatus.Name,
			}
			containers = append(containers, container)
		}
	}

	return containers, nil
}

func WaitForContainerReady(podName, namespace, containerName string, clientset *kubernetes.Clientset) error {
	timeout := 300 * time.Second
	interval := 20 * time.Second

	timeoutChan := time.After(timeout)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-timeoutChan:
			return fmt.Errorf("timed out waiting for container %s in pod %s to become ready", containerName, podName)
		case <-ticker.C:
			if err := checkContainerReady(podName, namespace, containerName, clientset); err == nil {
				return nil
			}
		}
	}
}

func checkContainerReady(podName, namespace, containerName string, clientset *kubernetes.Clientset) error {
	pod, err := clientset.CoreV1().Pods(namespace).Get(context.Background(), podName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	for _, containerStatus := range pod.Status.ContainerStatuses {
		if containerStatus.Name == containerName && containerStatus.Ready {
			return nil
		}
	}

	return fmt.Errorf("container %s in pod %s is not ready yet", containerName, podName)
}

func WaitForPodDeletion(ctx context.Context, podName string, namespace string, clientset *kubernetes.Clientset) error {

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

func RetrievePodName(path string) string {
	// Use regular expression to extract string between "checkpoint-" and the next "_" character
	re := regexp.MustCompile(`checkpoint-(.+?)_`)
	match := re.FindStringSubmatch(path)
	if len(match) > 1 {
		klog.Info("pod name:", match[1])
		return match[1]
	}

	return ""
}

func RetrieveContainerName(path string) string {
	// Extract the container name from the checkpoint file name
	parts := strings.Split(path, "-")

	for i := len(parts) - 1; i >= 0; i-- {
		part := parts[i]

		// check if the part is a valid date/time
		if part == "2024" {
			// the previous part is the container name
			if i > 0 {
				containerName := fmt.Sprintf("%s-%s-%s", parts[i-3], parts[i-2], parts[i-1])
				klog.Infof("container name: %s", containerName)
				return containerName
			} else {
				break
			}
		}
	}

	return ""
}

func PushDockerImage(localCheckpointPath string, containerName string, podName string) {
	authFile := "/run/user/1000/containers/auth.json"
	localCheckpointPath = "localhost/" + localCheckpointPath
	remoteCheckpointPath := "docker.io/leonardopoggiani/checkpoint-images:" + containerName
	pushCheckpointCmd := exec.Command("sudo", "buildah", "push", "--authfile", authFile, localCheckpointPath, remoteCheckpointPath)
	klog.Info(pushCheckpointCmd.String())
	if err := pushCheckpointCmd.Run(); err != nil {
		klog.ErrorS(err, "failed to push checkpoint")
	} else {
		klog.Info("", "pushed image")
	}

	klog.Info("", "newPod", podName)
}
