package kube

import (
	"time"

	"github.com/rossoctl/context-service/internal/contextresource"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func buildContextPVC(request contextresource.CreateRequest) *corev1.PersistentVolumeClaim {
	labels := map[string]string{
		managedLabel: managedBy, contextLabel: request.Name, contextTypeLabel: request.Type,
	}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: contextPVCName(request.Name), Namespace: request.Namespace, Labels: labels},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.PersistentVolumeAccessMode(request.Storage.AccessMode)},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(request.Storage.Size)},
			},
		},
	}
	createdAt := time.Now().UTC()
	owner := normalizeOwner(request.Owner)
	_ = writeAnnotation(pvc, grantsAnnotation, []contextresource.Grant{ownerGrant(owner, createdAt)})
	_ = writeAnnotation(pvc, auditAnnotation, []contextresource.AuditEvent{{Time: createdAt, Action: "context.created", Subject: owner}})
	if request.Storage.StorageClass != "" {
		pvc.Spec.StorageClassName = &request.Storage.StorageClass
	}
	return pvc
}

func contextPVCName(name string) string { return "context-" + name }
