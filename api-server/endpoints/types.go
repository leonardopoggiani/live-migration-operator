package endpoints

import (
	"errors"
	// "fmt"
	// "strings"

	"github.com/emicklei/go-restful/v3"
	v1 "github.com/leonardopoggiani/live-migration-operator/api/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type Endpoint interface {
	SetupWithWS(ws *restful.WebService)
}

type Livemigration struct {
	Name         string                  `json:"name"`
	DestHost     string                  `json:"destHost"`
	Replicas     int                     `json:"replicas"`
	Selector     *metav1.LabelSelector   `json:"selector"`
	Action       string                  `json:"action"`
	SnapshotPath string                  `json:"snapshotPath"`
	SourcePod    string                  `json:"sourcePod"`
	Template     corev1.PodTemplateSpec  `json:"template,omitempty"`
	Status       *v1.LiveMigrationStatus `json:"status,omitempty"`
}

func (pm *Livemigration) Validate() error {
	var validated bool
	validated = true
	//TODO(Tuong): check template is valid or not
	// if pm.Template == checkTemplate {
	// 	return error.New("template can't be empty")
	// } else {
	// 	validated = true
	// }
	if validated {
		return nil
	}
	return errors.New("source type validation was not performed, type can only be [WebFolder,S3]")
}

type List struct {
	Items []Livemigration `json:"items"`
}

type Error struct {
	Title   string `json:"title"`
	Details string `json:"details"`
}
