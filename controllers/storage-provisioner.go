package controllers

import (
	"context"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/sig-storage-lib-external-provisioner/v7/controller"
)

type CheckpointProvisioner struct {
	client                  client.Client
	virtualStorageClassName string
	storageNamespace        string
	localRealStorageClass   string
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

	return pv, controller.ProvisioningFinished, nil
}

func (c CheckpointProvisioner) Delete(ctx context.Context, volume *corev1.PersistentVolume) error {
	//TODO implement me
	panic("implement me")
}

func NewCheckpointProvisioner(ctx context.Context, cl client.Client,
	virtualStorageClassName, storageNamespace, localRealStorageClass string) (controller.Provisioner, error) {
	// ensure that the storage namespace exists
	err := cl.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: storageNamespace,
		},
	})

	klog.Error(err)

	return &CheckpointProvisioner{
		client:                  cl,
		virtualStorageClassName: virtualStorageClassName,
		storageNamespace:        storageNamespace,
		localRealStorageClass:   localRealStorageClass,
	}, nil
}
