package localcontext

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rossoctl/context-service/internal/contextresource"
)

func TestQueryMemoryWithProvenance(t *testing.T) {
	store := New(t.TempDir())
	source, err := store.Create("session", "state")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(source.Path, "harnesses"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source.Path, "harnesses", "session.txt"), []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	sourceRevision, err := store.Revision("session")
	if err != nil {
		t.Fatal(err)
	}
	memory, err := store.CreateDerivedMemory("project-memory", GenerationSource{Name: "session", Type: "state", Revision: sourceRevision}, "test", "Release on Friday\nUse the blue environment")
	if err != nil {
		t.Fatal(err)
	}
	response, err := store.Query(contextresource.QueryRequest{Query: "release", Contexts: []string{"project-memory"}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 1 {
		t.Fatalf("results = %+v", response)
	}
	record := response.Items[0].Record
	if record.Context != "project-memory" || record.Revision != memory.CurrentRevision || record.Source.Context != "session" || record.Source.Revision != sourceRevision.Digest {
		t.Fatalf("record provenance = %+v", record)
	}
}

func TestQueryReportsUnavailableIndex(t *testing.T) {
	store := New(t.TempDir())
	if _, err := store.Create("empty-memory", "memory"); err != nil {
		t.Fatal(err)
	}
	response, err := store.Query(contextresource.QueryRequest{Query: "anything", Contexts: []string{"empty-memory"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 0 || len(response.Unavailable) != 1 || response.Unavailable[0] != "empty-memory" {
		t.Fatalf("response = %+v", response)
	}
}
