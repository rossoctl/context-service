package kube

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
)

func TestListStorageClassesReturnsStableSortedView(t *testing.T) {
	wait := storagev1.VolumeBindingWaitForFirstConsumer
	retain := corev1.PersistentVolumeReclaimRetain
	expand := true
	manager := &Manager{core: kubernetesfake.NewSimpleClientset(
		&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "zeta"}, Provisioner: "zeta.csi.io"},
		&storagev1.StorageClass{
			ObjectMeta:  metav1.ObjectMeta{Name: "alpha", Annotations: map[string]string{defaultStorageClassAnnotation: "true"}},
			Provisioner: "alpha.csi.io", VolumeBindingMode: &wait, ReclaimPolicy: &retain, AllowVolumeExpansion: &expand,
		},
	)}

	items, err := manager.ListStorageClasses(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Name != "alpha" || items[1].Name != "zeta" {
		t.Fatalf("unexpected order: %#v", items)
	}
	if !items[0].Default || items[0].VolumeBindingMode != "WaitForFirstConsumer" || items[0].ReclaimPolicy != "Retain" || !items[0].AllowVolumeExpansion {
		t.Fatalf("unexpected alpha: %#v", items[0])
	}
	if items[1].Default || items[1].VolumeBindingMode != "Immediate" || items[1].ReclaimPolicy != "Delete" || items[1].AllowVolumeExpansion {
		t.Fatalf("unexpected zeta defaults: %#v", items[1])
	}
}

func TestListStorageClassesRecognizesBetaDefaultAnnotation(t *testing.T) {
	manager := &Manager{core: kubernetesfake.NewSimpleClientset(&storagev1.StorageClass{
		ObjectMeta:  metav1.ObjectMeta{Name: "legacy", Annotations: map[string]string{betaDefaultStorageClassAnnotation: "true"}},
		Provisioner: "legacy.csi.io",
	})}
	items, err := manager.ListStorageClasses(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || !items[0].Default {
		t.Fatalf("unexpected result: %#v", items)
	}
}
