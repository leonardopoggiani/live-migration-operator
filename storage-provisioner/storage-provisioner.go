package storage_provisioner

import (
	"bytes"
	"context"
	"io"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"net/http"
	"os"
	"path/filepath"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/sig-storage-lib-external-provisioner/v7/controller"
	"time"
)

const (
	port     = 12345
	buffSize = 1024
)

var (
	// PingLossThreshold is the number of lost packets after which the connection check is considered as failed.
	PingLossThreshold uint
	// PingInterval is the interval at which the ping is sent.
	PingInterval time.Duration
)

type CheckpointProvisioner struct {
	client                  client.Client
	virtualStorageClassName string
	storageNamespace        string
	localRealStorageClass   string
}

func (c CheckpointProvisioner) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	klog.Infof("Reconciling %s", request.NamespacedName)
	return reconcile.Result{}, nil
}

func (c CheckpointProvisioner) Provision(ctx context.Context, options controller.ProvisionOptions) (*corev1.PersistentVolume, controller.ProvisioningState, error) {
	// Create a new PV object based on the PVC's specifications
	klog.Infof("Provisioning a new PV for PVC %s/%s", options.PVC.Namespace, options.PVC.Name)

	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: options.PVC.Name,
		},
		Spec: corev1.PersistentVolumeSpec{
			AccessModes: options.PVC.Spec.AccessModes,
			Capacity:    options.PVC.Spec.Resources.Requests,
			ClaimRef: &corev1.ObjectReference{
				APIVersion: "v1",
				Kind:       "PersistentVolumeClaim",
				Name:       options.PVC.Name,
				Namespace:  options.PVC.Namespace,
			},
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
			StorageClassName:              options.StorageClass.GetName(),
			// MountOptions:                  options.Parameters["mountOptions"],
			VolumeMode: options.PVC.Spec.VolumeMode,
			// Set the checkpoint storage location based on the provisioner's configuration

			// NFS: &corev1.NFSVolumeSource{
			// 	Server: "liqo-gateway-service.remote-cluster.svc.cluster.local",
			//	Path:   "/mnt/checkpoints",
			//},
		},
	}

	// Create the new PV object in Kubernetes
	if err := c.client.Create(ctx, pv); err != nil {
		klog.Errorf("Failed to create PV %s: %v", pv.Name, err)
		return nil, controller.ProvisioningFinished, err
	}

	// TODO: send the checkpoint to the remote cluster
	// need to open a socket to the liqo gateway service on the remote cluster
	// remote gateway service address

	client := &http.Client{}
	// req, err := http.NewRequestWithContext(context.Background(), "POST", "http://dummy-service.liqo-demo.svc.cluster.local:80/checkpoint", nil)
	// http://flights-service.offloaded:7999/schedule
	req, err := http.NewRequestWithContext(context.Background(), "POST", "http://flights-service.offloaded:7999/schedule", nil)

	if err != nil {
		klog.ErrorS(err, "failed to create request")
	}

	// Read the files to be sent
	dir := "/tmp/checkpoints/"
	files, err := os.ReadDir(dir)
	if err != nil {
		klog.ErrorS(err, "unable to read dir", "dir", dir)
	}

	for _, file := range files {
		klog.Infof("file found: %s", file.Name())
		checkpointPath := filepath.Join(dir, file.Name())
		klog.Infof("checkpointPath: %s", checkpointPath)

		fileData, err := os.ReadFile(checkpointPath)
		if err != nil {
			klog.ErrorS(err, "failed to read file")
		}

		// Add the file data to the request body
        req.Body = io.MultiReader(req.Body, bytes.NewReader(fileData))
		// req.Body = io.NopCloser(bytes.NewReader(fileData))
		req.Header.Set("Content-Type", "application/octet-stream")

		// Send the request and handle the response
		resp, err := client.Do(req)
		if err != nil {
			klog.ErrorS(err, "failed to send request")
		}

		if resp.StatusCode != http.StatusOK {
			klog.ErrorS(nil, "failed to send file", "status", resp.StatusCode)
		}
	}

    defer resp.Body.Close()

	klog.Infof("Successfully sent file to remote cluster")

	return pv, controller.ProvisioningFinished, nil
}

func (c CheckpointProvisioner) Delete(ctx context.Context, volume *corev1.PersistentVolume) error {
	//TODO implement me
	panic("implement me")
}

func NewCheckpointProvisioner(ctx context.Context, cl client.Client, storageNamespace string) (controller.Provisioner, error) {
	// ensure that the storage namespace exists
	err := cl.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: storageNamespace,
		},
	})

	if err != nil {
		klog.ErrorS(err, "Failed to create the storage namespace", "storageNamespace", storageNamespace)
	} else {
		klog.InfoS("Storage namespace created", "storageNamespace", storageNamespace)
	}

	return &CheckpointProvisioner{
		client:                  cl,
		virtualStorageClassName: "",
		storageNamespace:        storageNamespace,
		localRealStorageClass:   "",
	}, nil
}

func SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.PersistentVolume{}).
		Complete(&CheckpointProvisioner{})
}
