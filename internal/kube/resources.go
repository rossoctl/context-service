package kube

import (
	"fmt"
	"strconv"

	"github.com/rossoctl/context-service/internal/pool"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func buildPVC(namespace string, request pool.CreateRequest, index int) *corev1.PersistentVolumeClaim {
	labels := map[string]string{
		poolLabel: request.Name, managedLabel: managedBy, replicasLabel: strconv.Itoa(request.Replicas),
	}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: pvcName(request, index), Namespace: namespace, Labels: labels},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.PersistentVolumeAccessMode(request.Workspace.AccessMode)},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(request.Workspace.Size)},
			},
		},
	}
	if request.Workspace.StorageClass != "" {
		pvc.Spec.StorageClassName = &request.Workspace.StorageClass
	}
	return pvc
}

func pvcName(request pool.CreateRequest, index int) string {
	if request.Workspace.AccessMode == string(corev1.ReadWriteMany) {
		return request.Name + "-workspace"
	}
	return fmt.Sprintf("%s-workspace-%d", request.Name, index)
}

func selectorFor(poolName string) string { return fmt.Sprintf("%s=%s", poolLabel, poolName) }
