package controllers

import (
	"os/exec"

	"github.com/leonardopoggiani/live-migration-operator/controllers/types"
	"k8s.io/klog/v2"
)

func (r *LiveMigrationReconciler) CheckpointPodCrio(containers []types.Container, namespace string, podName string) error {
	// curl -sk -X POST "https://localhost:10250/checkpoint/liqo-demo/tomcat-pod/tomcat"

	for _, container := range containers {
		klog.Info("", "checkpointing container", "container", container.Name)

		curlPath := "https://localhost:10250/checkpoint/" + namespace + "/" + podName + "/" + container.Name
		checkpointCmd := exec.Command("curl", "-sk", "-XPOST", curlPath)

		output, err := checkpointCmd.CombinedOutput()
		if err != nil {
			klog.ErrorS(err, "failed to checkpoint container", "output", string(output))
		}

	}

	return nil
}
