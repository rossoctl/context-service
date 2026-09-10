package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rossoctl/context-service/internal/client"
	"github.com/rossoctl/context-service/internal/localcontext"
)

func TestQueryLocalMemory(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CS_CONTEXT_HOME", root)
	store := localcontext.New(root)
	source, err := store.Create("state", "state")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(source.Path, "harnesses"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source.Path, "harnesses", "session.txt"), []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	revision, err := store.Revision("state")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateDerivedMemory("memory", localcontext.GenerationSource{Name: "state", Type: "state", Revision: revision}, "test", "Release on Friday"); err != nil {
		t.Fatal(err)
	}
	var queryErr error
	output := captureStdout(t, func() {
		queryErr = queryContext(client.New("http://unused", "", nil), []string{"release", "--context", "memory"})
	})
	if queryErr != nil {
		t.Fatal(queryErr)
	}
	for _, want := range []string{"RESULTS (1)", "Release on Friday", "context  memory@", "source   state@"} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q:\n%s", want, output)
		}
	}
}
