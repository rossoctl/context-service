package kube

import (
	"context"
	"errors"
	"testing"

	"github.com/rossoctl/context-service/internal/contextresource"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func TestLegacyContextDefaultsToAnonymousOwner(t *testing.T) {
	legacy := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "context-legacy",
			Namespace: "team1",
			Labels: map[string]string{
				managedLabel: managedBy, contextLabel: "legacy", contextTypeLabel: "state",
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{
				corev1.ResourceStorage: resource.MustParse("1Gi"),
			}},
		},
	}
	manager := &Manager{core: fake.NewSimpleClientset(legacy)}
	anonymous := contextresource.Subject{Kind: "user", Name: "anonymous"}

	items, err := manager.ListAccessibleContexts(context.Background(), "team1", anonymous)
	if err != nil || len(items) != 1 || items[0].Name != "legacy" {
		t.Fatalf("legacy list = %+v, err = %v", items, err)
	}
	for _, permission := range []contextresource.Permission{
		contextresource.PermissionRead,
		contextresource.PermissionWrite,
		contextresource.PermissionAdminister,
	} {
		if _, err := manager.AccessContext(context.Background(), "team1", "legacy", anonymous, permission); err != nil {
			t.Fatalf("legacy %s access denied: %v", permission, err)
		}
	}
	grants, err := manager.ListContextGrants(context.Background(), "team1", "legacy")
	if err != nil || len(grants) != 1 || !sameSubject(grants[0].Subject, anonymous) {
		t.Fatalf("legacy grants = %+v, err = %v", grants, err)
	}

	outsider := contextresource.Subject{Kind: "agent", Name: "outsider"}
	if _, err := manager.AccessContext(context.Background(), "team1", "legacy", outsider, contextresource.PermissionRead); !errors.Is(err, contextresource.ErrNotFound) {
		t.Fatalf("legacy outsider access error = %v", err)
	}
}

func TestExplicitEmptyContextGrantsDoNotUseLegacyFallback(t *testing.T) {
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{grantsAnnotation: "[]"}}}
	if grants := readGrants(pvc); len(grants) != 0 {
		t.Fatalf("explicit empty grants = %+v", grants)
	}
}

func TestContextGrantsEnforceDiscoveryRevocationAndNamespaceIsolation(t *testing.T) {
	manager := &Manager{core: fake.NewSimpleClientset()}
	owner := contextresource.Subject{Kind: "user", Name: "owner"}
	reader := contextresource.Subject{Kind: "agent", Name: "reader"}
	writer := contextresource.Subject{Kind: "agent", Name: "writer"}
	createAccessContext(t, manager, "team1", "shared", owner)
	createAccessContext(t, manager, "team2", "private", owner)

	if visible, err := manager.ListAccessibleContexts(context.Background(), "team1", reader); err != nil || len(visible) != 0 {
		t.Fatalf("least-privilege list = %+v, err = %v", visible, err)
	}
	if _, err := manager.AccessContext(context.Background(), "team1", "shared", reader, contextresource.PermissionRead); !errors.Is(err, contextresource.ErrNotFound) {
		t.Fatalf("unauthorized get error = %v", err)
	}
	if _, err := manager.SetContextGrant(context.Background(), "team1", "shared", contextresource.Grant{Subject: reader, Permissions: []contextresource.Permission{contextresource.PermissionRead, contextresource.PermissionAttach}}, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SetContextGrant(context.Background(), "team1", "shared", contextresource.Grant{Subject: writer, Permissions: []contextresource.Permission{contextresource.PermissionRead, contextresource.PermissionWrite, contextresource.PermissionAttach}}, owner); err != nil {
		t.Fatal(err)
	}
	readAccess, err := manager.AccessContext(context.Background(), "team1", "shared", reader, contextresource.PermissionRead)
	if err != nil || containsPermission(readAccess.EffectiveAccess, contextresource.PermissionWrite) {
		t.Fatalf("reader access = %+v, err = %v", readAccess.EffectiveAccess, err)
	}
	if _, err := manager.AccessContext(context.Background(), "team1", "shared", reader, contextresource.PermissionWrite); !errors.Is(err, contextresource.ErrNotFound) {
		t.Fatalf("reader write error = %v", err)
	}
	if _, err := manager.AccessContext(context.Background(), "team1", "shared", writer, contextresource.PermissionWrite); err != nil {
		t.Fatalf("writer denied: %v", err)
	}
	if visible, err := manager.ListAccessibleContexts(context.Background(), "team2", reader); err != nil || len(visible) != 0 {
		t.Fatalf("cross-namespace list = %+v, err = %v", visible, err)
	}
	if _, err := manager.RevokeContextGrant(context.Background(), "team1", "shared", reader, owner); err != nil {
		t.Fatal(err)
	}
	if visible, err := manager.ListAccessibleContexts(context.Background(), "team1", reader); err != nil || len(visible) != 0 {
		t.Fatalf("revoked list = %+v, err = %v", visible, err)
	}
	events, err := manager.ListContextAudit(context.Background(), "team1", "shared")
	if err != nil || len(events) != 4 {
		t.Fatalf("audit events = %+v, err = %v", events, err)
	}
}

func TestContextConsumersBlockSafeDelete(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "agent-0", Namespace: "team1"},
		Spec:       corev1.PodSpec{ServiceAccountName: "agent", Containers: []corev1.Container{{Name: "agent", Image: "test", VolumeMounts: []corev1.VolumeMount{{Name: "context", MountPath: "/context", ReadOnly: true}}}}, Volumes: []corev1.Volume{{Name: "context", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "context-shared"}}}}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	manager := &Manager{core: fake.NewSimpleClientset([]runtime.Object{pod}...)}
	owner := contextresource.Subject{Kind: "user", Name: "owner"}
	createAccessContext(t, manager, "team1", "shared", owner)
	consumers, err := manager.ListContextConsumers(context.Background(), "team1", "shared")
	if err != nil || len(consumers) != 1 || !consumers[0].Active || consumers[0].AccessMode != "readOnly" {
		t.Fatalf("active consumers = %+v, err = %v", consumers, err)
	}
	if err := manager.DeleteContext(context.Background(), "team1", "shared"); !errors.Is(err, contextresource.ErrInUse) {
		t.Fatalf("safe delete error = %v", err)
	}
	if err := manager.ForceDeleteContext(context.Background(), "team1", "shared"); err != nil {
		t.Fatal(err)
	}
}

func TestDeclaredConsumerBlocksDeleteWhenNoPodIsRunning(t *testing.T) {
	manager := &Manager{core: fake.NewSimpleClientset()}
	owner := contextresource.Subject{Kind: "user", Name: "owner"}
	agent := contextresource.Subject{Kind: "agent", Name: "worker"}
	createAccessContext(t, manager, "team1", "shared", owner)
	consumer := contextresource.Consumer{Subject: agent, Kind: "agent", Name: "worker", AccessMode: "readWrite"}
	if _, err := manager.SetContextConsumer(context.Background(), "team1", "shared", consumer, true, owner); err != nil {
		t.Fatal(err)
	}
	if err := manager.DeleteContext(context.Background(), "team1", "shared"); !errors.Is(err, contextresource.ErrInUse) {
		t.Fatalf("safe delete error = %v", err)
	}
	if _, err := manager.SetContextConsumer(context.Background(), "team1", "shared", consumer, false, owner); err != nil {
		t.Fatal(err)
	}
	if err := manager.DeleteContext(context.Background(), "team1", "shared"); err != nil {
		t.Fatal(err)
	}
}

func createAccessContext(t *testing.T, manager *Manager, namespace, name string, owner contextresource.Subject) {
	t.Helper()
	_, err := manager.CreateContext(context.Background(), contextresource.CreateRequest{
		Name: name, Namespace: namespace, Type: "state", Owner: owner,
		Storage: contextresource.Storage{Backend: "pvc", Size: "1Gi", AccessMode: "ReadWriteOnce"},
	})
	if err != nil {
		t.Fatal(err)
	}
}
