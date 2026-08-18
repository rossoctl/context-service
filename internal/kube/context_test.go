package kube

import (
	"context"
	"testing"

	"github.com/rossoctl/context-service/internal/contextresource"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
)

func TestManagerCreatesGetsAndDeletesWorkspaceContext(t *testing.T) {
	manager := &Manager{core: kubernetesfake.NewSimpleClientset()}
	request := contextresource.CreateRequest{
		Name: "research", Namespace: "team1", Type: "workspace",
		Storage: contextresource.Storage{Backend: "pvc", Size: "10Gi", AccessMode: "ReadWriteMany"},
	}
	created, err := manager.CreateContext(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if created.Attachment.ClaimName != "context-research" || created.Status != "provisioning" {
		t.Fatalf("unexpected context: %+v", created)
	}
	pvc, err := manager.core.CoreV1().PersistentVolumeClaims("team1").Get(context.Background(), "context-research", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pvc.Status.Phase = corev1.ClaimBound
	if _, err := manager.core.CoreV1().PersistentVolumeClaims("team1").UpdateStatus(context.Background(), pvc, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	ready, err := manager.GetContext(context.Background(), "team1", "research")
	if err != nil || ready.Status != "ready" {
		t.Fatalf("context = %+v, err = %v", ready, err)
	}
	items, err := manager.ListContexts(context.Background(), "team1")
	if err != nil || len(items) != 1 || items[0].Name != "research" {
		t.Fatalf("contexts = %+v, err = %v", items, err)
	}
	if err := manager.DeleteContext(context.Background(), "team1", "research"); err != nil {
		t.Fatal(err)
	}
}
