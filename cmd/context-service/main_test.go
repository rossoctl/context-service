package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rossoctl/context-service/internal/localcontext"
)

func TestMaterializeUploadCreatesImmutableConsumerReadableRevision(t *testing.T) {
	root := t.TempDir()
	source := localcontext.New(filepath.Join(root, "source"))
	manifest, err := source.Create("incident", "artifacts")
	if err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(manifest.Path, "artifacts", "report.txt")
	if err := os.MkdirAll(filepath.Dir(payload), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payload, []byte("synthetic incident\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(root, "incident.context")
	if _, err := source.Export("incident", bundle); err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(bundle)
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := materializeUpload(workspace, "test-id", "artifacts", input)
	_ = input.Close()
	if err != nil {
		t.Fatal(err)
	}
	materialized := filepath.Join(root, "workspace", ".context-service", "materialized", result.Revision)
	if result.WorkspacePath != ".context-service/materialized/"+result.Revision {
		t.Fatalf("workspacePath = %q", result.WorkspacePath)
	}
	for _, check := range []struct {
		path string
		mode os.FileMode
	}{
		{materialized, 0o755},
		{filepath.Join(materialized, "artifacts"), 0o755},
		{filepath.Join(materialized, "artifacts", "report.txt"), 0o644},
	} {
		info, err := os.Stat(check.path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != check.mode {
			t.Errorf("%s mode = %#o, want %#o", check.path, info.Mode().Perm(), check.mode)
		}
	}

	input, err = os.Open(bundle)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := materializeUpload(workspace, "second-id", "artifacts", input)
	_ = input.Close()
	if err != nil {
		t.Fatal(err)
	}
	if repeated != result {
		t.Fatalf("repeat result = %+v, want %+v", repeated, result)
	}
	if err := os.WriteFile(filepath.Join(materialized, "artifacts", "report.txt"), []byte("tampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	input, err = os.Open(bundle)
	if err != nil {
		t.Fatal(err)
	}
	_, err = materializeUpload(workspace, "third-id", "artifacts", input)
	_ = input.Close()
	if err == nil {
		t.Fatal("deduplication accepted modified materialized content")
	}
}

func TestCleanupUploadStagingRemovesOnlyImportDirectories(t *testing.T) {
	root := t.TempDir()
	stale := filepath.Join(root, ".context-import-stale")
	keep := filepath.Join(root, "revision")
	if err := os.Mkdir(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(keep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := cleanupUploadStaging(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale staging path still exists: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("revision path was removed: %v", err)
	}
}

func TestMaterializeUploadRejectsTypeMismatchAndUnsafeDestination(t *testing.T) {
	root := t.TempDir()
	source := localcontext.New(filepath.Join(root, "source"))
	manifest, err := source.Create("incident", "artifacts")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(manifest.Path, "artifacts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manifest.Path, "artifacts", "report.txt"), []byte("incident\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(root, "incident.context")
	if _, err := source.Export("incident", bundle); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(bundle)
	if err != nil {
		t.Fatal(err)
	}
	_, err = materializeUpload(workspace, "wrong-type", "memory", input)
	_ = input.Close()
	if err == nil {
		t.Fatal("type mismatch succeeded")
	}

	unsafeWorkspace := filepath.Join(root, "unsafe")
	if err := os.MkdirAll(unsafeWorkspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "outside"), filepath.Join(unsafeWorkspace, ".context-service")); err != nil {
		t.Fatal(err)
	}
	input, err = os.Open(bundle)
	if err != nil {
		t.Fatal(err)
	}
	_, err = materializeUpload(unsafeWorkspace, "unsafe", "artifacts", input)
	_ = input.Close()
	if err == nil {
		t.Fatal("symbolic-link materialization root succeeded")
	}

	revision, err := source.Revision("incident")
	if err != nil {
		t.Fatal(err)
	}
	symlinkWorkspace := filepath.Join(root, "symlink-digest")
	materializedRoot := filepath.Join(symlinkWorkspace, ".context-service", "materialized")
	if err := os.MkdirAll(materializedRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(materializedRoot, revision.Digest)); err != nil {
		t.Fatal(err)
	}
	input, err = os.Open(bundle)
	if err != nil {
		t.Fatal(err)
	}
	_, err = materializeUpload(symlinkWorkspace, "unsafe-digest", "artifacts", input)
	_ = input.Close()
	if err == nil {
		t.Fatal("symbolic-link digest destination succeeded")
	}
}

func TestMaterializeUploadRespectsExpandedLimit(t *testing.T) {
	root := t.TempDir()
	source := localcontext.New(filepath.Join(root, "source"))
	manifest, err := source.Create("incident", "artifacts")
	if err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(manifest.Path, "artifacts", "report.txt")
	if err := os.MkdirAll(filepath.Dir(payload), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payload, []byte("larger than limit"), 0o600); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(root, "incident.context")
	if _, err := source.Export("incident", bundle); err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(bundle)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := materializeUploadWithLimit(workspace, "limited", "artifacts", 8, input); err == nil {
		t.Fatal("materialization exceeded the PVC-derived expanded limit")
	}
}
