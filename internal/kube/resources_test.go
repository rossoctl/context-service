package kube

import (
	"testing"

	"github.com/rossoctl/context-service/internal/contextresource"
)

func TestBuildContextPVCPreservesSupportedType(t *testing.T) {
	for _, contextType := range []string{"workspace", "memory", "knowledge", "artifacts"} {
		t.Run(contextType, func(t *testing.T) {
			request := contextresource.CreateRequest{
				Name: "research", Namespace: "team1", Type: contextType,
				Storage: contextresource.Storage{Backend: "pvc", Size: "10Gi", AccessMode: "ReadWriteMany", StorageClass: "ibm-scale-csi"},
			}
			pvc := buildContextPVC(request)
			if pvc.Name != "context-research" || pvc.Namespace != "team1" {
				t.Fatalf("unexpected PVC identity: %s/%s", pvc.Namespace, pvc.Name)
			}
			if pvc.Labels[contextTypeLabel] != contextType || pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != "ibm-scale-csi" {
				t.Fatalf("unexpected PVC: %#v", pvc)
			}
		})
	}
}
