package performance_evaluation

import (
	"context"
	"fmt"
	"github.com/leonardopoggiani/live-migration-operator/controllers"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func getCheckpointSize(clientset *kubernetes.Clientset, numContainers int) {

	// Define the Pod manifest
	podManifest := []byte(fmt.Sprintf(
		`apiVersion: v1
		kind: Pod
		metadata:
		  name: test-pod-%d-containers
		spec:
		  containers:`,
		numContainers))

	// Add the specified number of containers to the Pod manifest
	for i := 0; i < numContainers; i++ {
		containerManifest := fmt.Sprintf(
			`- name: container-%d
			image: nginx`,
			i)
		podManifest = append(podManifest, []byte(containerManifest)...)
	}

	// Finish the Pod manifest
	podManifest = append(podManifest, []byte(`
  		restartPolicy: Never
	`)...)

	// Create the Pod
	pod, err := clientset.CoreV1().Pods("default").Create(context.TODO(), &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("test-pod-%d-containers", numContainers),
			Labels: map[string]string{
				"app": "test",
			},
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:  "nginx",
					Image: "nginx",
				},
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		panic(err.Error())
	}

	fmt.Printf("Pod %s is ready\n", pod.Name)

	LiveMigrationReconciler := controllers.LiveMigrationReconciler{}
	// Create a slice of Container structs
	var containers []controllers.Container

	// Append the container ID and name for each container in each pod
	pods, err := clientset.CoreV1().Pods("default").List(context.Background(), metav1.ListOptions{})

	for _, pod := range pods.Items {
		for _, containerStatus := range pod.Status.ContainerStatuses {
			idParts := strings.Split(containerStatus.ContainerID, "//")
			if len(idParts) < 2 {
				return
			}
			containerID := idParts[1]

			container := controllers.Container{
				ID:   containerID,
				Name: containerStatus.Name,
			}
			containers = append(containers, container)
		}
	}

	LiveMigrationReconciler.
		err = LiveMigrationReconciler.checkpointPodCrio(containers, "default", pod.Name)

	directory := "/tmp/checkpoints/checkpoints"
	var size int64 = 0
	err = filepath.Walk(directory, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		size += info.Size()
		return nil
	})
	if err != nil {
		return
	}
	fmt.Printf("The size of %s is %d bytes.\n", directory, size)

	// delete checkpoints folder
	if _, err := exec.Command("sudo", "rm", "-rf", directory+"/*").Output(); err != nil {
		return
	}

	err = LiveMigrationReconciler.checkpointPodPipelined(containers, "default", pod.Name)
	err = filepath.Walk(directory, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		size += info.Size()
		return nil
	})
	if err != nil {
		return
	}
	fmt.Printf("The size of %s is %d bytes.\n", directory, size)
}

func getTotalTime(clientset *kubernetes.Clientset, numContainers int) (time.Duration, error) {
	// Define the Pod manifest
	podManifest := []byte(fmt.Sprintf(
		`apiVersion: v1
		kind: Pod
		metadata:
		  name: test-pod-%d-containers
		spec:
		  containers:`,
		numContainers))

	// Add the specified number of containers to the Pod manifest
	for i := 0; i < numContainers; i++ {
		containerManifest := fmt.Sprintf(
			`- name: container-%d
			image: nginx`,
			i)
		podManifest = append(podManifest, []byte(containerManifest)...)
	}

	// Finish the Pod manifest
	podManifest = append(podManifest, []byte(`
  		restartPolicy: Never
	`)...)

	// Create the Pod
	pod, err := clientset.CoreV1().Pods("default").Create(context.TODO(), &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("test-pod-%d-containers", numContainers),
			Labels: map[string]string{
				"app": "test",
			},
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:  "nginx",
					Image: "nginx",
				},
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		panic(err.Error())
	}

	fmt.Printf("Pod %s is ready\n", pod.Name)

	LiveMigrationReconciler := controllers.LiveMigrationReconciler{}
	var containers []controllers.Container

	// Append the container ID and name for each container in each pod
	pods, err := clientset.CoreV1().Pods("default").List(context.Background(), metav1.ListOptions{})

	for _, pod := range pods.Items {
		for _, containerStatus := range pod.Status.ContainerStatuses {
			idParts := strings.Split(containerStatus.ContainerID, "//")
			if len(idParts) < 2 {
				return 0, nil
			}
			containerID := idParts[1]

			container := controllers.Container{
				ID:   containerID,
				Name: containerStatus.Name,
			}
			containers = append(containers, container)
		}
	}

	// Get the start time of the checkpoint
	start := time.Now()

	// Calculate the time taken for the checkpoint
	elapsed := time.Since(start)

	fmt.Printf("Checkpoint took %s\n", elapsed)
}

func getCheckpointTime(clientset *kubernetes.Clientset, numContainers int) (time.Duration, error) {
	// Define the Pod manifest
	podManifest := []byte(fmt.Sprintf(
		`apiVersion: v1
		kind: Pod
		metadata:
		  name: test-pod-%d-containers
		spec:
		  containers:`,
		numContainers))

	// Add the specified number of containers to the Pod manifest
	for i := 0; i < numContainers; i++ {
		containerManifest := fmt.Sprintf(
			`- name: container-%d
			image: nginx`,
			i)
		podManifest = append(podManifest, []byte(containerManifest)...)
	}

	// Finish the Pod manifest
	podManifest = append(podManifest, []byte(`
  		restartPolicy: Never
	`)...)

	// Create the Pod
	pod, err := clientset.CoreV1().Pods("default").Create(context.TODO(), &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("test-pod-%d-containers", numContainers),
			Labels: map[string]string{
				"app": "test",
			},
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:  "nginx",
					Image: "nginx",
				},
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		panic(err.Error())
	}

	fmt.Printf("Pod %s is ready\n", pod.Name)

	LiveMigrationReconciler := controllers.LiveMigrationReconciler{}
	var containers []controllers.Container

	// Append the container ID and name for each container in each pod
	pods, err := clientset.CoreV1().Pods("default").List(context.Background(), metav1.ListOptions{})

	for _, pod := range pods.Items {
		for _, containerStatus := range pod.Status.ContainerStatuses {
			idParts := strings.Split(containerStatus.ContainerID, "//")
			if len(idParts) < 2 {
				return 0, nil
			}
			containerID := idParts[1]

			container := controllers.Container{
				ID:   containerID,
				Name: containerStatus.Name,
			}
			containers = append(containers, container)
		}
	}

	// Get the start time of the checkpoint
	start := time.Now()

	err = LiveMigrationReconciler.checkpointPodCrio(containers, "default", pod.Name)
	if err != nil {
		return 0, err
	}

	// Calculate the time taken for the checkpoint
	elapsed := time.Since(start)
	fmt.Println("Elapsed sequential: ", elapsed)

	start = time.Now()

	err = LiveMigrationReconciler.checkpointPodPipelined(containers, "default", pod.Name)
	if err != nil {
		return 0, err
	}

	// Calculate the time taken for the checkpoint
	elapsed = time.Since(start)
	fmt.Println("Elapsed pipelined: ", elapsed)

	return 0, nil
}

func getRestoreTime(ctx context.Context) (time.Duration, error) {
	// Get the start time of the restore
	LiveMigrationReconciler := controllers.LiveMigrationReconciler{}

	start := time.Now()

	_, err := LiveMigrationReconciler.buildahRestore(ctx, "")
	if err != nil {
		return 0, err
	}
	// Calculate the time taken for the restore
	elapsed := time.Since(start)
	fmt.Println("Elapsed sequential: ", elapsed)

	start = time.Now()

	_, err = LiveMigrationReconciler.buildahRestorePipelined(ctx, "")
	if err != nil {
		return 0, err
	}
	// Calculate the time taken for the restore
	elapsed = time.Since(start)
	fmt.Println("Elapsed pipelined: ", elapsed)

	start = time.Now()

	_, err = LiveMigrationReconciler.buildahRestoreParallelized(ctx, "")
	if err != nil {
		return 0, err
	}
	// Calculate the time taken for the restore
	elapsed = time.Since(start)
	fmt.Println("Elapsed parallel: ", elapsed)

	return 0, nil
}

func getCheckpointImageRestoreSize(ctx context.Context, numContainers int) (int64, error) {
	/*
		config, err := rest.InClusterConfig()
		if err != nil {
			return 0, fmt.Errorf("failed to get in-cluster config: %v", err)
		}

		clientset, err := kubernetes.NewForConfig(config)
		if err != nil {
			return 0, fmt.Errorf("failed to create kubernetes client: %v", err)
		}

		podName := fmt.Sprintf("test-pod-%d-containers", numContainers)

		pod, err := clientset.CoreV1().Pods("default").Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			return 0, fmt.Errorf("failed to get pod: %v", err)
		}

		containerID := pod.Status.ContainerStatuses[0].ContainerID

		containerLogOptions := &v1.PodLogOptions{
			Container: pod.Spec.Containers[0].Name,
		}

		logStream, err := clientset.CoreV1().Pods("default").GetLogs(podName, containerLogOptions).Stream(ctx)
		if err != nil {
			return 0, fmt.Errorf("failed to get log stream: %v", err)
		}
		defer func(logStream io.ReadCloser) {
			err := logStream.Close()
			if err != nil {
				klog.Errorf("failed to close log stream: %v", err)
			}
		}(logStream)

		logBytes, err := io.ReadAll(logStream)
		if err != nil {
			return 0, fmt.Errorf("failed to read log stream: %v", err)
		}

		return int64(len(logBytes)), nil

	*/
	return 0, nil
}

func getTimeDirectVsTriangularized(clientset *kubernetes.Clientset, numContainers int) {
	// Define the Pod manifest
	podManifest := []byte(fmt.Sprintf(
		`apiVersion: v1
		kind: Pod
		metadata:
		  name: test-pod-%d-containers
		spec:
		  containers:`,
		numContainers))

	// Add the specified number of containers to the Pod manifest
	for i := 0; i < numContainers; i++ {
		containerManifest := fmt.Sprintf(
			`- name: container-%d
			image: nginx`,
			i)
		podManifest = append(podManifest, []byte(containerManifest)...)
	}

	// Finish the Pod manifest
	podManifest = append(podManifest, []byte(`
  		restartPolicy: Never
	`)...)

	// Create the Pod
	pod, err := clientset.CoreV1().Pods("default").Create(context.TODO(), &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("test-pod-%d-containers", numContainers),
			Labels: map[string]string{
				"app": "test",
			},
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:  "nginx",
					Image: "nginx",
				},
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		panic(err.Error())
	}

	fmt.Printf("Pod %s is ready\n", pod.Name)

	// triangularized
	LiveMigrationReconciler := controllers.LiveMigrationReconciler{}

	var containers []controllers.Container

	// Append the container ID and name for each container in each pod
	pods, err := clientset.CoreV1().Pods("default").List(context.Background(), metav1.ListOptions{})

	for _, pod := range pods.Items {
		for _, containerStatus := range pod.Status.ContainerStatuses {
			idParts := strings.Split(containerStatus.ContainerID, "//")
			if len(idParts) < 2 {
				return
			}
			containerID := idParts[1]

			container := controllers.Container{
				ID:   containerID,
				Name: containerStatus.Name,
			}
			containers = append(containers, container)
		}
	}

	err = LiveMigrationReconciler.checkpointPodCrio(containers, "default", pod.Name)
	if err != nil {
		return
	}

	// ...
}

func main() {
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
		return
	}

	// Create Kubernetes API client
	clientset, err := kubernetes.NewForConfig(kubeconfig)
	if err != nil {
		klog.ErrorS(err, "failed to create Kubernetes client")
		return
	}

	containerCounts := []int{1, 2, 3, 5, 10}

	// Loop over the container counts
	for _, numContainers := range containerCounts {
		getCheckpointSize(clientset, numContainers)
	}

}
