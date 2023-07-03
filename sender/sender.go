package main

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"

	migrationoperator "github.com/leonardopoggiani/live-migration-operator/controllers"
	_ "github.com/mattn/go-sqlite3"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

func deletePodsStartingWithTest(ctx context.Context, clientset *kubernetes.Clientset) error {
	podList, err := clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}

	for _, pod := range podList.Items {
		if pod.ObjectMeta.Name[:5] == "test-" {
			err := clientset.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{})
			if err != nil {
				return err
			}
			fmt.Printf("Deleted pod %s\n", pod.Name)
		}
	}

	return nil
}

func CreateContainers(ctx context.Context, numContainers int, clientset *kubernetes.Clientset, reconciler migrationoperator.LiveMigrationReconciler) *v1.Pod {
	// Generate a random string
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	randStr := fmt.Sprintf("%d", r.Int())

	createContainers := []v1.Container{}
	fmt.Println("numContainers: " + fmt.Sprintf("%d", numContainers))
	// Add the specified number of containers to the Pod manifest
	for i := 0; i < numContainers; i++ {
		fmt.Println("Creating container: " + fmt.Sprintf("container%d", i))
		docker_image := ""

		if i == 0 {
			// Create the first container with the migration sender
			docker_image = "docker.io/library/tomcat:latest"
		} else if i == 1 {
			docker_image = "docker.io/library/nginx:latest"
		} else if i == 2 {
			docker_image = "docker.io/library/redis:latest"
		} else if i == 3 {
			docker_image = "docker.io/library/rabbitmq:latest"
		} else if i == 4 {
			docker_image = "docker.io/library/memcached:latest"
		} else if i == 5 {
			docker_image = "docker.io/library/mariadb:latest"
		} else if i == 6 {
			docker_image = "docker.io/library/postgres:latest"
		} else if i == 7 {
			docker_image = "docker.io/library/mongo:latest"
		} else if i == 8 {
			docker_image = "docker.io/library/wordpress:latest"
		} else if i == 9 {
			docker_image = "docker.io/library/drupal:latest"
		} else {
			docker_image = "docker.io/library/joomla:latest"
		}

		container := v1.Container{
			Name:            fmt.Sprintf("container%d", i),
			Image:           docker_image,
			ImagePullPolicy: v1.PullPolicy("IfNotPresent"),
			Ports: []v1.ContainerPort{
				{
					ContainerPort: 8080,
					Protocol:      v1.Protocol("TCP"),
				},
			},
		}

		createContainers = append(createContainers, container)
	}

	// Create the Pod with the random string appended to the name
	pod, err := clientset.CoreV1().Pods("default").Create(ctx, &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("pod-%d-containers-%s", numContainers, randStr),
			Labels: map[string]string{
				"app": fmt.Sprintf("pod-%d-containers-%s", numContainers, randStr),
			},
		},
		Spec: v1.PodSpec{
			Containers:            createContainers,
			ShareProcessNamespace: &[]bool{true}[0],
		},
	}, metav1.CreateOptions{})

	if err != nil {
		fmt.Println(err.Error())
		return nil
	} else {
		fmt.Printf("Pod created %s", pod.Name)
		fmt.Println(createContainers[0].Name)
	}

	err = reconciler.WaitForContainerReady(pod.Name, "default", createContainers[0].Name, clientset)
	if err != nil {
		fmt.Println(err.Error())
		return nil
	} else {
		fmt.Println("Container ready")
	}

	return pod
}

func waitForServiceCreation(clientset *kubernetes.Clientset, ctx context.Context) {
	watcher, err := clientset.CoreV1().Services("liqo-demo").Watch(ctx, metav1.ListOptions{})
	if err != nil {
		fmt.Println("Error watching services")
		return
	}
	defer watcher.Stop()

	for {
		select {
		case event, ok := <-watcher.ResultChan():
			if !ok {
				fmt.Println("Watcher channel closed")
				return
			}
			if event.Type == watch.Added {
				service, ok := event.Object.(*v1.Service)
				if !ok {
					fmt.Println("Error casting service object")
					return
				}
				if service.Name == "dummy-service" {
					fmt.Println("Service dummy-service created")
					return
				}
			}
		case <-ctx.Done():
			fmt.Println("Context done")
			return
		}
	}
}

func makeCurlPost(url string, data string) error {
	cmd := exec.Command("curl", "-X", "POST", "-d", data, url)
	err := cmd.Run()
	return err
}

func waitUntilSuccess(url string, data string, timeout time.Duration) error {
	startTime := time.Now()
	for {
		err := makeCurlPost(url, data)
		if err == nil {
			fmt.Println("POST request successful!")
			return err
		}

		if time.Since(startTime) > timeout {
			fmt.Println("Timeout reached. POST request failed.")
			return err
		}

		time.Sleep(10 * time.Second)
	}
}

func main() {
	fmt.Println("Sender program, sending migration request")
	runtime.GOMAXPROCS(runtime.NumCPU())

	// Connect to the SQLite database
	db, err := sql.Open("sqlite3", "performance.db")
	if err != nil {
		fmt.Println(err.Error())
	}
	defer db.Close()

	// Create the time_measurements table if it doesn't exist
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS time_measurements (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			mode TEXT,
			start_time TIMESTAMP,
			end_time TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			elapsed_time INTEGER
		)
	`)
	if err != nil {
		fmt.Println(err.Error())
	}

	// Load Kubernetes config
	kubeconfigPath := os.Getenv("KUBECONFIG")
	if kubeconfigPath == "" {
		kubeconfigPath = "~/.kube/config"
	}

	kubeconfigPath = os.ExpandEnv(kubeconfigPath)
	if _, err := os.Stat(kubeconfigPath); os.IsNotExist(err) {
		fmt.Println("Kubeconfig file not found")
		return
	}

	kubeconfig, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		fmt.Println("Error loading kubeconfig")
		return
	}

	// Create Kubernetes API client
	clientset, err := kubernetes.NewForConfig(kubeconfig)
	if err != nil {
		fmt.Println("Error creating kubernetes client")
		return
	}

	ctx := context.Background()

	deletePodsStartingWithTest(ctx, clientset)
	waitForServiceCreation(clientset, ctx)

	reconciler := migrationoperator.LiveMigrationReconciler{}

	// Once created the dummy pod and correctly offloaded, I can create a pod to migrate
	// numContainers := []int{1, 2, 3, 4, 5}

	repetitions := 10
	numContainers := []int{1}

	for _, numContainer := range numContainers {
		fmt.Printf("Number of Containers: %d\n", numContainer)

		for j := 0; j < repetitions; j++ {
			fmt.Printf("Iteration %d\n", j+1)

			pod := CreateContainers(ctx, numContainer, clientset, reconciler)
			if pod == nil {
				fmt.Println("Error creating pod")
				return
			}

			// Create a slice of Container structs
			var containers []migrationoperator.Container

			// Append the container ID and name for each container in each pod
			pods, err := clientset.CoreV1().Pods("default").List(ctx, metav1.ListOptions{})
			if err != nil {
				fmt.Println(err.Error())
				return
			}

			for _, pod := range pods.Items {
				for _, containerStatus := range pod.Status.ContainerStatuses {
					idParts := strings.Split(containerStatus.ContainerID, "//")

					if len(idParts) < 2 {
						fmt.Println("Malformed container ID")
						return
					}
					containerID := idParts[1]

					container := migrationoperator.Container{
						ID:   containerID,
						Name: containerStatus.Name,
					}
					containers = append(containers, container)
				}
			}

			fmt.Println("pod.Name: " + pod.Name)

			start := time.Now()

			err = reconciler.CheckpointPodCrio(containers, "default", pod.Name)
			if err != nil {
				fmt.Println(err.Error())
				return
			} else {
				fmt.Println("Checkpointing completed")
			}

			elapsedCheckpoint := time.Since(start)
			fmt.Printf("[MEASURE] Checkpointing took %d\n", elapsedCheckpoint.Milliseconds())

			// Insert the time measurement into the database
			_, err = db.Exec("INSERT INTO time_measurements (mode, start_time, end_time, elapsed_time) VALUES (?, ?, ?, ?)", "sequential_checkpoint", start, time.Now(), elapsedCheckpoint.Milliseconds())
			if err != nil {
				fmt.Println("Insert failed")
				fmt.Println(err.Error())
			}

			err = reconciler.TerminateCheckpointedPod(ctx, pod.Name, clientset)
			if err != nil {
				fmt.Println("Terminate checkpointed pod failed")
				fmt.Println(err.Error())
				return
			} else {
				fmt.Println("Pod terminated")
			}

			directory := "/tmp/checkpoints/checkpoints/"

			files, err := os.ReadDir(directory)
			if err != nil {
				fmt.Println("Reading directory failed")
				fmt.Printf("Error reading directory: %v\n", err)
				return
			}

			for _, file := range files {
				if file.IsDir() {
					fmt.Printf("Directory: %s\n", file.Name())
				}
			}

			startMigration := time.Now()

			err = reconciler.MigrateCheckpoint(ctx, directory, clientset)
			if err != nil {
				fmt.Println("Migration failed")
				fmt.Println(err.Error())
				return
			}

			// send a dummy file at the end to signal the end of the migration
			createDummyFile := exec.Command("sudo", "touch", "/tmp/checkpoints/checkpoints/dummy")
			_, err = createDummyFile.Output()
			if err != nil {
				fmt.Println("Dummy create failed")
				fmt.Println(err.Error())
				return
			}

			dummyPath := "/tmp/checkpoints/checkpoints/dummy"

			dummyIp, dummyPort := reconciler.GetDummyServiceIPAndPort(clientset, ctx)

			err = waitUntilSuccess(fmt.Sprintf("http://%s:%d/upload", dummyIp, dummyPort), "{}", 1000)
			if err != nil {
				fmt.Println("Timeout reached")
				fmt.Println(err.Error())
				return
			} else {
				fmt.Println("Post success")
			}

			postCmd := exec.Command("curl", "-X", "POST", "-F", fmt.Sprintf("file=@%s", dummyPath), fmt.Sprintf("http://%s:%d/upload", dummyIp, dummyPort))
			fmt.Println("post command", "cmd", postCmd.String())
			postOut, err := postCmd.CombinedOutput()
			if err != nil {
				fmt.Println("Post failed")
				fmt.Println(err.Error())
				return
			} else {
				fmt.Println("post on the service", "service", "dummy-service", "out", string(postOut))
			}

			// Insert the measured time into the database
			elapsedMigration := time.Since(startMigration)
			elapsed := time.Since(start)

			fmt.Printf("[MEASURE] Migration took %d from beginning\n", elapsed.Milliseconds())
			fmt.Printf("[MEASURE] Migration took %d\n", elapsedMigration.Milliseconds())

			_, err = db.Exec("INSERT INTO time_measurements (mode, start_time, end_time, elapsed_time) VALUES (?, ?, ?, ?)", "proposed_migration", startMigration, time.Now(), elapsedMigration.Milliseconds())
			if err != nil {
				fmt.Println("Failed to insert time measurement:", err)
			}

			// delete checkpoints folder
			if _, err := exec.Command("sudo", "rm", "-rf", directory+"/").Output(); err != nil {
				fmt.Println("Delete checkpoints failed")
				fmt.Println(err.Error())
				return
			}

			if _, err = exec.Command("sudo", "mkdir", "/tmp/checkpoints/checkpoints/").Output(); err != nil {
				fmt.Println(err.Error())
				return
			}

			time.Sleep(30 * time.Second)
		}
	}
}
