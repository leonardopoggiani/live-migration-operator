package controllers

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	api "github.com/leonardopoggiani/live-migration-operator/api/v1alpha1"

	"github.com/leonardopoggiani/live-migration-operator/controllers/utils"

	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
)

func (r *LiveMigrationReconciler) MigrateCheckpoint(ctx context.Context, directory string, clientset *kubernetes.Clientset, namespace string) error {
	files, err := os.ReadDir(directory)
	if err != nil {
		klog.ErrorS(err, "failed to read dir", "dir", directory)
		return err
	} else {
		klog.Info("[INFO] files in dir: ", files)
	}

	utils.WaitForServiceReady(ctx, "dummy-service", namespace, clientset)
	dummyIp, dummyPort := utils.GetDummyServiceIPAndPort(clientset, ctx, namespace)
	klog.Info("[INFO] ", "dummyIp: ", dummyIp, ", port: ", dummyPort)

	for _, file := range files {

		checkpointPath := filepath.Join(directory, file.Name())
		klog.Info("[INFO] checkpointPath: ", checkpointPath)

		chmodCmd := exec.Command("sudo", "chmod", "+rwx", checkpointPath)
		chmodOutput, err := chmodCmd.Output()
		if err != nil {
			klog.ErrorS(err, "failed to change permissions of checkpoint file", "checkpointPath", checkpointPath)
		} else {
			klog.Infof("[INFO] checkpoint file permissions changed: %s", chmodOutput)
		}

		postCmd := exec.Command("curl", "-X", "POST", "-F", fmt.Sprintf("file=@%s", checkpointPath), fmt.Sprintf("http://%s:%d/upload", dummyIp, dummyPort))
		klog.Info("[INFO] ", "cmd", postCmd.String())
		postOut, err := postCmd.CombinedOutput()
		if err != nil {
			klog.ErrorS(err, "failed to post on the service", "service", "dummy-service")
		} else {
			klog.Infof("[INFO] dummy-service %s", string(postOut))
		}
	}

	return nil
}

func (r *LiveMigrationReconciler) MigrateCheckpointParallelized(ctx context.Context, files []os.DirEntry, dir string, clientset *kubernetes.Clientset, namespace string) error {
	// inside the worker function for uploading files
	dummyIp, dummyPort := utils.GetDummyServiceIPAndPort(clientset, ctx, namespace)

	var wg sync.WaitGroup
	for _, entry := range files {
		wg.Add(1)
		go func(entry os.DirEntry) {
			defer wg.Done()
			file, err := entry.Info()
			if err != nil {
				klog.ErrorS(err, "failed to get file info", "entry", entry.Name())
				return
			}
			checkpointPath := filepath.Join(dir, file.Name())
			klog.Infof("checkpointPath: %s", checkpointPath)

			postCmd := exec.Command("curl", "-X", "POST", "-F", fmt.Sprintf("file=@%s", checkpointPath), fmt.Sprintf("http://%s:%d/upload", dummyIp, dummyPort))
			klog.Info("[INFO] ", "cmd", postCmd.String())
			postOut, err := postCmd.CombinedOutput()
			if err != nil {
				klog.ErrorS(err, "failed to post on the service", "service", "dummy-service")
			} else {
				klog.Info("[INFO] ", "service", "dummy-service", "out", string(postOut))
			}

		}(entry)
	}

	wg.Wait()

	createDummyFile := exec.Command("sudo", "touch", "/tmp/checkpoints/checkpoints/dummy")
	createDummyFileOutput, err := createDummyFile.Output()
	if err != nil {
		klog.ErrorS(err, "failed to create dummy file", "output", createDummyFileOutput)
	}

	dummyPath := "/tmp/checkpoints/checkpoints/dummy"

	postCmd := exec.Command("curl", "-X", "POST", "-F", fmt.Sprintf("file=@%s", dummyPath), fmt.Sprintf("http://%s:%d/upload", dummyIp, dummyPort))
	klog.Info("[INFO] ", "cmd", postCmd.String())
	postOut, err := postCmd.CombinedOutput()
	if err != nil {
		klog.ErrorS(err, "failed to post on the service", "service", "dummy-service")
	} else {
		klog.Info("[INFO] ", "service", "dummy-service", "out", string(postOut))
	}

	return nil
}

func (r *LiveMigrationReconciler) MigratePod(ctx context.Context, clientset *kubernetes.Clientset, migratingPod *api.LiveMigration, namespace string) (ctrl.Result, error) {
	containers, err := utils.PrintContainerIDs(clientset, namespace)
	if err != nil {
		klog.ErrorS(err, "unable to get container IDs")
	}

	// Delete existing checkpoints folder
	pathToClear := "/tmp/checkpoints/checkpoints"
	if err := os.RemoveAll(pathToClear); err != nil {
		klog.ErrorS(err, "failed to delete existing checkpoints folder")
	}

	// Create new checkpoints folder
	if err := os.MkdirAll(pathToClear, 0777); err != nil {
		return ctrl.Result{}, err
	}

	err = r.CheckpointPodCrio(containers, namespace, migratingPod.Name)
	if err != nil {
		klog.ErrorS(err, "unable to checkpoint")
	}

	// Change owner of checkpoints folder to the current user
	if err := os.Chown(pathToClear, os.Getuid(), os.Getgid()); err != nil {
		klog.ErrorS(err, "failed to change owner of checkpoints folder")
	}

	err = r.TerminateCheckpointedPod(ctx, migratingPod.Name, clientset, namespace)
	if err != nil {
		klog.ErrorS(err, "unable to terminate checkpointed pod", "pod", migratingPod.Name)
	}

	files, err := os.ReadDir(pathToClear)
	if err != nil {
		klog.ErrorS(err, "unable to read dir", "dir", pathToClear)
	}

	klog.Info("[INFO] ", "pathToClear ", pathToClear)

	err = r.MigrateCheckpointParallelized(ctx, files, pathToClear, clientset, namespace)
	if err != nil {
		klog.ErrorS(err, "migration failed")
	}

	return ctrl.Result{}, nil
}

func (r *LiveMigrationReconciler) MigratePodPipelined(ctx context.Context, clientset *kubernetes.Clientset, migratingPod *api.LiveMigration, namespace string) (ctrl.Result, error) {
	containers, err := utils.PrintContainerIDs(clientset, namespace)
	if err != nil {
		klog.ErrorS(err, "unable to get container IDs")
	}

	// Stage 1: Checkpoint pods
	checkpoints := make(chan string)
	go func() {
		defer close(checkpoints)
		pathToClear := "/tmp/checkpoints/checkpoints"
		if err := os.RemoveAll(pathToClear); err != nil {
			klog.ErrorS(err, "failed to delete existing checkpoints folder")
			return
		}
		if err := os.MkdirAll(pathToClear, 0777); err != nil {
			klog.ErrorS(err, "failed to create checkpoints folder")
			return
		}
		if err := CheckpointPodPipelined(containers, namespace, migratingPod.Name); err != nil {
			klog.ErrorS(err, "unable to checkpoint pod", "pod", migratingPod.Name)
			return
		}
		if err := os.Chown(pathToClear, os.Getuid(), os.Getgid()); err != nil {
			klog.ErrorS(err, "failed to change owner of checkpoints folder")
			return
		}
		checkpoints <- pathToClear

	}()

	// Stage 2: Migrate checkpoints in parallel
	migratedCheckpoints := make(chan string, len(checkpoints))
	var wg sync.WaitGroup
	for pathToClear := range checkpoints {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			files, err := os.ReadDir(path)
			if err != nil {
				klog.ErrorS(err, "unable to read dir", "dir", path)
				return
			}
			if err := r.MigrateCheckpointParallelized(ctx, files, path, clientset, namespace); err != nil {
				klog.ErrorS(err, "migration failed", "dir", path)
				return
			}
			migratedCheckpoints <- path
		}(pathToClear)
	}

	go func() {
		wg.Wait()
		close(migratedCheckpoints)
	}()

	if err := r.TerminateCheckpointedPod(ctx, migratingPod.Name, clientset, namespace); err != nil {
		klog.ErrorS(err, "unable to terminate checkpointed pod", "pod", migratingPod.Name)
		return ctrl.Result{}, err
	} else {
		klog.Info("[INFO] ", "pod terminated ", migratingPod.Name)
	}

	// Stage 3: Clean up checkpoints folder
	for range migratedCheckpoints {
		pathToClear := "/tmp/checkpoints/checkpoints"
		if err := os.RemoveAll(pathToClear); err != nil {
			klog.ErrorS(err, "failed to delete checkpoints folder", "dir", pathToClear)
			continue
		} else {
			klog.Info("[INFO] ", "directory cleared ", pathToClear)
		}
	}

	return ctrl.Result{}, nil
}
