package endpoints

import (
	v1 "github.com/leonardopoggiani/live-migration-operator/api/v1alpha1"
)

var From = &from{}

type from struct{}

func (c *from) Object(pm *v1.LiveMigration) *Livemigration {
	return &Livemigration{
		Name:         pm.Name,
		Action:       pm.Spec.Action,
		Replicas:     pm.Spec.Replicas,
		SourcePod:    pm.Spec.SourcePod,
		DestHost:     pm.Spec.DestHost,
		SnapshotPath: pm.Spec.SnapshotPath,
		Selector:     pm.Spec.Selector,
		Status:       &pm.Status,
	}
}

func (c *from) List(list *v1.LiveMigrationList) *List {
	items := make([]Livemigration, len(list.Items))
	for i, r := range list.Items {
		items[i] = *c.Object(&r)
	}
	return &List{
		Items: items,
	}
}
