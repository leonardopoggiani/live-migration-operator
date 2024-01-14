package controllers

import (
	"context"
	"os/exec"
	"sync"

	"github.com/leonardopoggiani/live-migration-operator/controllers/types"
	"github.com/leonardopoggiani/live-migration-operator/controllers/utils"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

func (r *LiveMigrationReconciler) TerminateCheckpointedPod(ctx context.Context, podName string, clientset *kubernetes.Clientset, namespace string) error {
	// get the pod by name
	klog.Info("[INFO] ", "Terminating pod ", podName)

	pod, err := clientset.CoreV1().Pods(namespace).Get(context.Background(), podName, metav1.GetOptions{})
	if err != nil {
		klog.ErrorS(err, "unable to get pod ", pod.Name)
	} else {
		klog.Info("[INFO] ", "pod ", podName)
	}

	// delete the pod
	err = clientset.CoreV1().Pods(namespace).Delete(context.Background(), podName, metav1.DeleteOptions{})
	if err != nil {
		klog.ErrorS(err, "unable to delete pod", pod.Name)
	} else {
		klog.Info("[INFO] ", "pod deleted ", podName)
	}

	err = utils.WaitForPodDeletion(ctx, podName, namespace, clientset)
	if err != nil {
		klog.ErrorS(err, "unable to finish delete pod", "pod", pod.Name)
	} else {
		klog.Info("[INFO] ", "pod deletetion completed ", podName)
	}

	klog.Info("[INFO] ", "Pod terminated ", podName)
	return nil
}

func CheckpointPodPipelined(containers []types.Container, namespace string, podName string) error {
	var wg sync.WaitGroup

	for _, container := range containers {
		klog.Infof("[INFO] Checkpointing container %s", container.Name)
		wg.Add(1)
		go func(containerName string) {
			defer wg.Done()

			curlPath := "https://localhost:10250/checkpoint/" + namespace + "/" + podName + "/" + containerName
			checkpointCmd := exec.Command("curl", "-sk", "-XPOST", curlPath)

			output, err := checkpointCmd.CombinedOutput()
			if err != nil {
				klog.ErrorS(err, "failed to checkpoint container", "output", string(output))
			}
		}(container.Name)
	}

	wg.Wait()

	return nil
}
