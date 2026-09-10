package localcontext

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStateForGenerationAndCreateDerivedMemory(t *testing.T) {
	root := t.TempDir()
	store := New(filepath.Join(root, "contexts"))
	manifest, err := store.Create("session", "state")
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(manifest.Path, "harnesses", "claude", "project")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(stateDir, "session.jsonl")
	original := []byte("user prefers concise output\n")
	if err := os.WriteFile(statePath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := store.StateForGeneration("session", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if source.Revision.Digest == "" || !strings.Contains(source.Content, "user prefers concise output") {
		t.Fatalf("source = %+v", source)
	}
	memory, err := store.CreateDerivedMemory("project-memory", source, "claude/test", "# Preferences\n\n- Use concise output.")
	if err != nil {
		t.Fatal(err)
	}
	if memory.Type != "memory" || memory.Derivation == nil || memory.Derivation.SourceRevision != source.Revision.Digest {
		t.Fatalf("memory manifest = %+v", memory)
	}
	data, err := os.ReadFile(filepath.Join(memory.Path, "memory", "MEMORY.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Use concise output") {
		t.Fatalf("memory = %q", data)
	}
	bundlePath := filepath.Join(root, "memory.context")
	if _, err := store.Export("project-memory", bundlePath); err != nil {
		t.Fatal(err)
	}
	importedStore := New(filepath.Join(root, "imported"))
	imported, err := importedStore.Import(bundlePath, "memory-copy")
	if err != nil {
		t.Fatal(err)
	}
	if imported.Derivation == nil || imported.Derivation.SourceRevision != source.Revision.Digest {
		t.Fatalf("imported provenance = %+v", imported.Derivation)
	}
	if data, err := os.ReadFile(filepath.Join(imported.Path, "memory", "MEMORY.md")); err != nil || !strings.Contains(string(data), "Use concise output") {
		t.Fatalf("imported memory = %q err=%v", data, err)
	}
	unchanged, err := os.ReadFile(statePath)
	if err != nil || string(unchanged) != string(original) {
		t.Fatalf("source state changed: %q err=%v", unchanged, err)
	}
}

func TestStateForGenerationRejectsWrongTypeAndLimit(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "contexts"))
	if _, err := store.Create("memory", "memory"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StateForGeneration("memory", 1024); err == nil || !strings.Contains(err.Error(), "requires state") {
		t.Fatalf("wrong type error = %v", err)
	}
	manifest, err := store.Create("state", "state")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(manifest.Path, "harnesses", "claude")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "large.txt"), []byte("too large"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StateForGeneration("state", 2); err == nil || !strings.Contains(err.Error(), "generation limit") {
		t.Fatalf("limit error = %v", err)
	}
}
