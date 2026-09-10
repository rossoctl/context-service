package kube

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/rossoctl/context-service/internal/contextresource"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
)

func TestManagerCreatesSnapshotsAndClonesContexts(t *testing.T) {
	storageClassName := "csi-hostpath-sc"
	pvc := buildContextPVC(contextresource.CreateRequest{
		Name: "research", Namespace: "team1", Type: "workspace", Owner: contextresource.Subject{Kind: "user", Name: "owner"},
		Storage: contextresource.Storage{Backend: "pvc", Size: "1Gi", AccessMode: "ReadWriteOnce", StorageClass: storageClassName},
	})
	pvc.Annotations[currentRevisionAnnotation] = strings.Repeat("a", 64)
	pvc.Status.Phase = corev1.ClaimBound
	storageClass := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: storageClassName}, Provisioner: "hostpath.csi.k8s.io"}
	snapshotClass := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "snapshot.storage.k8s.io/v1", "kind": "VolumeSnapshotClass",
		"metadata": map[string]any{"name": "csi-hostpath-snapclass"}, "driver": "hostpath.csi.k8s.io", "deletionPolicy": "Delete",
	}}
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		volumeSnapshotResource: "VolumeSnapshotList", volumeSnapshotClassResource: "VolumeSnapshotClassList",
	}, snapshotClass)
	manager := &Manager{core: kubernetesfake.NewSimpleClientset(pvc, storageClass), dynamic: dynamicClient}

	created, err := manager.CreateContextSnapshot(context.Background(), "team1", "research", contextresource.SnapshotRequest{Name: "baseline", Labels: map[string]string{"protected": "true"}})
	if err != nil {
		t.Fatal(err)
	}
	if created.SnapshotClass != "csi-hostpath-snapclass" || created.Revision != strings.Repeat("a", 64) || !created.Protected {
		t.Fatalf("snapshot = %+v", created)
	}
	object, err := dynamicClient.Resource(volumeSnapshotResource).Namespace("team1").Get(context.Background(), created.SnapshotName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedField(object.Object, true, "status", "readyToUse"); err != nil {
		t.Fatal(err)
	}
	if _, err := dynamicClient.Resource(volumeSnapshotResource).Namespace("team1").Update(context.Background(), object, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	clone, err := manager.CloneContextSnapshot(context.Background(), "team1", "research", contextresource.CloneRequest{
		Name: "review", Snapshot: "baseline", Owner: contextresource.Subject{Kind: "agent", Name: "reviewer"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if clone.Name != "review" || clone.CurrentRevision != strings.Repeat("a", 64) {
		t.Fatalf("clone = %+v", clone)
	}
	clonePVC, err := manager.contextPVC(context.Background(), "team1", "review")
	if err != nil {
		t.Fatal(err)
	}
	if clonePVC.Spec.DataSource == nil || clonePVC.Spec.DataSource.Name != created.SnapshotName {
		t.Fatalf("data source = %+v", clonePVC.Spec.DataSource)
	}
	if clonePVC.Annotations[contextSourceAnnotation] != "research" || clonePVC.Annotations[contextSnapshotAnnotation] != "baseline" {
		t.Fatalf("clone provenance = %+v", clonePVC.Annotations)
	}
	if _, err := manager.SetContextRetention(context.Background(), "team1", "research", contextresource.RetentionRequest{MaxAge: "1ns"}); err != nil {
		t.Fatal(err)
	}
	gc, err := manager.GarbageCollectContextSnapshots(context.Background(), "team1", "research", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(gc.Deleted) != 0 || len(gc.Kept) != 1 {
		t.Fatalf("referenced snapshot was not retained: %+v", gc)
	}
}

func TestSnapshotCapabilitiesAndUnsupportedRestore(t *testing.T) {
	storageClassName := "scale"
	pvc := buildContextPVC(contextresource.CreateRequest{Name: "demo", Namespace: "team1", Type: "state", Storage: contextresource.Storage{Backend: "pvc", Size: "1Gi", AccessMode: "ReadWriteOnce", StorageClass: storageClassName}})
	storageClass := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: storageClassName}, Provisioner: "scale.csi.example"}
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{volumeSnapshotClassResource: "VolumeSnapshotClassList"})
	manager := &Manager{core: kubernetesfake.NewSimpleClientset(pvc, storageClass), dynamic: dynamicClient}
	capabilities, err := manager.ContextLifecycleCapabilities(context.Background(), "team1", "demo")
	if err != nil || capabilities.Snapshots || capabilities.Driver != "scale.csi.example" || capabilities.Reason == "" {
		t.Fatalf("capabilities = %+v, err = %v", capabilities, err)
	}
	if _, err := manager.RestoreContextSnapshot(context.Background(), "team1", "demo", contextresource.RestoreRequest{Snapshot: "old"}); !errors.Is(err, contextresource.ErrUnsupported) {
		t.Fatalf("restore error = %v", err)
	}
}

func TestContextSnapshotNameIsStableAndValid(t *testing.T) {
	name := contextSnapshotName(strings.Repeat("a", 50), strings.Repeat("b", 50))
	if len(name) > 63 || name != contextSnapshotName(strings.Repeat("a", 50), strings.Repeat("b", 50)) {
		t.Fatalf("invalid generated snapshot name %q", name)
	}
}
