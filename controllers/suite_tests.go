package controllers

import (
	"context"
	"testing"

	api "github.com/leonardopoggiani/live-migration-operator/api/v1alpha1"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestLiveMigrationReconciler(t *testing.T) {
	// Create a fake client to mock API calls
	k8sClient := fake.NewSimpleClientset()

	// Create a fake LiveMigration object
	liveMigration := &api.LiveMigration{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-livemigration",
			Namespace: "default",
		},
		Spec: api.LiveMigrationSpec{
			SourcePod: api.PodReference{
				Name:      "test-source-pod",
				Namespace: "default",
			},
			DestinationPod: api.PodReference{
				Name:      "test-destination-pod",
				Namespace: "default",
			},
		},
	}

	// Create a fake request to simulate Reconcile() being called on an event for the object
	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      liveMigration.Name,
			Namespace: liveMigration.Namespace,
		},
	}

	// Create a LiveMigrationReconciler object
	r := &LiveMigrationReconciler{
		Client: k8sClient,
		Scheme: nil,
	}

	// Call Reconcile() with the fake request
	_, err := r.Reconcile(context.Background(), req)
	assert.NoError(t, err)

	// Check that the LiveMigration object has been created
	foundLiveMigration := &api.LiveMigration{}
	err = k8sClient.Get(context.Background(), req.NamespacedName, foundLiveMigration)
	assert.NoError(t, err)
	assert.Equal(t, liveMigration.Spec, foundLiveMigration.Spec)
}
