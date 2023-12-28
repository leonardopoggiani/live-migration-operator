package dummy

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	utils "github.com/leonardopoggiani/live-migration-operator/controllers/utils"
)

func CreateDummyPod(clientset *kubernetes.Clientset, ctx context.Context, namespace string) error {
	_, err := clientset.CoreV1().Pods(namespace).Get(ctx, "dummy-pod", metav1.GetOptions{})

	if err != nil {
		klog.ErrorS(err, "failed to get dummy pod")

		// Create the Pod
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "dummy-pod",
				Namespace: namespace,
				Labels:    map[string]string{"app": "dummy-pod"},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "dummy-container",
						Image: "docker.io/leonardopoggiani/file-handler:latest",
						Ports: []corev1.ContainerPort{
							{
								Name:          "http",
								ContainerPort: 8080,
							},
						},
						VolumeMounts: []corev1.VolumeMount{
							{
								Name:      "checkpoint-files",
								MountPath: "/mnt/data",
							},
						},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("100m"),  // 100 milliCPU (0.1 CPU)
								corev1.ResourceMemory: resource.MustParse("128Mi"), // 128 Mebibytes
							},
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("250m"),  // 250 milliCPU (0.25 CPU)
								corev1.ResourceMemory: resource.MustParse("256Mi"), // 256 Mebibytes
							},
						},
					},
				},
				Volumes: []corev1.Volume{
					{
						Name: "checkpoint-files",
						VolumeSource: corev1.VolumeSource{
							HostPath: &corev1.HostPathVolumeSource{
								Path: "/tmp/checkpoints/checkpoints",
							},
						},
					},
				},
			},
		}

		_, err = clientset.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{})
		if err != nil {
			klog.ErrorS(err, "failed to create pod")
			return err
		}

		err = utils.WaitForContainerReady(pod.Name, namespace, "dummy-container", clientset)
		if err != nil {
			klog.ErrorS(err, "failed to wait for container ready")
		} else {
			klog.Info("Container ready")
		}

	} else {
		klog.Info("dummy pod found")
	}

	return nil
}

func CreateDummyService(clientset *kubernetes.Clientset, ctx context.Context, namespace string) error {
	_, err := clientset.CoreV1().Services(namespace).Get(ctx, "dummy-service", metav1.GetOptions{})
	if err != nil {
		klog.ErrorS(err, "failed to get dummy service")

		service := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "dummy-service",
				Namespace: namespace,
			},
			Spec: corev1.ServiceSpec{
				Ports: []corev1.ServicePort{
					{
						Name:       "http",
						Port:       80,
						TargetPort: intstr.FromInt(8080),
					},
				},
				Selector: map[string]string{
					"app": "dummy-pod",
				},
			},
		}

		_, err = clientset.CoreV1().Services(namespace).Create(ctx, service, metav1.CreateOptions{})
		if err != nil {
			klog.ErrorS(err, "failed to create service")
			return err
		} else {
			klog.Info("Service created", "service", service.Spec.ClusterIP)
		}
	} else {
		klog.Info("dummy service found")
	}

	return nil
}

func GetDummyServiceIPAndPort(clientset *kubernetes.Clientset, ctx context.Context, namespace string) (string, int32) {
	dummyService, err := clientset.CoreV1().Services(namespace).Get(ctx, "dummy-service", metav1.GetOptions{})
	if err != nil {
		klog.ErrorS(err, "failed to get dummy service")
	} else {
		klog.Info("dummy service found", "service", dummyService.Spec.ClusterIP)
	}

	return dummyService.Spec.ClusterIP, dummyService.Spec.Ports[0].Port
}
