package controllers

import (
	"fmt"

	v1 "github.com/leonardopoggiani/live-migration-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
)

func (r *LiveMigrationReconciler) desiredPod(migratingPod v1.LiveMigration, parentObject runtime.Object, namespace string, template *corev1.PodTemplateSpec) (*corev1.Pod, error) {
	// template := &migratingPod.Spec.Template
	desiredLabels := getPodsLabelSet(template)
	desiredFinalizers := getPodsFinalizers(template)
	desiredAnnotations := getPodsAnnotationSet(&migratingPod, template)
	accessor, _ := meta.Accessor(parentObject)
	prefix := getPodsPrefix(accessor.GetName())
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:    namespace,
			Labels:       desiredLabels,
			Finalizers:   desiredFinalizers,
			Annotations:  desiredAnnotations,
			GenerateName: prefix,
		},
	}
	pod.Spec = *template.Spec.DeepCopy()
	if err := ctrl.SetControllerReference(&migratingPod, pod, r.Scheme); err != nil {
		return pod, err
	}
	return pod, nil
}

func (r *LiveMigrationReconciler) desiredDeployment(migratingPod v1.LiveMigration, parentObject runtime.Object, namespace string, template *corev1.PodTemplateSpec) (*appsv1.Deployment, error) {
	// template := &migratingPod.Spec.Template
	desiredLabels := getPodsLabelSet(template)
	desiredFinalizers := getPodsFinalizers(template)
	desiredAnnotations := getPodsAnnotationSet(&migratingPod, template)
	accessor, _ := meta.Accessor(parentObject)
	prefix := getPodsPrefix(accessor.GetName())
	podSpec := *template.Spec.DeepCopy()
	replicas := int32(migratingPod.Spec.Replicas)
	desiredLabels["migratingPod"] = migratingPod.Name
	depl := &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{APIVersion: appsv1.SchemeGroupVersion.String(), Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      migratingPod.Name,
			Namespace: migratingPod.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: desiredLabels,
			},
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Namespace:    namespace,
					Labels:       desiredLabels,
					Finalizers:   desiredFinalizers,
					Annotations:  desiredAnnotations,
					GenerateName: prefix,
				},
				Spec: podSpec,
			},
		},
	}
	if err := ctrl.SetControllerReference(&migratingPod, depl, r.Scheme); err != nil {
		return depl, err
	}
	return depl, nil
}

func getPodsLabelSet(template *corev1.PodTemplateSpec) labels.Set {
	desiredLabels := make(labels.Set)
	for k, v := range template.Labels {
		desiredLabels[k] = v
	}
	return desiredLabels
}

func getPodsFinalizers(template *corev1.PodTemplateSpec) []string {
	desiredFinalizers := make([]string, len(template.Finalizers))
	copy(desiredFinalizers, template.Finalizers)
	return desiredFinalizers
}

func getPodsAnnotationSet(migratingPod *v1.LiveMigration, template *corev1.PodTemplateSpec) labels.Set {
	// template := &migratingPod.Spec.Template
	desiredAnnotations := make(labels.Set)
	for k, v := range template.Annotations {
		desiredAnnotations[k] = v
	}

	desiredAnnotations["sourcePod"] = migratingPod.Spec.SourcePod
	desiredAnnotations["snapshotPolicy"] = migratingPod.Spec.Action
	desiredAnnotations["snapshotPath"] = migratingPod.Spec.SnapshotPath
	return desiredAnnotations
}

func getPodsPrefix(controllerName string) string {
	// use the dash (if the name isn't too long) to make the pod name a bit prettier
	prefix := fmt.Sprintf("%s-", controllerName)
	// if len(validation.ValidatePodName(prefix, true)) != 0 {
	// 	prefix = controllerName
	// }
	return prefix
}
