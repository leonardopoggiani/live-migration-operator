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
	"fmt"
	"github.com/checkpoint-restore/go-criu"
	"github.com/containerd/containerd"
	"github.com/containerd/containerd/api/services/tasks/v1"
	"github.com/containerd/containerd/cio"
	"github.com/containerd/containerd/namespaces"
	api "github.com/leonardopoggiani/live-migration-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	"math/rand"
	"os"
	"os/exec"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"strconv"
	"strings"
	"time"
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

	// Load Kubernetes config
	config, err := clientcmd.BuildConfigFromFlags("", "/home/ubuntu/.kube/config")
	if err != nil {
		klog.ErrorS(err, "failed to load Kubernetes config")
	}

	// Create Kubernetes API client
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		klog.ErrorS(err, "failed to create Kubernetes client")
	}

	/* TODO: initialize containerd/crio env*/
	/* first may need to test the migration with default options using a node that is not the virtual-kubelet node
	(there may be the taint (noSchedule) that complicate things). Then try to understand how migration works originally
	(they used a migration with containerd or something else? it seems that it's not in the GO code)
	Then I can try different migrations supports like CRIO or Containerd one.
	*/

	// ******* CONTAINERD ******* //
	/*
		c, err := containerd.New("/run/containerd/containerd.sock")
		if err != nil {
			klog.Errorf("ERR: %s", err)
		}
		klog.Infof("opened containerd client", "client", c)

		defer c.Close()

		// create a context for docker
		ctx = context.Background()
		ctx = namespaces.WithNamespace(ctx, "my-namespace")

		klog.Infof("created namespace", "namespace", ctx)

		// pull an image and unpack it into the configured snapshotter
		image, err := c.Pull(ctx, "docker.io/library/redis:latest", containerd.WithPullUnpack)
		if err != nil {
			klog.Errorf("ERR: %s", err)
		}
		klog.Infof("pulled image", "image", image)

		// allocate a new RW root filesystem for a container based on the image
		redis, err := c.NewContainer(ctx, "redis-master",
			containerd.WithNewSnapshot("redis-rootfs", image),
			containerd.WithNewSpec(oci.WithImageConfig(image)),
		)
		if err != nil {
			klog.Errorf("ERR %s", err)
		}

		// create a new task
		task, err := redis.NewTask(ctx, cio.NewCreator(cio.WithStdio))
		defer task.Delete(ctx)
		if err != nil {
			klog.Errorf("ERR %s", err)
		}

		// the task is now running and has a pid that can be used to setup networking
		// or other runtime settings outside containerd
		pid := task.Pid()
		klog.Infof("redis-master running as pid %d", pid)

		// start the redis-server process inside the container
		err = task.Start(ctx)
		klog.ErrorS(err, "start task")

		// wait for the task to exit and get the exit status
		status, err := task.Wait(ctx)
		klog.Infof("status task %s", status)

		// checkpoint the task then push it to a registry
		checkpoint, err := task.Checkpoint(ctx)
		klog.ErrorS(err, "checkpoint error")

		// push the image to the registry using the descriptor
		err = c.Push(ctx, "myregistry/checkpoints/redis:master", checkpoint.Target())
		if err != nil {
			// handle error
		}

		// on a new machine pull the checkpoint and restore the redis container
		checkpoint, err = c.Pull(ctx, "myregistry/checkpoints/redis:master")

		redis, err = c.NewContainer(ctx, "redis-master", containerd.WithNewSnapshot("redis-rootfs", checkpoint))
		defer redis.Delete(ctx)

		task, err = redis.NewTask(ctx, cio.NewCreator(cio.WithStdio), containerd.WithTaskCheckpoint(checkpoint))
		defer task.Delete(ctx)

		err = task.Start(ctx)


	*/
	/*
		// node1
		checkpoint, err := container.Checkpoint(ctx, ref, containerd.WithTaskState)

		client.Push(ctx, ref, checkpoint.Target())

		// node2
		checkpoint, err := client.Pull(ctx, ref)

		container, err := client.NewContainer(ctx, id)
		container.Restore(ctx, checkpoint)

		task, err := container.NewTask(ctx, nil, containerd.WithCheckpoint(checkpoint))
	*/
	// ******* CRIO ******* //
	/*
		c, err := crio.New("/run/crio/crio.sock")
	*/

	// ******* original ******* //

	// Load the LiveMigration resource object, if there is no Object, return directly
	var migratingPod api.LiveMigration
	klog.Infof("", "namespaced name", req.NamespacedName)
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
			klog.ErrorS(Err, "sourcePod not exist", "pod", migratingPod.Spec.SourcePod)
			return ctrl.Result{}, Err
		}
	}

	if migratingPod.Spec.DestHost != "" {
		template.Spec.NodeSelector = map[string]string{"kubernetes.io/hostname": migratingPod.Spec.DestHost}
	}

	desiredLabels := getPodsLabelSet(template)
	klog.Infof("", "desired labels: ", desiredLabels)
	klog.Infof("", "migratingPod.Name: ", migratingPod.Name)
	klog.Infof("", "migratingPod.Spec.DestHost: ", migratingPod.Spec.DestHost)

	desiredLabels["migratingPod"] = migratingPod.Name

	annotations := getPodsAnnotationSet(&migratingPod, template)
	klog.Infof("", "annotations: ", annotations)

	// Then list all pods controlled by the LiveMigration resource object
	var childPods corev1.PodList
	if err := r.List(ctx, &childPods, client.InNamespace(req.Namespace), client.MatchingLabels(desiredLabels)); err != nil {
		klog.ErrorS(err, "unable to list child pods")
		return ctrl.Result{}, err
	}

	pod, err := r.desiredPod(migratingPod, &migratingPod, req.Namespace, template)
	klog.Infof("", "pod: ", pod)
	if err != nil {
		return ctrl.Result{}, err
	}

	depl, err := r.desiredDeployment(migratingPod, &migratingPod, req.Namespace, template)
	klog.Infof("", "depl: ", depl)

	if err != nil {
		return ctrl.Result{}, err
	}

	klog.Infof("", "annotations ", annotations["snapshotPath"])
	klog.Infof("", "number of existing pod ", len(childPods.Items))
	klog.Infof("", "desired pod ", pod)
	klog.Infof("", "number of desired pod ", migratingPod.Spec.Replicas)

	count, _, _ := r.getActualRunningPod(&childPods)
	klog.Infof("", "number of actual running pod ", count)

	if annotations["snapshotPolicy"] == "live-migration" && annotations["sourcePod"] != "" {
		// We are live-migrate a running pod here - Hot scale
		klog.Infof("", "live-migrate a running pod")
		// Step1: Check source pod is exist or not clean previous source pod checkpoint/restore annotations and snapshotPath
		sourcePod, err := r.checkPodExist(ctx, annotations["sourcePod"], req.Namespace)
		if err != nil || sourcePod == nil {
			klog.ErrorS(err, "sourcePod not exist", "pod", annotations["sourcePod"])
			return ctrl.Result{}, err
		}
		if err := r.removeCheckpointPod(ctx, sourcePod, "/var/lib/kubelet/migration/", "", req.Namespace); err != nil {
			klog.ErrorS(err, "unable to remove checkpoint", "pod", sourcePod)
			return ctrl.Result{}, err
		}
		klog.Infof("", "Live-migration", "Step 1 - Check source pod is exist or not - completed")
		klog.Infof("", "sourcePod ok ", sourcePod)
		klog.Infof("", "sourcePod status ", sourcePod.Status.Phase)
		// Step2: checkpoint sourcePod
		// copySourcePod := sourcePod.DeepCopy()

		/* TODO: checkpoint sourcePod */
		// for every container inside a pod
		/*
			It may be an option to make a checkpoint of every container separetely or another option may be to compress all
			the images inside one. May need to investigate the performance of the two proposal.
			And if i do the checkpoints concurrently?
		*/

		//******* CRI-O ******* //
		// Get all running pods in the default namespace.
		// Get the Kubernetes client configuration.

		// Iterate over each pod and checkpoint each container.
		containers, err := PrintContainerIDs(clientset)
		if err != nil {
			klog.ErrorS(err, "unable to print containerIDs")
		}

		for _, container := range containers {
			klog.Infof("", "container ->", container)

			// TODO: Check if the container is running.

			// Checkpoint the container.
			err := r.checkpointPodCrio(container.ID)
			if err != nil {
				klog.ErrorS(err, "unable to checkpoint", "container", container.Name, "ID", container.ID)
			} else {
				klog.Infof("", "checkpointPodCrio ok")
			}
		}
	}

	/*
		err = r.checkpointPodContainerd(ctx, sourcePod, "/var/lib/kubelet/migration/")
		if err != nil {
			return ctrl.Result{}, err
		} else {
			klog.Infof("", "checkpointPodCriu ok")
		}

		if err := r.checkpointPod(ctx, sourcePod, ""); err != nil {
			klog.ErrorS(err, "unable to checkpoint", "pod", sourcePod)
			return ctrl.Result{}, err
		}
	*/

	klog.Infof("", "Live-migration", "Step 2 - checkpoint source Pod - completed")
	// TODO(TUONG): make migrate all container inside Pod
	// for container := range copySourcePod.Spec.Containers {
	// 	fmt.Println(copySourcePod.Spec.Containers[container].Name)
	// 	klog.Infof("", "container of pod", copySourcePod.Spec.Containers[container].Name)
	// }

	// at this moment just loop the containers inside a pod, later it must checkpoint and restore every container
	// inside the pod.
	// for container := range sourcePod.Spec.Containers {
	//	klog.Infof("", "container --> ", sourcePod.Spec.Containers[container].Name)
	//}

	// Step3: wait until checkpoint info are created
	//container := sourcePod.Spec.Containers[0].Name // = web
	// multi-containers: [ web | db ]

	// change the path to check, may no be anymore this one may also need to understand how checkpoint is done now
	/*
		checkpointPath := path.Join("/var/lib/kubelet/migration/", sourcePod.Name) // = multi-container-pod
		klog.Infof("", "checkpointPath: ", checkpointPath)                         // = /var/lib/kubelet/migration/multi-container-pod
		klog.Infof("", "live-migration pod", container)                            // = web

		for {
			klog.Infof("looping waiting on file %s", path.Join(checkpointPath, container, "descriptors.json"))

			// why this file and this path?
			_, err := os.Stat(path.Join(checkpointPath, container, "descriptors.json"))
			if os.IsNotExist(err) {
				time.Sleep(100 * time.Millisecond)
			} else {
				break
			}
		}

	*/

	// klog.Infof("", "Live-migration", "checkpointPath"+checkpointPath)
	// klog.Infof("", "Live-migration", "Step 3 - Wait until checkpoint info are created - completed")
	// time.Sleep(10)
	// Step4: restore destPod from sourcePod checkpoted info
	// newPod, err := r.restorePod(ctx, pod, annotations["sourcePod"], checkpointPath)
	/*
		if err != nil {
			klog.ErrorS(err, "unable to restore", "pod", sourcePod)
			return ctrl.Result{}, err
		}

	*/

	// func (r *LiveMigrationReconciler) restorePodCrio(podName string, namespace string, containerName string, checkpointImage string, clientset *kubernetes.Clientset)

	klog.Infof("Migrating pod: %s", migratingPod)

	// first I need to terminate the checkpointed pod
	err = r.terminateCheckpointedPod(migratingPod.Name, clientset)
	if err != nil {
		klog.ErrorS(err, "unable to terminate checkpointed pod", "pod", migratingPod.Name)
	} else {
		klog.Infof("checkpointed pod terminated")
	}

	// TODO: for every container i previously checkpointed, i need to restore it.
	err = r.restorePodCrio(migratingPod.Name, req.Namespace, "web", "checkpoint", clientset, migratingPod.Spec.DestHost)
	if err != nil {
		klog.ErrorS(err, "unable to restore", "pod", migratingPod.Name, "destinationHost", migratingPod.Spec.DestHost)
	}

	klog.Infof("", "Live-migration", "Step 4 - Restore destPod from sourcePod's checkpointed info - completed")
	// time.Sleep(5)
	/*
		for {
			status, _ := r.checkPodExist(ctx, newPod.Name, req.Namespace)
			if status != nil {
				klog.Infof("", "Live-migration", "Step 4.1 - Check whether if newPod is Running or not - completed"+status.Name+string(status.Status.Phase))
				break
			} else {
				time.Sleep(200 * time.Millisecond)
			}
		}

	*/
	klog.Infof("", "Live-migration", "Step 4.1 - Check whether if newPod is Running or not - completed")
	// Step5: Clean checkpointpod process and checkpointPath
	// if err := r.removeCheckpointPod(ctx, sourcePod, "/var/lib/kubelet/migration/", newPod.Name, req.Namespace); err != nil {
	// 	klog.ErrorS(err, "unable to remove checkpoint", "pod", sourcePod)
	// 	return ctrl.Result{}, err
	// }
	// klog.Infof("", "Live-migration", "Step 5 - Clean checkpointPod process and checkpointPath - completed")

	// // Step6: Delete source Pod
	/*
			if err := r.deletePod(ctx, sourcePod); err != nil {
				klog.ErrorS(err, "unable to delete", "source pod", sourcePod)
				return ctrl.Result{}, err
			}
			klog.Infof("", "Live-migration", "Step 6 - Delete the source pod - completed")
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
				klog.ErrorS(err, "unable to create Pod for restore", "pod", pod)
				return ctrl.Result{}, err
			}
			klog.Infof("", "Restore", "Step 0 - Create multiple pods from checkpoint infomation - completed")
		} else if count != 0 && count != migratingPod.Spec.Replicas {
			_, err := os.Stat(annotations["snapshotPath"])
			if annotations["snapshotPolicy"] != "restore" && os.IsNotExist(err) {
				pod.ObjectMeta.Annotations["snapshotPolicy"] = ""
				pod.ObjectMeta.Annotations["snapshotPath"] = ""
			}
			if err := r.updateMultiPod(ctx, migratingPod.Spec.Replicas-count, depl); err != nil {
				klog.ErrorS(err, "unable to create Pod for restore", "pod", pod)
				return ctrl.Result{}, err
			}
			klog.Infof("", "Restore", "Step 0 - Scale multiple pods from checkpoint infomation - completed")
		} else {
			// We are checkpointing a running pod here
			if annotations["snapshotPolicy"] == "checkpoint" && annotations["sourcePod"] != "" {
				_, err := os.Stat(annotations["snapshotPath"])
				// Step1: Check source pod is exist or not
				sourcePod, err := r.checkPodExist(ctx, annotations["sourcePod"], req.Namespace)
				if err != nil || sourcePod == nil {
					klog.ErrorS(err, "sourcePod not exist", "pod", annotations["sourcePod"])
					return ctrl.Result{}, err
				}
				klog.Infof("", "Checkpoint", "Step 1 - Check the snapshotPaht is exist or not - completed")
				// Step2: Clean previous checkpoint folder if exist
				if err := r.removeCheckpointPod(ctx, sourcePod, annotations["snapshotPath"], "", req.Namespace); err != nil {
					klog.ErrorS(err, "unable to remove checkpoint", "pod", sourcePod)
					return ctrl.Result{}, err
				}
				klog.Infof("", "Checkpoint", "Step 2 - Clean previous checkpoint folder if exist - completed")
				// Step3: Checkpoint the source pod now
				if err := r.checkpointPod(ctx, sourcePod, annotations["snapshotPath"]); err != nil {
					klog.ErrorS(err, "unable to checkpoint", "pod", sourcePod)
					return ctrl.Result{}, err
				}
				klog.Infof("", "Checkpoint", "Step 3 - Checkpoint source Pod and save it - completed")
			}
		}

	*/
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *LiveMigrationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&api.LiveMigration{}).
		Complete(r)
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
		snapshotPath = "/var/lib/kubelet/migration/multi-container-pod"
	}
	// not sure but to investigate:
	/*
		updating the annotations for the given pod triggers a Reconciler somewhere that check the pod, see the new annotation
		and perform the checkpoint?
	*/
	klog.Infof("", "checkpointPod", "pod", pod.Name, "snapshotPath", snapshotPath)

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
	// why bring the name like this? just loosing generality in my opinion, what if my pod name is "my-webapp"? It
	// will break all the system
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

func (r *LiveMigrationReconciler) checkPodExist(ctx context.Context, name, namespace string) (*corev1.Pod, error) {
	var childPods corev1.PodList
	if err := r.List(ctx, &childPods, client.InNamespace(namespace)); err != nil {
		return nil, err
	}
	if len(childPods.Items) > 0 {
		for _, pod := range childPods.Items {
			if pod.Name == name && pod.Status.Phase == "Running" {
				return &pod, nil
			}
		}

	}
	return nil, nil
}

func (r *LiveMigrationReconciler) getSourcePodTemplate(ctx context.Context, sourcePodName string, namespace string) (*corev1.PodTemplateSpec, error) {
	sourcePod, err := r.checkPodExist(ctx, sourcePodName, namespace)
	if sourcePod == nil {
		return nil, err
	}
	//(TODO: TuongVX): Get template of pod with multiple containers
	pod := sourcePod.DeepCopy()
	container := pod.Spec.Containers[0]
	template := &corev1.PodTemplateSpec{
		ObjectMeta: pod.ObjectMeta,
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:         container.Name,
					Image:        container.Image,
					Ports:        container.Ports,
					VolumeMounts: container.VolumeMounts,
				},
			},
			Volumes: pod.Spec.Volumes,
		},
	}
	return template, nil
}

func (r *LiveMigrationReconciler) removeCheckpointPod(ctx context.Context, pod *corev1.Pod, snapshotPathCurrent, newPodName, namespace string) error {
	if newPodName != "" {
		for {
			ok, _ := r.checkPodExist(ctx, newPodName, namespace)
			if ok != nil {
				break
			}
		}
	}
	snapshotPolicyUpdate := ""
	snapshotPathUpdate := ""
	if err := r.updateAnnotations(ctx, pod, snapshotPolicyUpdate, snapshotPathUpdate); err != nil {
		return err
	}
	// this is where i need the permissions executing the controller are we really sure it's better like this
	// and not just keeping all the checkpoint versions (how much space are we talking about?) and maybe incremetally
	// increase the version number?
	err := os.Chmod(snapshotPathCurrent, 0777)
	if err != nil {
		return err
	}
	if _, err := exec.Command("sudo", "rm", "-rf", snapshotPathCurrent).Output(); err != nil {
		return err
	}
	return nil
}

func (r *LiveMigrationReconciler) updateAnnotations(ctx context.Context, pod *corev1.Pod, snapshotPolicy, snapshotPath string) error {
	ann := pod.ObjectMeta.Annotations
	if ann == nil {
		ann = make(map[string]string)
	}
	ann["snapshotPolicy"] = snapshotPolicy
	ann["snapshotPath"] = snapshotPath
	pod.ObjectMeta.Annotations = ann
	if err := r.Update(ctx, pod); err != nil {
		return err
	}
	return nil
}

func (r *LiveMigrationReconciler) checkpointPodContainerd(ctx context.Context, pod *corev1.Pod, snapshotPath string) error {

	cr := criu.MakeCriu()
	err := cr.Prepare()
	if err != nil {
		return err
	} else {
		klog.Infof("checkpointPodCriu", "pod", pod.Name, "snapshotPath", snapshotPath)
	}

	// ******* CONTAINERD ******* //

	cntrd, err := containerd.New("/run/containerd/containerd.sock")
	if err != nil {
		klog.Errorf("ERR: %s", err)
	}
	klog.Infof("opened containerd client", "client", cntrd)

	defer func(cntrd *containerd.Client) {
		err := cntrd.Close()
		if err != nil {

		}
	}(cntrd)

	// create a context for docker
	ctx = context.Background()
	ctx = namespaces.WithNamespace(ctx, "liqo-demo")

	klog.Infof("created namespace", "namespace", ctx)

	// list all running containers in the liqo-demo namespace, need to checkpoint all of them
	containers, err := cntrd.ContainerService().List(ctx)
	if err != nil {
		klog.Errorf("ERR: %s", err)
	} else {
		klog.Infof("containers", "containers", containers)
	}

	for cntr := range containers {
		klog.Infof("container", "container", cntr)

		container, err := cntrd.LoadContainer(ctx, string(rune(cntr)))
		if err != nil {
			return err
		} else {
			klog.Infof("loaded container", "container", container)
		}
	}

	container, err := cntrd.LoadContainer(ctx, "redis")
	if err != nil {
		return err
	} else {
		klog.Infof("loaded container", "container", container)
	}

	/* pulling an image from a registry, REMOTE method
	// pull an image and unpack it into the configured snapshotter
	image, err := cntrd.Pull(ctx, "docker.io/library/redis:latest", containerd.WithPullUnpack)
	if err != nil {
		klog.Errorf("ERR: %s", err)
	}
	klog.Infof("pulled image", "image", image)
	*/

	snapshotter := containerd.DefaultSnapshotter
	snapshotterService := cntrd.SnapshotService(snapshotter)
	if err != nil {
		return err
	}
	if err := snapshotterService.Remove(ctx, "redis-rootfs"); err != nil {
		klog.ErrorS(err, "failed to remove snapshot", "snapshot", "redis-rootfs")
	} else {
		klog.Infof("removed snapshot", "snapshot", "redis-rootfs")
	}

	// Get a list of all tasks running in the system
	taskService := cntrd.TaskService()
	tasksList, err := cntrd.TaskService().List(ctx, &tasks.ListTasksRequest{})

	// Iterate through the tasks and stop and delete any tasks with the name "redis"
	for _, t := range tasksList.Tasks {
		taskPid := t.Pid
		taskID := t.ID
		taskContainerID := t.ContainerID
		klog.Infof("task", "pid", taskPid, "id", taskID, "containerID", taskContainerID)

		request := tasks.DeleteTaskRequest{
			ContainerID: taskContainerID,
		}

		response, err := taskService.Delete(ctx, &request)
		if err != nil {
			klog.ErrorS(err, "failed to delete task", "task", "redis")
		} else {
			klog.Infof("deleted task", "task", response)
		}
	}

	err = cntrd.ContainerService().Delete(ctx, "redis")
	if err != nil {
		klog.ErrorS(err, "failed to delete container", "container", "redis")
	} else {
		klog.Infof("deleted container", "container", "redis")
	}

	// allocate a new RW root filesystem for a container based on the image
	/* if using a running container there is no need to create a new one
	redis, err := cntrd.NewContainer(ctx, "redis",
		containerd.WithNewSnapshot("redis-rootfs", image),
		containerd.WithNewSpec(oci.WithImageConfig(image)),
	)
	if err != nil {
		klog.Errorf("ERR %s", err)
	}

	*/

	task, err := container.NewTask(ctx, cio.NewCreator(cio.WithStdio))
	if err != nil {
		klog.Errorf("ERR %s", err)
	} else {
		klog.Infof("created task", "task", task)
	}

	// the task is now running and has a pid that can be used to setup networking
	// or other runtime settings outside containerd
	pid := task.Pid()
	klog.Infof("redis is running as pid %d", pid)

	// start the redis-server process inside the container
	err = task.Start(ctx)
	if err != nil {
		klog.ErrorS(err, "start task")
	}

	// wait for the task to exit and get the exit status
	status, err := task.Wait(ctx)
	if err != nil {
		klog.ErrorS(err, "ERR")
	} else {
		klog.Infof("task status", "status", status)
	}

	// checkpoint the task then push it to a registry
	checkpoint, err := task.Checkpoint(ctx)
	if err != nil {
		klog.ErrorS(err, "checkpoint error")
	} else {
		klog.Infof("checkpointed task", "checkpoint", checkpoint)
	}

	// push the image to the registry using the descriptor
	/*
		err = cntrd.Push(ctx, "docker.io/leonardopoggiani/redis", checkpoint.Target())
		if err != nil {
			klog.Errorf("ERR %s", err)
		} else {
			klog.Infof("pushed image", "image", image)
		}
	*/

	// on a new machine pull the checkpoint and restore the redis container
	/*
		checkpoint, err = cntrd.Pull(ctx, "myregistry/checkpoints/redis:master")

		nginx, err = cntrd.NewContainer(ctx, "redis-master", containerd.WithNewSnapshot("redis-rootfs", checkpoint))
		defer func(nginx containerd.Container, ctx context.Context, opts ...containerd.DeleteOpts) {
			err := nginx.Delete(ctx, opts...)
			if err != nil {

			}
		}(nginx, ctx)

		task, err = nginx.NewTask(ctx, cio.NewCreator(cio.WithStdio), containerd.WithTaskCheckpoint(checkpoint))
		defer func(task containerd.Task, ctx context.Context, opts ...containerd.ProcessDeleteOpts) {
			_, err := task.Delete(ctx, opts...)
			if err != nil {

			}
		}(task, ctx)

		err = task.Start(ctx)

	*/

	/*

		checkpointOpts := criu.CheckpointOpts{
			TrackMem:     true,
			LeaveRunning: true,
		}
		snapshotPolicy := "checkpoint"
		if snapshotPath == "" {
			snapshotPath = "/var/lib/kubelet/migration/kkk"
		}
		if err := r.updateAnnotations(ctx, pod, snapshotPolicy, snapshotPath); err != nil {
			return err
		}
		// Get the containerd client
		client, err := containerd.New("/run/containerd/containerd.sock")
		if err != nil {
			return err
		}
		defer client.Close()

		// Get the container and task for the pod
		container, err := client.LoadContainer(ctx, pod.Name)
		if err != nil {
			return err
		}
		defer container.Delete(ctx)

		task, err := container.Task(ctx, nil)
		if err != nil {
			return err
		}
		defer task.Delete(ctx)

		// Checkpoint the task with custom options
		checkpoint, err := task.CheckpointWithOptions(ctx, checkpointOpts)
		if err != nil {
			return err
		}

		// Push the checkpoint to a registry
		err = client.Push(ctx, "myregistry/checkpoints/"+pod.Name+":latest", checkpoint)
		if err != nil {
			return err
		}

	*/

	return nil
}

func (r *LiveMigrationReconciler) checkpointPodCrio(containerID string) error {
	// Construct the cri-o checkpoint command.
	checkpointCmd := exec.Command("crictl", "checkpoint")
	checkpointCmd.Args = append(checkpointCmd.Args, containerID)

	// Execute the checkpoint command.
	output, err := checkpointCmd.CombinedOutput()
	if err != nil {
		klog.ErrorS(err, "failed to checkpoint container", "container", containerID, "output", string(output))
	} else {
		klog.InfoS("checkpointed pod", "container", containerID, "output", string(output))
	}

	return nil
}

type Container struct {
	ID   string
	Name string
}

func PrintContainerIDs(clientset *kubernetes.Clientset) ([]Container, error) {
	// Get pods in liqo-demo namespace
	pods, err := clientset.CoreV1().Pods("liqo-demo").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods: %v", err)
	}

	// Create a slice of Container structs
	var containers []Container

	// Append the container ID and name for each container in each pod
	for _, pod := range pods.Items {
		for _, containerStatus := range pod.Status.ContainerStatuses {
			idParts := strings.Split(containerStatus.ContainerID, "//")
			if len(idParts) < 2 {
				return nil, fmt.Errorf("invalid container ID format: %v", containerStatus.ContainerID)
			}
			containerID := idParts[1]

			container := Container{
				ID:   containerID,
				Name: containerStatus.Name,
			}
			containers = append(containers, container)
		}
	}

	return containers, nil
}

func (r *LiveMigrationReconciler) restorePodCrio(podName string, namespace string, containerName string, checkpointImage string, clientset *kubernetes.Clientset, destinationHost string) error {

	podClient := clientset.CoreV1().Pods(namespace)
	pod, err := podClient.Get(context.Background(), podName, metav1.GetOptions{})
	if err != nil {
		klog.ErrorS(err, "failed to get pod", "podName", podName, "namespace", namespace)
		return err
	} else {
		klog.InfoS("got pod", "podName", podName, "namespace", namespace)
	}

	// Find the container to restore.
	var container *corev1.ContainerStatus
	for i := range pod.Status.ContainerStatuses {
		if pod.Status.ContainerStatuses[i].Name == containerName {
			container = &pod.Status.ContainerStatuses[i]
			klog.InfoS("found container", "container", container.Name)
			break
		}
	}

	if container == nil {
		klog.ErrorS(err, "container not found", "podName", podName, "namespace", namespace, "containerName", containerName)
	}

	restoredPod := createRestoredPod(podName, namespace, containerName, checkpointImage, destinationHost)

	klog.Infof("restored pod %s", restoredPod)
	// Update the pod
	_, err = podClient.Create(context.Background(), restoredPod, metav1.CreateOptions{})
	if err != nil {
		klog.ErrorS(err, "failed to update pod", "podName", podName, "namespace", namespace)
		return err
	}

	// Wait for the new container to become ready.
	err = r.waitForContainerReady(podName, namespace, containerName, clientset)
	if err != nil {
		klog.ErrorS(err, "container did not become ready", "podName", podName, "namespace", namespace, "containerName", containerName)
		return err
	}

	// Delete the old container.
	containerID := container.ContainerID
	deleteCmd := exec.Command("crictl", "rm", containerID)
	output, err := deleteCmd.CombinedOutput()
	if err != nil {
		klog.ErrorS(err, "failed to delete container", "containerID", containerID, "output", string(output))
		return err
	} else {
		klog.InfoS("deleted container", "containerID", containerID, "output", string(output))
	}

	return nil
}

func createRestoredPod(restoredName string, restoredNamespace string, restoredContainerName string, restoredContainerImage string, destinationHost string) *core.Pod {
	return &core.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      restoredName,
			Namespace: restoredNamespace,
		},
		Spec: core.PodSpec{
			Containers: []core.Container{
				{
					Name:            restoredContainerName,
					Image:           restoredContainerImage,
					ImagePullPolicy: core.PullIfNotPresent,
				},
			},
		},
	}
}

func (r *LiveMigrationReconciler) waitForContainerReady(podName string, namespace string, containerName string, clientset *kubernetes.Clientset) error {
	timeout := 60 * time.Second
	interval := 1 * time.Second
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case <-timer.C:
			return fmt.Errorf("timed out waiting for container to become ready")
		default:
			pod, err := clientset.CoreV1().Pods(namespace).Get(context.Background(), podName, metav1.GetOptions{})
			if err != nil {
				return err
			}

			for i := range pod.Status.ContainerStatuses {
				if pod.Status.ContainerStatuses[i].Name == containerName {
					if pod.Status.ContainerStatuses[i].Ready {
						return nil
					}
					break
				}
			}
		}

		time.Sleep(interval)
	}
}

func (r *LiveMigrationReconciler) terminateCheckpointedPod(podName string, clientset *kubernetes.Clientset) error {
	// get the pod by name
	klog.Infof("", "Terminating pod", "pod", podName)

	pod, err := clientset.CoreV1().Pods("liqo-demo").Get(context.Background(), podName, metav1.GetOptions{})
	if err != nil {
		klog.ErrorS(err, "unable to get pod", "pod", pod.Name)
	} else {
		klog.Info("pod", "pod", podName)
	}

	// delete the pod
	err = clientset.CoreV1().Pods("liqo-demo").Delete(context.Background(), podName, metav1.DeleteOptions{})
	if err != nil {
		klog.ErrorS(err, "unable to delete pod", "pod", pod.Name)
	} else {
		klog.Info("pod deleted", "pod", podName)
	}

	klog.Infof("", "Pod terminated", "pod", podName)
	return nil
}
