package kube

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rossoctl/context-service/internal/contextresource"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
)

func uploadTestPVC(uid, revision, frozen string) *corev1.PersistentVolumeClaim {
	annotations := map[string]string{currentRevisionAnnotation: revision}
	if frozen != "" {
		annotations[frozenRevisionAnnotation] = frozen
	}
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: "context-research", Namespace: "team1", UID: types.UID(uid), Annotations: annotations,
			Labels: map[string]string{managedLabel: managedBy, contextLabel: "research", contextTypeLabel: "artifacts"},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}},
		},
	}
}

func TestBuildContextUploadPodMountsOnlyTheContextPVC(t *testing.T) {
	pod := buildContextUploadPod("team1", "context-research", types.UID("pvc-uid"), "context-service:test")
	if pod.Namespace != "team1" || pod.Spec.Containers[0].Image != "context-service:test" {
		t.Fatalf("unexpected upload pod: %#v", pod)
	}
	if len(pod.Spec.Volumes) != 1 || pod.Spec.Volumes[0].PersistentVolumeClaim.ClaimName != "context-research" {
		t.Fatalf("unexpected upload volumes: %#v", pod.Spec.Volumes)
	}
	if len(pod.OwnerReferences) != 1 || pod.OwnerReferences[0].UID != types.UID("pvc-uid") {
		t.Fatalf("upload helper owner reference = %#v", pod.OwnerReferences)
	}
	resources := pod.Spec.Containers[0].Resources
	if resources.Requests.Cpu().IsZero() || resources.Requests.Memory().IsZero() ||
		resources.Limits.Cpu().IsZero() || resources.Limits.Memory().IsZero() {
		t.Fatalf("upload helper resources are incomplete: %#v", resources)
	}
	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
		t.Fatal("upload helper must not receive a Kubernetes service-account token")
	}
	if pod.Spec.SecurityContext == nil || pod.Spec.SecurityContext.FSGroupChangePolicy == nil || *pod.Spec.SecurityContext.FSGroupChangePolicy != corev1.FSGroupChangeOnRootMismatch {
		t.Fatalf("FSGroupChangePolicy = %#v", pod.Spec.SecurityContext)
	}
	if pod.Spec.SecurityContext.SeccompProfile == nil || pod.Spec.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Fatalf("SeccompProfile = %#v", pod.Spec.SecurityContext.SeccompProfile)
	}
	security := pod.Spec.Containers[0].SecurityContext
	if security == nil || security.AllowPrivilegeEscalation == nil || *security.AllowPrivilegeEscalation || security.ReadOnlyRootFilesystem == nil || !*security.ReadOnlyRootFilesystem {
		t.Fatalf("uploader security context = %#v", security)
	}
	if pod.Labels[poolLabel] != "" {
		t.Fatalf("upload helper must not have a sandbox-pool label: %#v", pod.Labels)
	}
}

func TestContextUploadExpandedLimitRespectsSmallPVC(t *testing.T) {
	pvc := uploadTestPVC("pvc-uid", strings.Repeat("a", 64), "")
	pvc.Spec.Resources.Requests[corev1.ResourceStorage] = resource.MustParse("100Mi")
	limit, err := contextUploadExpandedLimit(pvc)
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(90 << 20); limit != want {
		t.Fatalf("expanded limit = %d, want %d", limit, want)
	}

	pvc.Spec.Resources.Requests[corev1.ResourceStorage] = resource.MustParse("2Gi")
	limit, err = contextUploadExpandedLimit(pvc)
	if err != nil || limit != contextUploadMaxExpandedBytes {
		t.Fatalf("large PVC limit = %d, error = %v", limit, err)
	}
}

func TestDeleteContextUploadPodWaitsForRemoval(t *testing.T) {
	manager := &Manager{core: kubernetesfake.NewSimpleClientset(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "uploader", Namespace: "team1"}})}
	if err := manager.deleteContextUploadPod(context.Background(), "team1", "uploader"); err != nil {
		t.Fatal(err)
	}
	_, err := manager.core.CoreV1().Pods("team1").Get(context.Background(), "uploader", metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("helper pod still exists: %v", err)
	}
}

func TestUploadContextRequiresAnExistingManagedContext(t *testing.T) {
	manager := &Manager{core: kubernetesfake.NewSimpleClientset()}
	_, err := manager.UploadContext(context.Background(), "team1", "missing", "uid", bytes.NewBufferString("bundle"))
	if !errors.Is(err, contextresource.ErrNotFound) {
		t.Fatalf("UploadContext error = %v", err)
	}
}

func TestUploadContextRejectsRecreatedPVC(t *testing.T) {
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
		Name: "context-research", Namespace: "team1", UID: types.UID("new-uid"),
		Labels: map[string]string{managedLabel: managedBy, contextLabel: "research", contextTypeLabel: "artifacts"},
	}}
	manager := &Manager{core: kubernetesfake.NewSimpleClientset(pvc)}
	_, err := manager.UploadContext(context.Background(), "team1", "research", "old-uid", bytes.NewBufferString("bundle"))
	if !errors.Is(err, contextresource.ErrNotFound) {
		t.Fatalf("UploadContext error = %v", err)
	}
	pods, listErr := manager.core.CoreV1().Pods("team1").List(context.Background(), metav1.ListOptions{})
	if listErr != nil || len(pods.Items) != 0 {
		t.Fatalf("unexpected helper pods: %+v, error=%v", pods.Items, listErr)
	}
}

func TestFreezeContextIsIdempotentForExactRevision(t *testing.T) {
	revision := strings.Repeat("a", 64)
	manager := &Manager{core: kubernetesfake.NewSimpleClientset(uploadTestPVC("pvc-uid", revision, ""))}

	first, err := manager.FreezeContext(context.Background(), "team1", "research", revision, "pvc-uid")
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.FreezeContext(context.Background(), "team1", "research", revision, "pvc-uid")
	if err != nil {
		t.Fatal(err)
	}
	if first.FrozenRevision != revision || second.FrozenRevision != revision || second.StorageUID != "pvc-uid" {
		t.Fatalf("first = %+v, second = %+v", first, second)
	}
}

func TestFreezeContextRejectsRevisionAndStorageIdentityChanges(t *testing.T) {
	revision := strings.Repeat("a", 64)
	tests := []struct {
		name        string
		pvc         *corev1.PersistentVolumeClaim
		expectedRev string
		expectedUID string
		want        error
	}{
		{name: "revision mismatch", pvc: uploadTestPVC("pvc-uid", revision, ""), expectedRev: strings.Repeat("b", 64), expectedUID: "pvc-uid", want: contextresource.ErrRevisionConflict},
		{name: "frozen differently", pvc: uploadTestPVC("pvc-uid", revision, strings.Repeat("b", 64)), expectedRev: revision, expectedUID: "pvc-uid", want: contextresource.ErrFrozen},
		{name: "recreated PVC", pvc: uploadTestPVC("new-uid", revision, ""), expectedRev: revision, expectedUID: "old-uid", want: contextresource.ErrNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := &Manager{core: kubernetesfake.NewSimpleClientset(test.pvc)}
			_, err := manager.FreezeContext(context.Background(), "team1", "research", test.expectedRev, test.expectedUID)
			if !errors.Is(err, test.want) {
				t.Fatalf("FreezeContext error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestFrozenContextRejectsUploadBeforeCreatingHelper(t *testing.T) {
	revision := strings.Repeat("a", 64)
	manager := &Manager{core: kubernetesfake.NewSimpleClientset(uploadTestPVC("pvc-uid", revision, revision))}
	_, err := manager.UploadContext(context.Background(), "team1", "research", "pvc-uid", bytes.NewBufferString("bundle"))
	if !errors.Is(err, contextresource.ErrFrozen) {
		t.Fatalf("UploadContext error = %v", err)
	}
	pods, listErr := manager.core.CoreV1().Pods("team1").List(context.Background(), metav1.ListOptions{})
	if listErr != nil || len(pods.Items) != 0 {
		t.Fatalf("unexpected helper pods: %+v, error=%v", pods.Items, listErr)
	}
}

func TestFreezeContextRejectsAnUploadInProgress(t *testing.T) {
	revision := strings.Repeat("a", 64)
	manager := &Manager{core: kubernetesfake.NewSimpleClientset(uploadTestPVC("pvc-uid", revision, ""))}
	unlock := manager.lockContextUpload("pvc-uid")
	_, err := manager.FreezeContext(context.Background(), "team1", "research", revision, "pvc-uid")
	if !errors.Is(err, contextresource.ErrUploadInProgress) {
		t.Fatalf("freeze error = %v", err)
	}
	unlock()
	if _, err := manager.FreezeContext(context.Background(), "team1", "research", revision, "pvc-uid"); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupExpiredContextUploadPods(t *testing.T) {
	old := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "old", Namespace: "team1", CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Hour)),
		Labels: map[string]string{managedLabel: managedBy, "context.rossoctl.io/component": "uploader"},
	}}
	recent := old.DeepCopy()
	recent.Name = "recent"
	recent.CreationTimestamp = metav1.Now()
	manager := &Manager{core: kubernetesfake.NewSimpleClientset(old, recent)}
	if err := manager.cleanupExpiredContextUploadPods(context.Background(), "team1", 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	pods, err := manager.core.CoreV1().Pods("team1").List(context.Background(), metav1.ListOptions{})
	if err != nil || len(pods.Items) != 1 || pods.Items[0].Name != "recent" {
		t.Fatalf("remaining pods = %+v, error=%v", pods.Items, err)
	}
}
