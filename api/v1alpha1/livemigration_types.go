package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// LiveMigrationSpec defines the desired state of LiveMigration
type LiveMigrationSpec struct {
	// Number of desired pods. This is a pointer to distinguish between explicit
	// zero and not specified. Defaults to 1.
	// +optional
	Replicas     int    `json:"replicas,omitempty"`
	SourcePod    string `json:"sourcePod,omitempty"`
	DestHost     string `json:"destHost,omitempty"`
	SnapshotPath string `json:"snapshotPath,omitempty"`

	// Label selector for pods. Existing ReplicaSets whose pods are
	// selected by this will be the ones affected by this deployment.
	// It must match the pod template's labels.
	Selector *metav1.LabelSelector  `json:"selector"`
	Template corev1.PodTemplateSpec `json:"template,omitempty"`

	// Template describes the pods that will be created.
	// +kubebuilder:validation:Required
	Action string `json:"action"`
	// ExcludeNode indicates a node that the Pod should not get scheduled on or get migrated
	// away from.
	// +kubebuilder:validation:Optional
	// ExcludeNodeSelector map[string]string `json:"excludeNodeSelector"`
}

// LiveMigrationStatus defines the observed state of LiveMigration
type LiveMigrationStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	// State indicates the state of the MigratingPod
	State string `json:"state"`

	// CurrentRevision indicates the version of the MigratingPod to generate the current Pod
	CurrentRevision string `json:"currentRevision"`

	// ActivePod
	ActivePod string `json:"activePod"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// LiveMigration is the Schema for the livemigrations API
type LiveMigration struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   LiveMigrationSpec   `json:"spec,omitempty"`
	Status LiveMigrationStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// LiveMigrationList contains a list of LiveMigration
type LiveMigrationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []LiveMigration `json:"items"`
}

func init() {
	SchemeBuilder.Register(&LiveMigration{}, &LiveMigrationList{})
}
