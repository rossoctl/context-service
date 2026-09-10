package localcontext

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCapturePublishesOnlyChangedRevisions(t *testing.T) {
	root := t.TempDir()
	store := New(filepath.Join(root, "contexts"))
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create("state", "state"); err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(root, "session.jsonl")
	writeFixture(t, session, "first\n")
	if _, _, err := store.CaptureSessionFile("state", "codex", project, "session-1", session); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CaptureSessionFile("state", "codex", project, "session-1", session); err != nil {
		t.Fatal(err)
	}
	revisions, err := store.Revisions("state")
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) != 1 || revisions[0].Operation != "capture" || revisions[0].Producer != "codex" {
		t.Fatalf("unchanged capture revisions = %+v", revisions)
	}
	writeFixture(t, session, "second\n")
	if _, _, err := store.CaptureSessionFile("state", "codex", project, "session-1", session); err != nil {
		t.Fatal(err)
	}
	revisions, err = store.Revisions("state")
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) != 2 || revisions[0].ID == revisions[1].ID {
		t.Fatalf("changed capture revisions = %+v", revisions)
	}
}

func TestMultiSourceLineageSurvivesMissingSourcesAndBundleRoundTrip(t *testing.T) {
	root := t.TempDir()
	store := New(filepath.Join(root, "contexts"))
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	var sources []GenerationSource
	for _, name := range []string{"alpha", "beta"} {
		if _, err := store.Create(name, "state"); err != nil {
			t.Fatal(err)
		}
		session := filepath.Join(root, name+".jsonl")
		writeFixture(t, session, name+" facts\n")
		if _, _, err := store.CaptureSessionFile(name, "codex", project, "session-1", session); err != nil {
			t.Fatal(err)
		}
		source, err := store.ContextForKnowledge(name, 1024)
		if err != nil {
			t.Fatal(err)
		}
		sources = append(sources, source)
	}
	records := []KnowledgeRecord{
		{Title: "Alpha", Text: "alpha facts", SourceContext: "alpha", SourceType: "state", SourceRevision: sources[0].Revision.Digest, SourceFile: sources[0].Files[0], Generator: "test"},
		{Title: "Beta", Text: "beta facts", SourceContext: "beta", SourceType: "state", SourceRevision: sources[1].Revision.Digest, SourceFile: sources[1].Files[0], Generator: "test"},
	}
	parameters := map[string]string{"model": "test-model", "mode": "compact"}
	manifest, err := store.WriteKnowledge("combined", sources, "test/test-model", records, parameters)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Revisions) != 1 || len(manifest.Revisions[0].Sources) != 2 || manifest.Revisions[0].Parameters["mode"] != "compact" {
		t.Fatalf("derived revision = %+v", manifest.Revisions)
	}
	if err := os.RemoveAll(store.contextDir("alpha")); err != nil {
		t.Fatal(err)
	}
	stillValid, err := store.Get("combined")
	if err != nil || len(stillValid.Revisions[0].Sources) != 2 {
		t.Fatalf("lineage after source deletion = %+v, err = %v", stillValid.Revisions, err)
	}
	bundlePath := filepath.Join(root, "combined.context")
	if _, err := store.Export("combined", bundlePath); err != nil {
		t.Fatal(err)
	}
	imported, err := New(filepath.Join(root, "imported")).Import(bundlePath, "portable")
	if err != nil {
		t.Fatal(err)
	}
	if imported.CurrentRevision != manifest.CurrentRevision || len(imported.Revisions) != 1 || len(imported.Revisions[0].Sources) != 2 {
		t.Fatalf("imported revision history = %+v", imported.Revisions)
	}
}
