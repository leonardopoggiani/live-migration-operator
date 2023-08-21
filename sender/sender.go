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

func CreateContainers(ctx context.Context, numContainers int, clientset *kubernetes.Clientset, reconciler migrationoperator.LiveMigrationReconciler) *v1.Pod {
	randStr := generateRandomString()

	createContainers := generateContainers(numContainers)

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
	}

	fmt.Printf("Pod created %s", pod.Name)
	fmt.Println(createContainers[0].Name)

	err = reconciler.WaitForContainerReady(pod.Name, "default", createContainers[0].Name, clientset)
	if err != nil {
		fmt.Println(err.Error())
		return nil
	}

	fmt.Println("Container ready")
	return pod
}

func generateRandomString() string {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	return fmt.Sprintf("%d", r.Int())
}

func generateContainers(numContainers int) []v1.Container {
	var dockerImages = []string{
		"docker.io/library/tomcat:latest",
		"docker.io/library/nginx:latest",
		"docker.io/library/redis:latest",
		"docker.io/library/rabbitmq:latest",
		"docker.io/library/memcached:latest",
		"docker.io/library/mariadb:latest",
		"docker.io/library/postgres:latest",
		"docker.io/library/mongo:latest",
		"docker.io/library/wordpress:latest",
		"docker.io/library/drupal:latest",
		"docker.io/library/joomla:latest",
	}

	var createContainers []v1.Container
	for i := 0; i < numContainers; i++ {
		imageIndex := i
		if i >= len(dockerImages) {
			imageIndex = len(dockerImages) - 1
		}
		container := v1.Container{
			Name:            fmt.Sprintf("container%d", i),
			Image:           dockerImages[imageIndex],
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
	return createContainers
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

func deletePodsStartingWithTest(ctx context.Context, clientset *kubernetes.Clientset) {
	podList, err := clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		fmt.Println("Error listing pods")
		os.Exit(1)
	}

	for _, pod := range podList.Items {
		if pod.ObjectMeta.Name[:5] == "test-" {
			err := clientset.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{})
			if err != nil {
				fmt.Println("Error deleting pod")
				os.Exit(1)
			}
			fmt.Printf("Deleted pod %s\n", pod.Name)
		}
	}
}

func getContainers(ctx context.Context, clientset *kubernetes.Clientset) []migrationoperator.Container {
	// Create a slice of Container structs
	var containers []migrationoperator.Container

	// Append the container ID and name for each container in each pod
	pods, err := clientset.CoreV1().Pods("default").List(ctx, metav1.ListOptions{})
	if err != nil {
		fmt.Println(err.Error())
		return nil
	}

	for _, pod := range pods.Items {
		for _, containerStatus := range pod.Status.ContainerStatuses {
			idParts := strings.Split(containerStatus.ContainerID, "//")

			if len(idParts) < 2 {
				fmt.Println("Malformed container ID")
				return nil
			}
			containerID := idParts[1]

			container := migrationoperator.Container{
				ID:   containerID,
				Name: containerStatus.Name,
			}
			containers = append(containers, container)
		}

		fmt.Println("pod.Name: " + pod.Name)
	}

	return containers
}

func deleteDirectory(directory string) {
	if _, err := exec.Command("sudo", "rm", "-rf", directory+"/").Output(); err != nil {
		fmt.Println(err.Error())
		os.Exit(1)
	}

	fmt.Println("Checkpoints folder deleted")
}

func createDirectory(directory string) {
	if _, err := exec.Command("sudo", "mkdir", directory).Output(); err != nil {
		fmt.Println(err.Error())
		os.Exit(1)
	}

	fmt.Println("Checkpoints folder created")
}

func createKubernetesClient() *kubernetes.Clientset {
	kubeconfigPath := os.Getenv("KUBECONFIG")
	if kubeconfigPath == "" {
		kubeconfigPath = "~/.kube/config"
	}

	kubeconfigPath = os.ExpandEnv(kubeconfigPath)
	if _, err := os.Stat(kubeconfigPath); os.IsNotExist(err) {
		fmt.Println("Kubeconfig file not found")
		os.Exit(1)
	}

	kubeconfig, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		fmt.Println("Error loading kubeconfig")
		os.Exit(1)
	}

	clientset, err := kubernetes.NewForConfig(kubeconfig)
	if err != nil {
		fmt.Println("Error creating kubernetes client")
		os.Exit(1)
	}

	return clientset
}

func main() {
	fmt.Println("Sender program, sending migration request")
	runtime.GOMAXPROCS(runtime.NumCPU())

	// Connect to the SQLite database
	db := openDatabase()
	defer db.Close()

	createTableIfNotExists(db)

	clientset := createKubernetesClient()
	ctx := context.Background()

	deletePodsAndWaitForService(ctx, clientset)

	reconciler := migrationoperator.LiveMigrationReconciler{}

	repetitions := 10
	numContainers := []int{1}

	for _, numContainer := range numContainers {
		fmt.Printf("Number of Containers: %d\n", numContainer)

		for j := 0; j < repetitions; j++ {
			fmt.Printf("Iteration %d\n", j+1)

			pod := createAndCheckpointPod(ctx, numContainer, clientset, reconciler, db)
			if pod == nil {
				fmt.Println("Error creating pod")
				return
			}

			migrateAndCleanup(ctx, pod, reconciler, clientset, db)

			time.Sleep(30 * time.Second)
		}
	}
}

func openDatabase() *sql.DB {
	db, err := sql.Open("sqlite3", "performance.db")
	if err != nil {
		fmt.Println(err.Error())
	}
	return db
}

func createTableIfNotExists(db *sql.DB) {
	_, err := db.Exec(`
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
}

func deletePodsAndWaitForService(ctx context.Context, clientset *kubernetes.Clientset) {
	deletePodsStartingWithTest(ctx, clientset)
	waitForServiceCreation(clientset, ctx)
}

func createAndCheckpointPod(ctx context.Context, numContainer int, clientset *kubernetes.Clientset, reconciler migrationoperator.LiveMigrationReconciler, db *sql.DB) *v1.Pod {
	fmt.Printf("Number of Containers: %d\n", numContainer)

	pod := CreateContainers(ctx, numContainer, clientset, reconciler)
	if pod == nil {
		fmt.Println("Error creating pod")
		return nil
	}

	containers := getContainers(ctx, clientset)
	start := time.Now()

	err := reconciler.CheckpointPodCrio(containers, "default", pod.Name)
	if err != nil {
		fmt.Println(err.Error())
		return nil
	} else {
		fmt.Println("Checkpointing completed")
	}

	elapsedCheckpoint := time.Since(start)
	fmt.Printf("[MEASURE] Checkpointing took %d\n", elapsedCheckpoint.Milliseconds())

	// Insert the time measurement into the database
	db.Exec("INSERT INTO time_measurements (mode, start_time, end_time, elapsed_time) VALUES (?, ?, ?, ?)", "sequential_checkpoint", start, time.Now(), elapsedCheckpoint.Milliseconds())
	if err != nil {
		fmt.Println("Insert failed")
		fmt.Println(err.Error())
	}

	return pod
}

func migrateAndCleanup(ctx context.Context, pod *v1.Pod, reconciler migrationoperator.LiveMigrationReconciler, clientset *kubernetes.Clientset, db *sql.DB) {
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

	fmt.Printf("[MEASURE] Migration took %d\n", elapsedMigration.Milliseconds())

	db.Exec("INSERT INTO time_measurements (mode, start_time, end_time, elapsed_time) VALUES (?, ?, ?, ?)", "proposed_migration", startMigration, time.Now(), elapsedMigration.Milliseconds())

	// delete checkpoints folder
	deleteDirectory(directory + "/")
	createDirectory("/tmp/checkpoints/checkpoints/")

	time.Sleep(30 * time.Second)
}
