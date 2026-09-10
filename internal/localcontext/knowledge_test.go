package localcontext

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKnowledgeSearchIncrementalUpdateAndPortability(t *testing.T) {
	root := t.TempDir()
	store := New(filepath.Join(root, "contexts"))
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
	state, err := store.ContextForKnowledge("state", 1024)
	if err != nil {
		t.Fatal(err)
	}
	memory, err := store.CreateDerivedMemory("memory", state, "codex/test", "# Project\n\nCodename Juniper.")
	if err != nil {
		t.Fatal(err)
	}
	memorySource, err := store.ContextForKnowledge(memory.Name, 1024)
	if err != nil {
		t.Fatal(err)
	}
	records := []KnowledgeRecord{
		{Title: "Release", Text: "The release is Friday.", Keywords: []string{"release", "Friday"}, SourceContext: state.Name, SourceType: state.Type, SourceRevision: state.Revision.Digest, SourceFile: state.Files[0], Generator: "codex/test"},
		{Title: "Codename", Text: "The project codename is Juniper.", Keywords: []string{"project", "Juniper"}, SourceContext: memorySource.Name, SourceType: memorySource.Type, SourceRevision: memorySource.Revision.Digest, SourceFile: memorySource.Files[0], Generator: "codex/test"},
	}
	manifest, err := store.WriteKnowledge("knowledge", []GenerationSource{state, memorySource}, "codex/test", records)
	if err != nil {
		t.Fatal(err)
	}
	results, err := store.SearchKnowledge("knowledge", "Juniper project", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Record.SourceContext != "memory" || results[0].Record.SourceFile != "memory/MEMORY.md" {
		t.Fatalf("search results = %+v", results)
	}

	before, err := store.KnowledgeRecords("knowledge")
	if err != nil {
		t.Fatal(err)
	}
	memoryID := before[0].ID
	if before[0].SourceContext != "memory" {
		memoryID = before[1].ID
	}
	if err := os.WriteFile(stateFile, []byte("release Monday\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	changedState, err := store.ContextForKnowledge("state", 1024)
	if err != nil {
		t.Fatal(err)
	}
	updated := []KnowledgeRecord{
		{Title: "Release", Text: "The release is Monday.", SourceContext: state.Name, SourceType: state.Type, SourceRevision: changedState.Revision.Digest, SourceFile: changedState.Files[0], Generator: "codex/test"},
		records[1],
	}
	if _, err := store.WriteKnowledge("knowledge", []GenerationSource{changedState, memorySource}, "codex/test", updated); err != nil {
		t.Fatal(err)
	}
	after, err := store.KnowledgeRecords("knowledge")
	if err != nil {
		t.Fatal(err)
	}
	foundMemoryID := false
	for _, record := range after {
		if record.SourceContext == "memory" && record.ID == memoryID {
			foundMemoryID = true
		}
	}
	if !foundMemoryID {
		t.Fatal("unchanged memory record was not preserved")
	}

	bundle := filepath.Join(root, "knowledge.context")
	if _, err := store.Export("knowledge", bundle); err != nil {
		t.Fatal(err)
	}
	importedStore := New(filepath.Join(root, "imported"))
	if _, err := importedStore.Import(bundle, "knowledge-copy"); err != nil {
		t.Fatal(err)
	}
	results, err = importedStore.SearchKnowledge("knowledge-copy", "release Monday", 5)
	if err != nil || len(results) != 1 || results[0].Record.Text != "The release is Monday." {
		t.Fatalf("imported search = %+v err=%v manifest=%+v", results, err, manifest)
	}
}
