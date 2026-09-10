package kube

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rossoctl/context-service/internal/contextresource"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
)

func TestPublishAndQueryAuthorizedContextIndex(t *testing.T) {
	manager := &Manager{core: kubernetesfake.NewSimpleClientset()}
	owner := contextresource.Subject{Kind: "user", Name: "owner"}
	if _, err := manager.CreateContext(context.Background(), contextresource.CreateRequest{
		Name: "memory", Namespace: "team1", Type: "memory", Owner: owner,
		Storage: contextresource.Storage{Backend: "pvc", Size: "1Gi", AccessMode: "ReadWriteOnce"},
	}); err != nil {
		t.Fatal(err)
	}
	revision := contextresource.Revision{ID: strings.Repeat("a", 64), CreatedAt: time.Now().UTC(), Operation: "sync"}
	if _, err := manager.PublishContextRevision(context.Background(), "team1", "memory", revision); err != nil {
		t.Fatal(err)
	}
	record := contextresource.QueryRecord{
		ID: "fact", Context: "memory", Type: "memory", Revision: revision.ID,
		Title: "Release", Text: "Release on Friday", Source: contextresource.SourceReference{Context: "state", Type: "state", Revision: strings.Repeat("b", 64)},
	}
	if err := manager.PublishContextQueryIndex(context.Background(), "team1", "memory", contextresource.QueryIndex{Revision: revision.ID, Records: []contextresource.QueryRecord{record}}); err != nil {
		t.Fatal(err)
	}
	response, err := manager.QueryContexts(context.Background(), "team1", owner, contextresource.QueryRequest{Query: "release", Limit: 10})
	if err != nil || len(response.Items) != 1 || response.Items[0].Record.Source.Context != "state" {
		t.Fatalf("response = %+v, err = %v", response, err)
	}
	outsider, err := manager.QueryContexts(context.Background(), "team1", contextresource.Subject{Kind: "user", Name: "outsider"}, contextresource.QueryRequest{Query: "release"})
	if err != nil || len(outsider.Items) != 0 {
		t.Fatalf("outsider response = %+v, err = %v", outsider, err)
	}
	if _, err := manager.QueryContexts(context.Background(), "team1", contextresource.Subject{Kind: "user", Name: "outsider"}, contextresource.QueryRequest{Query: "release", Contexts: []string{"memory"}}); !errors.Is(err, contextresource.ErrNotFound) {
		t.Fatalf("explicit unauthorized query error = %v", err)
	}
	newRevision := contextresource.Revision{ID: strings.Repeat("c", 64), CreatedAt: time.Now().UTC(), Operation: "sync"}
	if _, err := manager.PublishContextRevision(context.Background(), "team1", "memory", newRevision); err != nil {
		t.Fatal(err)
	}
	stale, err := manager.QueryContexts(context.Background(), "team1", owner, contextresource.QueryRequest{Query: "release"})
	if err != nil || len(stale.Items) != 0 || len(stale.Unavailable) != 1 || stale.Unavailable[0] != "memory" {
		t.Fatalf("stale response = %+v, err = %v", stale, err)
	}
}
