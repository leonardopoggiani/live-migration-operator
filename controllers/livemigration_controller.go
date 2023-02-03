/*
Copyright 2023.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"k8s.io/klog/v2"
	"math/rand"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	livemigrationliqoiov1alpha1 "github.com/leonardopoggiani/live-migration-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// LiveMigrationReconciler reconciles a LiveMigration object
type LiveMigrationReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=livemigration.liqo.io,resources=livemigrations,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=livemigration.liqo.io,resources=livemigrations/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=livemigration.liqo.io,resources=livemigrations/finalizers,verbs=update

func (r *LiveMigrationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx = context.Background()
	klog.Infof("Reconciling LiveMigration %s", req.Name)

	// your logic here
	// Load the LiveMigration resource object, if there is no Object, return directly
	var migratingPod livemigrationliqoiov1alpha1.LiveMigration
	if err := r.Get(ctx, req.NamespacedName, &migratingPod); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	klog.Infof("", "print test", migratingPod.Spec)

	var template *corev1.PodTemplateSpec
	if migratingPod.Spec.Template.ObjectMeta.Name != "" {
		template = &migratingPod.Spec.Template
	} else {
		var Err error
		template, Err = r.getSourcePodTemplate(ctx, migratingPod.Spec.SourcePod, req.Namespace)
		if Err != nil || template == nil {
			log.Error(Err, "sourcePod not exist", "pod", migratingPod.Spec.SourcePod)
			return ctrl.Result{}, Err
		}
	}

	if migratingPod.Spec.DestHost != "" {
		template.Spec.NodeSelector = map[string]string{"kubernetes.io/hostname": migratingPod.Spec.DestHost}
	}

	desiredLabels := getPodsLabelSet(template)
	desiredLabels["migratingPod"] = migratingPod.Name
	annotations := getPodsAnnotationSet(&migratingPod, template)

	// Then list all pods controlled by the LiveMigration resource object
	var childPods corev1.PodList
	if err := r.List(ctx, &childPods, client.InNamespace(req.Namespace), client.MatchingLabels(desiredLabels)); err != nil {
		log.Error(err, "unable to list child pods")
		return ctrl.Result{}, err
	}

	pod, err := r.desiredPod(migratingPod, &migratingPod, req.Namespace, template)
	if err != nil {
		return ctrl.Result{}, err
	}

	depl, err := r.desiredDeployment(migratingPod, &migratingPod, req.Namespace, template)
	if err != nil {
		return ctrl.Result{}, err
	}

	log.Info("", "annotations ", annotations["snapshotPath"])
	log.Info("", "number of existing pod ", len(childPods.Items))
	log.Info("", "desired pod ", pod)
	log.Info("", "number of desired pod ", migratingPod.Spec.Replicas)

	count, _, _ := r.getActualRunningPod(&childPods)
	log.Info("", "number of actual running pod ", count)

	if annotations["snapshotPolicy"] == "live-migration" && annotations["sourcePod"] != "" {
		// We are live-migrate a running pod here - Hot scale
		// Step1: Check source pod is exist or not clean previous source pod checkpoint/restore annotations and snapshotPath
		sourcePod, err := r.checkPodExist(ctx, annotations["sourcePod"], req.Namespace)
		if err != nil || sourcePod == nil {
			log.Error(err, "sourcePod not exist", "pod", annotations["sourcePod"])
			return ctrl.Result{}, err
		}
		if err := r.removeCheckpointPod(ctx, sourcePod, "/var/lib/kubelet/migration/kkk", "", req.Namespace); err != nil {
			log.Error(err, "unable to remove checkpoint", "pod", sourcePod)
			return ctrl.Result{}, err
		}
		log.Info("", "Live-migration", "Step 1 - Check source pod is exist or not - completed")
		log.Info("", "sourcePod ok ", sourcePod)
		log.Info("", "sourcePod status ", sourcePod.Status.Phase)
		// Step2: checkpoint sourcePod
		// copySourcePod := sourcePod.DeepCopy()
		if err := r.checkpointPod(ctx, sourcePod, ""); err != nil {
			log.Error(err, "unable to checkpoint", "pod", sourcePod)
			return ctrl.Result{}, err
		}
		log.Info("", "Live-migration", "Step 2 - checkpoint source Pod - completed")
		// TODO(TUONG): make migrate all container inside Pod
		// for container := range copySourcePod.Spec.Containers {
		// 	fmt.Println(copySourcePod.Spec.Containers[container].Name)
		// 	log.Info("", "container of pod", copySourcePod.Spec.Containers[container].Name)
		// }

		// Step3: wait until checkpoint info are created
		container := sourcePod.Spec.Containers[0].Name
		checkpointPath := path.Join("/var/lib/kubelet/migration/kkk", strings.Split(sourcePod.Name, "-")[0])
		log.Info("", "live-migration pod", container)
		for {
			_, err := os.Stat(path.Join(checkpointPath, container, "descriptors.json"))
			if os.IsNotExist(err) {
				time.Sleep(100 * time.Millisecond)
			} else {
				break
			}
		}
		log.Info("", "Live-migration", "checkpointPath"+checkpointPath)
		log.Info("", "Live-migration", "Step 3 - Wait until checkpoint info are created - completed")
		// time.Sleep(10)
		// Step4: restore destPod from sourcePod checkpoted info
		newPod, err := r.restorePod(ctx, pod, annotations["sourcePod"], checkpointPath)
		if err != nil {
			log.Error(err, "unable to restore", "pod", sourcePod)
			return ctrl.Result{}, err
		}
		log.Info("", "Live-migration", "Step 4 - Restore destPod from sourcePod's checkpointed info - completed")
		// time.Sleep(5)
		for {
			status, _ := r.checkPodExist(ctx, newPod.Name, req.Namespace)
			if status != nil {
				log.Info("", "Live-migration", "Step 4.1 - Check whether if newPod is Running or not - completed"+status.Name+string(status.Status.Phase))
				break
			} else {
				time.Sleep(200 * time.Millisecond)
			}
		}
		log.Info("", "Live-migration", "Step 4.1 - Check whether if newPod is Running or not - completed")
		// Step5: Clean checkpointpod process and checkpointPath
		// if err := r.removeCheckpointPod(ctx, sourcePod, "/var/lib/kubelet/migration/kkk", newPod.Name, req.Namespace); err != nil {
		// 	log.Error(err, "unable to remove checkpoint", "pod", sourcePod)
		// 	return ctrl.Result{}, err
		// }
		// log.Info("", "Live-migration", "Step 5 - Clean checkpointPod process and checkpointPath - completed")

		// // Step6: Delete source Pod
		if err := r.deletePod(ctx, sourcePod); err != nil {
			log.Error(err, "unable to delete", "source pod", sourcePod)
			return ctrl.Result{}, err
		}
		log.Info("", "Live-migration", "Step 6 - Delete the source pod - completed")
		return ctrl.Result{}, nil
	}
	if count == 0 && annotations["snapshotPolicy"] == "restore" {
		// We are restoring pods here - Warm scale
		_, err := os.Stat(annotations["snapshotPath"])
		if annotations["snapshotPolicy"] != "restore" && os.IsNotExist(err) {
			pod.ObjectMeta.Annotations["snapshotPolicy"] = ""
			pod.ObjectMeta.Annotations["snapshotPath"] = ""
		}
		if err := r.createMultiPod(ctx, migratingPod.Spec.Replicas, depl); err != nil {
			log.Error(err, "unable to create Pod for restore", "pod", pod)
			return ctrl.Result{}, err
		}
		log.Info("", "Restore", "Step 0 - Create multiple pods from checkpoint infomation - completed")
	} else if count != 0 && count != migratingPod.Spec.Replicas {
		_, err := os.Stat(annotations["snapshotPath"])
		if annotations["snapshotPolicy"] != "restore" && os.IsNotExist(err) {
			pod.ObjectMeta.Annotations["snapshotPolicy"] = ""
			pod.ObjectMeta.Annotations["snapshotPath"] = ""
		}
		if err := r.updateMultiPod(ctx, migratingPod.Spec.Replicas-count, depl); err != nil {
			log.Error(err, "unable to create Pod for restore", "pod", pod)
			return ctrl.Result{}, err
		}
		log.Info("", "Restore", "Step 0 - Scale multiple pods from checkpoint infomation - completed")
	} else {
		// We are checkpointing a running pod here
		if annotations["snapshotPolicy"] == "checkpoint" && annotations["sourcePod"] != "" {
			_, err := os.Stat(annotations["snapshotPath"])
			// Step1: Check source pod is exist or not
			sourcePod, err := r.checkPodExist(ctx, annotations["sourcePod"], req.Namespace)
			if err != nil || sourcePod == nil {
				log.Error(err, "sourcePod not exist", "pod", annotations["sourcePod"])
				return ctrl.Result{}, err
			}
			log.Info("", "Checkpoint", "Step 1 - Check the snapshotPaht is exist or not - completed")
			// Step2: Clean previous checkpoint folder if exist
			if err := r.removeCheckpointPod(ctx, sourcePod, annotations["snapshotPath"], "", req.Namespace); err != nil {
				log.Error(err, "unable to remove checkpoint", "pod", sourcePod)
				return ctrl.Result{}, err
			}
			log.Info("", "Checkpoint", "Step 2 - Clean previous checkpoint folder if exist - completed")
			// Step3: Checkpoint the source pod now
			if err := r.checkpointPod(ctx, sourcePod, annotations["snapshotPath"]); err != nil {
				log.Error(err, "unable to checkpoint", "pod", sourcePod)
				return ctrl.Result{}, err
			}
			log.Info("", "Checkpoint", "Step 3 - Checkpoint source Pod and save it - completed")
		}
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *LiveMigrationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&livemigrationliqoiov1alpha1.LiveMigration{}).
		Complete(r)
}

// -------------------------- //

func (r *LiveMigrationReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	ctx := context.Background()
	log := r.Log.WithValues("LiveMigration", req.NamespacedName)

	// your logic here
	// Load the LiveMigration resource object, if there is no Object, return directly
	var migratingPod livemigrationliqoiov1alpha1.LiveMigration
	if err := r.Get(ctx, req.NamespacedName, &migratingPod); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log.Info("", "print test", migratingPod.Spec)
	var template *corev1.PodTemplateSpec
	if migratingPod.Spec.Template.ObjectMeta.Name != "" {
		template = &migratingPod.Spec.Template
	} else {
		var Err error
		template, Err = r.getSourcePodTemplate(ctx, migratingPod.Spec.SourcePod, req.Namespace)
		if Err != nil || template == nil {
			log.Error(Err, "sourcePod not exist", "pod", migratingPod.Spec.SourcePod)
			return ctrl.Result{}, Err
		}
	}

	if migratingPod.Spec.DestHost != "" {
		template.Spec.NodeSelector = map[string]string{"kubernetes.io/hostname": migratingPod.Spec.DestHost}
	}

	desiredLabels := getPodsLabelSet(template)
	desiredLabels["migratingPod"] = migratingPod.Name
	annotations := getPodsAnnotationSet(&migratingPod, template)

	// Then list all pods controlled by the LiveMigration resource object
	var childPods corev1.PodList
	if err := r.List(ctx, &childPods, client.InNamespace(req.Namespace), client.MatchingLabels(desiredLabels)); err != nil {
		log.Error(err, "unable to list child pods")
		return ctrl.Result{}, err
	}

	pod, err := r.desiredPod(migratingPod, &migratingPod, req.Namespace, template)
	if err != nil {
		return ctrl.Result{}, err
	}

	depl, err := r.desiredDeployment(migratingPod, &migratingPod, req.Namespace, template)
	if err != nil {
		return ctrl.Result{}, err
	}

	log.Info("", "annotations ", annotations["snapshotPath"])
	log.Info("", "number of existing pod ", len(childPods.Items))
	log.Info("", "desired pod ", pod)
	log.Info("", "number of desired pod ", migratingPod.Spec.Replicas)

	count, _, _ := r.getActualRunningPod(&childPods)
	log.Info("", "number of actual running pod ", count)

	if annotations["snapshotPolicy"] == "live-migration" && annotations["sourcePod"] != "" {
		// We are live-migrate a running pod here - Hot scale
		// Step1: Check source pod is exist or not clean previous source pod checkpoint/restore annotations and snapshotPath
		sourcePod, err := r.checkPodExist(ctx, annotations["sourcePod"], req.Namespace)
		if err != nil || sourcePod == nil {
			log.Error(err, "sourcePod not exist", "pod", annotations["sourcePod"])
			return ctrl.Result{}, err
		}
		if err := r.removeCheckpointPod(ctx, sourcePod, "/var/lib/kubelet/migration/kkk", "", req.Namespace); err != nil {
			log.Error(err, "unable to remove checkpoint", "pod", sourcePod)
			return ctrl.Result{}, err
		}
		log.Info("", "Live-migration", "Step 1 - Check source pod is exist or not - completed")
		log.Info("", "sourcePod ok ", sourcePod)
		log.Info("", "sourcePod status ", sourcePod.Status.Phase)
		// Step2: checkpoint sourcePod
		// copySourcePod := sourcePod.DeepCopy()
		if err := r.checkpointPod(ctx, sourcePod, ""); err != nil {
			log.Error(err, "unable to checkpoint", "pod", sourcePod)
			return ctrl.Result{}, err
		}
		log.Info("", "Live-migration", "Step 2 - checkpoint source Pod - completed")
		// TODO(TUONG): make migrate all container inside Pod
		// for container := range copySourcePod.Spec.Containers {
		// 	fmt.Println(copySourcePod.Spec.Containers[container].Name)
		// 	log.Info("", "container of pod", copySourcePod.Spec.Containers[container].Name)
		// }

		// Step3: wait until checkpoint info are created
		container := sourcePod.Spec.Containers[0].Name
		checkpointPath := path.Join("/var/lib/kubelet/migration/kkk", strings.Split(sourcePod.Name, "-")[0])
		log.Info("", "live-migration pod", container)
		for {
			_, err := os.Stat(path.Join(checkpointPath, container, "descriptors.json"))
			if os.IsNotExist(err) {
				time.Sleep(100 * time.Millisecond)
			} else {
				break
			}
		}
		log.Info("", "Live-migration", "checkpointPath"+checkpointPath)
		log.Info("", "Live-migration", "Step 3 - Wait until checkpoint info are created - completed")
		// time.Sleep(10)
		// Step4: restore destPod from sourcePod checkpoted info
		newPod, err := r.restorePod(ctx, pod, annotations["sourcePod"], checkpointPath)
		if err != nil {
			log.Error(err, "unable to restore", "pod", sourcePod)
			return ctrl.Result{}, err
		}
		log.Info("", "Live-migration", "Step 4 - Restore destPod from sourcePod's checkpointed info - completed")
		// time.Sleep(5)
		for {
			status, _ := r.checkPodExist(ctx, newPod.Name, req.Namespace)
			if status != nil {
				log.Info("", "Live-migration", "Step 4.1 - Check whether if newPod is Running or not - completed"+status.Name+string(status.Status.Phase))
				break
			} else {
				time.Sleep(200 * time.Millisecond)
			}
		}
		log.Info("", "Live-migration", "Step 4.1 - Check whether if newPod is Running or not - completed")
		// Step5: Clean checkpointpod process and checkpointPath
		// if err := r.removeCheckpointPod(ctx, sourcePod, "/var/lib/kubelet/migration/kkk", newPod.Name, req.Namespace); err != nil {
		// 	log.Error(err, "unable to remove checkpoint", "pod", sourcePod)
		// 	return ctrl.Result{}, err
		// }
		// log.Info("", "Live-migration", "Step 5 - Clean checkpointPod process and checkpointPath - completed")

		// // Step6: Delete source Pod
		if err := r.deletePod(ctx, sourcePod); err != nil {
			log.Error(err, "unable to delete", "source pod", sourcePod)
			return ctrl.Result{}, err
		}
		log.Info("", "Live-migration", "Step 6 - Delete the source pod - completed")
		return ctrl.Result{}, nil
	}
	if count == 0 && annotations["snapshotPolicy"] == "restore" {
		// We are restoring pods here - Warm scale
		_, err := os.Stat(annotations["snapshotPath"])
		if annotations["snapshotPolicy"] != "restore" && os.IsNotExist(err) {
			pod.ObjectMeta.Annotations["snapshotPolicy"] = ""
			pod.ObjectMeta.Annotations["snapshotPath"] = ""
		}
		if err := r.createMultiPod(ctx, migratingPod.Spec.Replicas, depl); err != nil {
			log.Error(err, "unable to create Pod for restore", "pod", pod)
			return ctrl.Result{}, err
		}
		log.Info("", "Restore", "Step 0 - Create multiple pods from checkpoint infomation - completed")
	} else if count != 0 && count != migratingPod.Spec.Replicas {
		_, err := os.Stat(annotations["snapshotPath"])
		if annotations["snapshotPolicy"] != "restore" && os.IsNotExist(err) {
			pod.ObjectMeta.Annotations["snapshotPolicy"] = ""
			pod.ObjectMeta.Annotations["snapshotPath"] = ""
		}
		if err := r.updateMultiPod(ctx, migratingPod.Spec.Replicas-count, depl); err != nil {
			log.Error(err, "unable to create Pod for restore", "pod", pod)
			return ctrl.Result{}, err
		}
		log.Info("", "Restore", "Step 0 - Scale multiple pods from checkpoint infomation - completed")
	} else {
		// We are checkpointing a running pod here
		if annotations["snapshotPolicy"] == "checkpoint" && annotations["sourcePod"] != "" {
			_, err := os.Stat(annotations["snapshotPath"])
			// Step1: Check source pod is exist or not
			sourcePod, err := r.checkPodExist(ctx, annotations["sourcePod"], req.Namespace)
			if err != nil || sourcePod == nil {
				log.Error(err, "sourcePod not exist", "pod", annotations["sourcePod"])
				return ctrl.Result{}, err
			}
			log.Info("", "Checkpoint", "Step 1 - Check the snapshotPaht is exist or not - completed")
			// Step2: Clean previous checkpoint folder if exist
			if err := r.removeCheckpointPod(ctx, sourcePod, annotations["snapshotPath"], "", req.Namespace); err != nil {
				log.Error(err, "unable to remove checkpoint", "pod", sourcePod)
				return ctrl.Result{}, err
			}
			log.Info("", "Checkpoint", "Step 2 - Clean previous checkpoint folder if exist - completed")
			// Step3: Checkpoint the source pod now
			if err := r.checkpointPod(ctx, sourcePod, annotations["snapshotPath"]); err != nil {
				log.Error(err, "unable to checkpoint", "pod", sourcePod)
				return ctrl.Result{}, err
			}
			log.Info("", "Checkpoint", "Step 3 - Checkpoint source Pod and save it - completed")
		}
	}
	return ctrl.Result{}, nil
}

func (r *LiveMigrationReconciler) getActualRunningPod(childPods *corev1.PodList) (int, corev1.PodList, corev1.PodList) {
	// if a pod is deleted, remove it from Actual running pod list
	count := 0
	var actualRunningPod, isDeletingPod corev1.PodList
	for _, pod := range childPods.Items {
		if !pod.DeletionTimestamp.IsZero() {
			isDeletingPod.Items = append(isDeletingPod.Items, pod)
		} else {
			actualRunningPod.Items = append(actualRunningPod.Items, pod)
			count++
		}
	}
	return count, actualRunningPod, isDeletingPod
}

func (r *LiveMigrationReconciler) createMultiPod(ctx context.Context, replicas int, depl *appsv1.Deployment) error {
	if err := r.Create(ctx, depl); err != nil {
		return err
	}
	return nil
}

func (r *LiveMigrationReconciler) updateMultiPod(ctx context.Context, replicas int, depl *appsv1.Deployment) error {
	if err := r.Update(ctx, depl); err != nil {
		return err
	}
	return nil
}

func (r *LiveMigrationReconciler) deletePod(ctx context.Context, pod *corev1.Pod) error {
	if err := r.Delete(ctx, pod); err != nil {
		return err
	}
	return nil
}

func (r *LiveMigrationReconciler) checkpointPod(ctx context.Context, pod *corev1.Pod, snapshotPath string) error {
	snapshotPolicy := "checkpoint"
	if snapshotPath == "" {
		snapshotPath = "/var/lib/kubelet/migration/kkk"
	}
	if err := r.updateAnnotations(ctx, pod, snapshotPolicy, snapshotPath); err != nil {
		return err
	}
	return nil
}

func (r *LiveMigrationReconciler) restorePod(ctx context.Context, pod *corev1.Pod, sourcePod, checkpointPath string) (*corev1.Pod, error) {
	// targetPod := pod.DeepCopy()
	// targetPod.Finalizers = append(targetPod.Finalizers, migratingPodFinalizer)
	s1 := rand.NewSource(time.Now().UnixNano())
	number := rand.New(s1)
	sourcePod = strings.Split(sourcePod, "-migration-")[0]
	pod.Name = sourcePod + "-migration-" + strconv.Itoa(number.Intn(100))
	// pod.Spec.ClonePod = sourcePod
	pod.ObjectMeta.Annotations["snapshotPolicy"] = "restore"
	pod.ObjectMeta.Annotations["snapshotPath"] = checkpointPath
	if err := r.Create(ctx, pod); err != nil {
		return nil, err
	}
	return pod, nil
}
