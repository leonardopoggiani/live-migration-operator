package endpoints

import (
	"fmt"
	"github.com/emicklei/go-restful/v3"
	"strings"

	v1 "github.com/leonardopoggiani/live-migration-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	kubelog "sigs.k8s.io/controller-runtime/pkg/log"
)

var namespace = "default"

type LivemigrationEndpoint struct {
	client client.Client
}

func (pe *LivemigrationEndpoint) SetupWithWS(ws *restful.WebService) {
	ws.Route(ws.GET("Livemigrations").To(pe.list).
		Doc("List of Livemigrations").
		Returns(200, "OK", &List{}))

	ws.Route(ws.POST("Livemigrations").To(pe.create).
		Doc("Create a new Livemigration").
		Reads(&Livemigration{}).
		Returns(200, "OK", &Livemigration{}).
		Returns(400, "Bad Request", nil))
}

func NewLivemigrationEndpoint(client client.Client) *LivemigrationEndpoint {
	return &LivemigrationEndpoint{client: client}
}

func (pe *LivemigrationEndpoint) list(request *restful.Request, response *restful.Response) {
	dl := new(v1.LiveMigrationList)
	err := pe.client.List(request.Request.Context(), dl, &client.ListOptions{})
	if err != nil {
		writeError(response, 404, Error{
			Title:   "Error",
			Details: fmt.Sprintf("Could not retrieve list: %s", err),
		})
	} else {
		l := From.List(dl)
		if err := response.WriteAsJson(l); err != nil {
			writeError(response, 404, Error{
				Title:   "Error",
				Details: "Could not list resources",
			})
		}
	}
}

func (pe *LivemigrationEndpoint) create(request *restful.Request, response *restful.Response) {
	pm := new(Livemigration)
	err := request.ReadEntity(pm)
	pm.Action = strings.ToLower(pm.Action)
	labelSelector := metav1.LabelSelector{MatchLabels: map[string]string{"podmig": "dcn"}}
	pm.Selector = &labelSelector
	pm.Template = corev1.PodTemplateSpec{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{},
		},
	}
	// fmt.Println("Calling an action: - %v", pm.Action)
	fmt.Println(pm)
	if err != nil {
		writeError(response, 400, Error{
			Title:   "Bad Request",
			Details: "Could not read entity",
		})
		return
	}

	if err := pm.Validate(); err != nil {
		writeError(response, 400, Error{
			Title:   "Validation error",
			Details: err.Error(),
		})
		return
	}

	// Check whether sourcePod of live-migration is exist or not
	var sourcePod *corev1.Pod
	// var template corev1.PodTemplateSpec
	if pm.SourcePod != "" {
		fmt.Println(pm.SourcePod)
		var childPods corev1.PodList
		if err := pe.client.List(request.Request.Context(), &childPods, client.InNamespace(namespace)); err != nil {
			writeError(response, 400, Error{
				Title:   "Bad Request",
				Details: "Could not find any running pod for migration",
			})
			return
		}
		if len(childPods.Items) > 0 {
			for _, pod := range childPods.Items {
				if pod.Name == pm.SourcePod && pod.Status.Phase == "Running" {
					sourcePod = pod.DeepCopy()
				}
			}
		}
	}

	if sourcePod == nil {
		writeError(response, 400, Error{
			Title:   "Bad Request",
			Details: "Could not find sourcePod for migration",
		})
		return
	}
	obj := &v1.LiveMigration{
		ObjectMeta: metav1.ObjectMeta{Name: pm.Name, Namespace: "default"},
		Spec: v1.LiveMigrationSpec{
			Replicas:     pm.Replicas,
			SourcePod:    pm.SourcePod,
			DestHost:     pm.DestHost,
			Selector:     pm.Selector,
			Action:       pm.Action,
			SnapshotPath: pm.SnapshotPath,
			Template:     pm.Template,
		},
	}
	err = pe.client.Create(request.Request.Context(), obj, &client.CreateOptions{})
	if err != nil {
		writeError(response, 400, Error{
			Title:   "Error",
			Details: fmt.Sprintf("Could not create object: %s", err),
		})
	} else {
		d := From.Object(obj)
		if err := response.WriteAsJson(d); err != nil {
			writeError(response, 422, Error{
				Title:   "Error",
				Details: "Could not write response",
			})
		}
	}
}

func writeError(response *restful.Response, httpStatus int, err Error) {
	if err := response.WriteHeaderAndJson(httpStatus, err, "application/json"); err != nil {
		kubelog.Log.Error(err, "Could not write the error response")
	}
}
