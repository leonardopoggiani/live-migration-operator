package controllers

import (
	"context"
	"os/exec"

	"github.com/leonardopoggiani/live-migration-operator/controllers/types"
	"github.com/leonardopoggiani/live-migration-operator/controllers/utils"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

func (r *LiveMigrationReconciler) TerminateCheckpointedPod(ctx context.Context, podName string, clientset *kubernetes.Clientset) error {
	// get the pod by name
	klog.Info("", "Terminating pod ", podName)

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

	err = utils.WaitForPodDeletion(ctx, podName, "default", clientset)
	if err != nil {
		klog.ErrorS(err, "unable to finish delete pod", "pod", pod.Name)
	} else {
		klog.Info("pod deletetion completed ", podName)
	}

	klog.Info("Pod terminated ", podName)
	return nil
}

func CheckpointPodPipelined(containers []types.Container, namespace string, podName string) error {
	for _, container := range containers {
		go func(containerName string) {
			curlPath := "https://localhost:10250/checkpoint/" + namespace + "/" + podName + "/" + containerName
			checkpointCmd := exec.Command("curl", "-sk", "-XPOST", curlPath)

			output, err := checkpointCmd.CombinedOutput()
			if err != nil {
				klog.ErrorS(err, "failed to checkpoint container", "output", string(output))
			}
		}(container.Name)
	}

	return nil
}
