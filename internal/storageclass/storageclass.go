package storageclass

import "context"

// Resource is the stable, purpose-built view of a Kubernetes StorageClass
// exposed to Context Service clients.
type Resource struct {
	Name                 string `json:"name"`
	Default              bool   `json:"default"`
	Provisioner          string `json:"provisioner"`
	VolumeBindingMode    string `json:"volumeBindingMode"`
	ReclaimPolicy        string `json:"reclaimPolicy"`
	AllowVolumeExpansion bool   `json:"allowVolumeExpansion"`
}

type List struct {
	Items []Resource `json:"items"`
}

type Manager interface {
	ListStorageClasses(context.Context) ([]Resource, error)
}
