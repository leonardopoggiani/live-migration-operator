package multi_container

import (
	"bytes"
	"k8s.io/klog/v2"
	"os/exec"
)

func main() {
	newContainerCmd := exec.Command("/bin/sh", "-c", "sudo buildah from scratch")
	// newContainerCmd := exec.Command("sudo", "buildah", "from", "scratch")
	newContainerOutput, err := newContainerCmd.Output()
	if err != nil {
		klog.ErrorS(err, "failed to create new container")
	}
	newContainerOutput = bytes.TrimRight(newContainerOutput, "\n") // remove trailing newline
	newContainer := string(newContainerOutput)

	klog.Infof("", "new container name", newContainer)
	klog.Infof("", "checkpoint path -> ", "/home/ubuntu/live-migration-operator/checkpoint/f63284008ee741f22570680b542ffce6a6ce82ca15fa50d64cf537037d36078a.tar")

	// err = builder.Add(builder.ContainerID, false, buildah.AddAndCopyOptions{}, "checkpoint/"+container.ID+".tar")

	klog.Infof("\nsudo buildah add ", newContainer, " /home/ubuntu/live-migration-operator/checkpoint/f63284008ee741f22570680b542ffce6a6ce82ca15fa50d64cf537037d36078a.tar /")
	//addCheckpointCmd := exec.Command("/bin/sh", "-c", "sudo buildah add "+newContainer+"/home/ubuntu/live-migration-operator/checkpoint/f63284008ee741f22570680b542ffce6a6ce82ca15fa50d64cf537037d36078a.tar /")
	addCheckpointCmd := exec.Command("sudo", "buildah", "add", newContainer, "/home/ubuntu/live-migration-operator/checkpoint/f63284008ee741f22570680b542ffce6a6ce82ca15fa50d64cf537037d36078a.tar")
	klog.Infof(addCheckpointCmd.String())

	out, err := addCheckpointCmd.CombinedOutput()
	if err != nil {
		klog.ErrorS(err, "failed to add checkpoint to container")
		klog.Infof("out: %s", out)
	} else {
		klog.Infof("", "checkpoint added to container", "web")
	}

	configCheckpointCmd := exec.Command("/bin/sh", "-c", "sudo buildah config --annotation=io.kubernetes.cri-o.annotations.checkpoint.name=web "+newContainer)
	// configCheckpointCmd := exec.Command("sudo", "buildah", "config", "--annotation=io.kubernetes.cri-o.annotations.checkpoint.name="+container.Name, newContainer)
	err = configCheckpointCmd.Run()
	if err != nil {
		klog.ErrorS(err, "failed to add checkpoint name to container")
	}

	commitCheckpointCmd := exec.Command("/bin/sh", "-c", "sudo buildah commit "+newContainer+" leonardopoggiani/checkpoint-image:f63284008ee741f22570680b542ffce6a6ce82ca15fa50d64cf537037d36078a")
	// commitCheckpointCmd := exec.Command("sudo", "buildah", "commit", newContainer, "localhost/checkpoint-image:"+container.ID)
	err = commitCheckpointCmd.Run()
	if err != nil {
		klog.ErrorS(err, "failed to commit checkpoint image")
	}

	pushCheckpointCmd := exec.Command("/bin/sh", "-c", "buildah push leonardopoggiani/checkpoint-images:f63284008ee741f22570680b542ffce6a6ce82ca15fa50d64cf537037d36078a docker.io/leonardopoggiani/checkpoint-images:f63284008ee741f22570680b542ffce6a6ce82ca15fa50d64cf537037d36078a")
	// pushCheckpointCmd := exec.Command("sudo", "buildah", "push", "localhost/checkpoint-image:"+container.ID, "leonardopoggiani/checkpoint-images:"+container.ID)
	if err = pushCheckpointCmd.Run(); err != nil {
		klog.ErrorS(err, "failed to push checkpoint image")
	} else {
		klog.Infof("", "pushed image")
	}
}
