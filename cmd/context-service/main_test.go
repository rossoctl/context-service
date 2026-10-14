package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rossoctl/context-service/internal/localcontext"
)

func TestExportMaterializedRevisionCreatesPortableReadableArchive(t *testing.T) {
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
	bundle := filepath.Join(root, "source.context")
	if _, err := source.Export("incident", bundle); err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(bundle)
	if err != nil {
		t.Fatal(err)
	}
	result, err := materializeUpload(root, "test", "artifacts", input)
	_ = input.Close()
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := exportMaterializedRevision(root, result.Revision, exportMaxArchiveSize, &output); err != nil {
		t.Fatal(err)
	}
	if _, err := localcontext.New(filepath.Join(root, "restored")).ImportPortableReaderWithLimits(bytes.NewReader(output.Bytes()), "restored", localcontext.ImportLimits{
		MaxFileSize: uploadMaxFileSize, MaxTotalSize: uploadMaxTotalSize, MaxEntries: uploadMaxEntries,
	}); err != nil {
		t.Fatalf("exported bundle is not portable: %v", err)
	}
	gzipReader, err := gzip.NewReader(bytes.NewReader(output.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	tarReader := tar.NewReader(gzipReader)
	found := false
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Name == "artifacts/report.txt" {
			found = true
			if header.Mode != 0o644 {
				t.Fatalf("file mode = %#o", header.Mode)
			}
			content, err := io.ReadAll(tarReader)
			if err != nil || string(content) != "incident\n" {
				t.Fatalf("content = %q, error = %v", content, err)
			}
		}
	}
	if !found {
		t.Fatal("exported archive omitted payload")
	}
}

func TestExportMaterializedRevisionRejectsUnsafeOrOversizedContent(t *testing.T) {
	revision := strings.Repeat("a", 64)
	root := t.TempDir()
	materialized := filepath.Join(root, ".context-service", "materialized", revision)
	if err := os.MkdirAll(materialized, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "outside"), filepath.Join(materialized, "link")); err != nil {
		t.Fatal(err)
	}
	if err := exportMaterializedRevision(root, revision, exportMaxArchiveSize, io.Discard); err == nil {
		t.Fatal("symbolic link export succeeded")
	}
	if err := os.Remove(filepath.Join(materialized, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(materialized, "large"), bytes.Repeat([]byte("x"), 1024), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := exportMaterializedRevision(root, revision, 32, io.Discard); err == nil {
		t.Fatal("oversized export succeeded")
	}
}

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

func TestMaterializeUploadKeepsExecutableWorkspaceFiles(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "demo-repo")
	for name, mode := range map[string]os.FileMode{"src/greet.py": 0o644, "test.sh": 0o755} {
		target := filepath.Join(directory, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(name+"\n"), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(target, mode); err != nil {
			t.Fatal(err)
		}
	}
	bundle := filepath.Join(root, "demo.context")
	packed, err := localcontext.Pack(directory, bundle, localcontext.PackOptions{})
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	upload := func(id string) (string, error) {
		t.Helper()
		input, err := os.Open(bundle)
		if err != nil {
			t.Fatal(err)
		}
		defer input.Close()
		result, err := materializeUpload(workspace, id, "workspace", input)
		return result.Revision, err
	}
	revision, err := upload("first")
	if err != nil {
		t.Fatal(err)
	}
	if revision != packed.Revision {
		t.Fatalf("materialized revision = %s, packed revision = %s", revision, packed.Revision)
	}
	materialized := filepath.Join(workspace, ".context-service", "materialized", revision, "workspace")
	for name, want := range map[string]os.FileMode{"test.sh": 0o755, "src/greet.py": 0o644} {
		info, err := os.Stat(filepath.Join(materialized, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s mode = %#o, want %#o", name, info.Mode().Perm(), want)
		}
	}
	// A repeated upload verifies the existing materialized revision.
	if _, err := upload("second"); err != nil {
		t.Fatalf("verify of executable revision failed: %v", err)
	}

	var output bytes.Buffer
	if err := exportMaterializedRevision(workspace, revision, exportMaxArchiveSize, &output); err != nil {
		t.Fatal(err)
	}
	gzipReader, err := gzip.NewReader(bytes.NewReader(output.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	modes := map[string]int64{}
	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		modes[header.Name] = header.Mode
	}
	if modes["workspace/test.sh"] != 0o755 || modes["workspace/src/greet.py"] != 0o644 {
		t.Fatalf("exported modes = %v", modes)
	}

	script := filepath.Join(materialized, "test.sh")
	for _, tampered := range []os.FileMode{0o644, 0o775, 0o711} {
		if err := os.Chmod(script, tampered); err != nil {
			t.Fatal(err)
		}
		if _, err := upload("tampered"); err == nil {
			t.Errorf("verify accepted materialized script with mode %#o", tampered)
		}
	}
}
