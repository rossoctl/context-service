package kube

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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

func TestManagerPublishesContextRevision(t *testing.T) {
	manager := &Manager{core: kubernetesfake.NewSimpleClientset()}
	request := contextresource.CreateRequest{
		Name: "research", Namespace: "team1", Type: "state",
		Storage: contextresource.Storage{Backend: "pvc", Size: "1Gi", AccessMode: "ReadWriteOnce"},
	}
	if _, err := manager.CreateContext(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	revision := contextresource.Revision{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", CreatedAt: time.Now().UTC(), Operation: "sync", Producer: "contextctl"}
	published, err := manager.PublishContextRevision(context.Background(), "team1", "research", revision)
	if err != nil {
		t.Fatal(err)
	}
	if published.CurrentRevision != revision.ID || len(published.Revisions) != 1 {
		t.Fatalf("published context = %+v", published)
	}
	if _, err := manager.PublishContextRevision(context.Background(), "team1", "research", revision); err != nil {
		t.Fatal(err)
	}
	got, err := manager.GetContext(context.Background(), "team1", "research")
	if err != nil || len(got.Revisions) != 1 {
		t.Fatalf("duplicate publication = %+v, err = %v", got.Revisions, err)
	}
}

func TestFrozenContextRejectsDifferentRevisionPublication(t *testing.T) {
	current := strings.Repeat("a", 64)
	pvc := uploadTestPVC("pvc-uid", current, current)
	manager := &Manager{core: kubernetesfake.NewSimpleClientset(pvc)}

	different := contextresource.Revision{
		ID: strings.Repeat("b", 64), CreatedAt: time.Now().UTC(), Operation: "sync", Producer: "contextctl",
	}
	if _, err := manager.PublishContextRevision(context.Background(), "team1", "research", different); !errors.Is(err, contextresource.ErrFrozen) {
		t.Fatalf("PublishContextRevision error = %v, want ErrFrozen", err)
	}
	got, err := manager.GetContext(context.Background(), "team1", "research")
	if err != nil {
		t.Fatal(err)
	}
	if got.CurrentRevision != current || got.FrozenRevision != current {
		t.Fatalf("frozen context changed: %+v", got)
	}
}

func TestFrozenContextAllowsExactRevisionPublication(t *testing.T) {
	current := strings.Repeat("a", 64)
	pvc := uploadTestPVC("pvc-uid", current, current)
	manager := &Manager{core: kubernetesfake.NewSimpleClientset(pvc)}
	revision := contextresource.Revision{
		ID: current, CreatedAt: time.Now().UTC(), Operation: "upload", Producer: "context-service",
	}

	got, err := manager.PublishContextRevision(context.Background(), "team1", "research", revision)
	if err != nil {
		t.Fatal(err)
	}
	if got.CurrentRevision != current || got.FrozenRevision != current {
		t.Fatalf("idempotent publication = %+v", got)
	}
}
