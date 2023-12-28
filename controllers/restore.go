package controllers

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"

	utils "github.com/leonardopoggiani/live-migration-operator/controllers/utils"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

func (r *LiveMigrationReconciler) BuildahRestore(ctx context.Context, path string, clientset *kubernetes.Clientset, namespace string) (*corev1.Pod, error) {
	var containersList []corev1.Container
	var podName string

	files, err := os.ReadDir(path)
	if err != nil {
		klog.ErrorS(err, "failed to read directory", "path", path)
	}

	for _, file := range files {

		if file.Name() == "dummy" {
			klog.Info("dummy file read")
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

		klog.Info("", "new container name", newContainer)
		// checkpointPath := "/home/ubuntu/live-migration-operator/checkpoint/" + container.ID + ".tar"
		addCheckpointCmd := exec.Command("sudo", "buildah", "add", newContainer, checkpointPath, "/")
		klog.Info(addCheckpointCmd.String())

		out, err := addCheckpointCmd.CombinedOutput()
		if err != nil {
			klog.ErrorS(err, "failed to add checkpoint to container")
			return nil, err
		} else {
			klog.Info("Checkpoint added to container", string(out))
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
			klog.Info("pod name:", match[1])
		} else {
			fmt.Println("No match found")
		}

		annotation := "--annotation=io.kubernetes.cri-o.annotations.checkpoint.name=" + containerName
		klog.Info("", "annotation", annotation)

		configCheckpointCmd := exec.Command("sudo", "buildah", "config", annotation, newContainer)
		out, err = configCheckpointCmd.CombinedOutput()
		if err != nil {
			klog.ErrorS(err, "failed to add checkpoint to container")
			return nil, err
		} else {
			klog.Info("Checkpoint added to container", string(out))
		}

		localCheckpointPath := "leonardopoggiani/checkpoint-images:" + containerName
		klog.Info("", "localCheckpointPath", localCheckpointPath)
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

	klog.Info("", "pod", podName)
	klog.Info("", "containersList", containersList)

	// Create the Pod
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: namespace,
		},
		Spec: corev1.PodSpec{
			Containers:            containersList,
			ShareProcessNamespace: &[]bool{true}[0],
		},
	}

	if pod, err = clientset.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		klog.ErrorS(err, "failed to create restored pod", "podName", podName)
	} else {
		klog.InfoS("restored pod", "podName", podName)
	}

	return pod, nil
}

func (r *LiveMigrationReconciler) BuildahRestoreParallelized(ctx context.Context, path string, clientset *kubernetes.Clientset, namespace string) (*corev1.Pod, error) {
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

	_, err := os.ReadDir(path)
	if err != nil {
		klog.ErrorS(err, "failed to read directory", "path", path)
	}

	// Create the Pod
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: namespace,
		},
		Spec: corev1.PodSpec{
			Containers:            containersList,
			ShareProcessNamespace: &[]bool{true}[0],
		},
	}

	if pod, err = clientset.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		klog.ErrorS(err, "failed to create restored pod", "podName", podName)
	} else {
		klog.InfoS("restored pod", "podName", podName)
	}

	return pod, nil
}

func (r *LiveMigrationReconciler) BuildahRestorePipelined(ctx context.Context, path string, clientset *kubernetes.Clientset, namespace string) (*corev1.Pod, error) {
	files := getFiles(path) // Get list of files to process
	podName := utils.RetrievePodName(files[0].Name())

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
			Namespace: namespace,
		},
		Spec: corev1.PodSpec{
			Containers:            containersList,
			ShareProcessNamespace: &[]bool{true}[0],
		},
	}

	if pod, err := clientset.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
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
