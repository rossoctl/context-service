package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rossoctl/context-service/internal/localcontext"
)

func TestDeriveKnowledgeReusesUnchangedSourceRecords(t *testing.T) {
	home := filepath.Join(t.TempDir(), "contexts")
	t.Setenv("CS_CONTEXT_HOME", home)
	store := localContextStore()
	stateManifest, err := store.Create("state", "state")
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(stateManifest.Path, "harnesses", "claude")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(stateDir, "session.jsonl")
	if err := os.WriteFile(stateFile, []byte("release Friday\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := store.StateForGeneration("state", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateDerivedMemory("memory", state, "codex/test", "Codename Juniper."); err != nil {
		t.Fatal(err)
	}

	original := knowledgeGenerator
	defer func() { knowledgeGenerator = original }()
	calls := map[string]int{}
	knowledgeGenerator = func(_ context.Context, _, _ string, prompt string) ([]knowledgeDraft, string, error) {
		source := "state"
		file := "harnesses/claude/session.jsonl"
		text := "The release is Friday."
		if strings.Contains(prompt, "Source context: memory") {
			source = "memory"
			file = "memory/MEMORY.md"
			text = "The codename is Juniper."
		}
		calls[source]++
		return []knowledgeDraft{{Title: source, Text: text, Keywords: []string{source}, SourceFile: file}}, "codex/test", nil
	}
	args := []string{"--name", "knowledge", "--from", "state", "--from", "memory", "--agent", "codex"}
	if err := deriveKnowledge(args); err != nil {
		t.Fatal(err)
	}
	first, err := store.KnowledgeRecords("knowledge")
	if err != nil {
		t.Fatal(err)
	}
	if err := deriveKnowledge(args); err != nil {
		t.Fatal(err)
	}
	if calls["state"] != 1 || calls["memory"] != 1 {
		t.Fatalf("unchanged sources regenerated: %+v", calls)
	}
	memoryID := recordIDForSource(t, first, "memory")
	if err := os.WriteFile(stateFile, []byte("release Monday\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := deriveKnowledge(args); err != nil {
		t.Fatal(err)
	}
	if calls["state"] != 2 || calls["memory"] != 1 {
		t.Fatalf("incremental calls = %+v", calls)
	}
	after, err := store.KnowledgeRecords("knowledge")
	if err != nil {
		t.Fatal(err)
	}
	if recordIDForSource(t, after, "memory") != memoryID {
		t.Fatal("unchanged memory record ID changed")
	}
}

func recordIDForSource(t *testing.T, records []localcontext.KnowledgeRecord, source string) string {
	t.Helper()
	for _, record := range records {
		if record.SourceContext == source {
			return record.ID
		}
	}
	t.Fatalf("missing record for %s: %+v", source, records)
	return ""
}
