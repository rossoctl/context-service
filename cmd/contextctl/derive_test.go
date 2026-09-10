package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeriveMemoryCreatesProvenancedContextAndIsIdempotent(t *testing.T) {
	home := filepath.Join(t.TempDir(), "contexts")
	t.Setenv("CS_CONTEXT_HOME", home)
	store := localContextStore()
	manifest, err := store.Create("session", "state")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(manifest.Path, "harnesses", "claude", "project")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session.jsonl"), []byte("project codename is juniper\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	original := memoryGenerator
	defer func() { memoryGenerator = original }()
	calls := 0
	memoryGenerator = func(_ context.Context, agent, model, prompt string) (string, string, error) {
		calls++
		if agent != "claude" || model != "test" || !strings.Contains(prompt, "codename is juniper") {
			t.Fatalf("unexpected generator input: agent=%s model=%s prompt=%s", agent, model, prompt)
		}
		return "# Project\n\n- Codename: Juniper", "claude/test", nil
	}
	if err := deriveMemory([]string{"session", "--name", "memory", "--model", "test"}); err != nil {
		t.Fatal(err)
	}
	if err := deriveMemory([]string{"session", "--name", "memory", "--model", "test"}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("generator calls = %d, want 1", calls)
	}
	derived, err := store.Get("memory")
	if err != nil {
		t.Fatal(err)
	}
	if derived.Derivation == nil || derived.Derivation.SourceContext != "session" || derived.Derivation.Generator != "claude/test" {
		t.Fatalf("derived manifest = %+v", derived)
	}
}
