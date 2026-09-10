package localcontext

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExportImportBundleRoundTrip(t *testing.T) {
	root := t.TempDir()
	sourceStore := New(filepath.Join(root, "source"))
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := sourceStore.Create("demo", "state"); err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(root, "session.jsonl")
	writeFixture(t, session, `{"type":"user","message":"violet telescope"}`+"\n")
	if _, _, err := sourceStore.CaptureSessionFile("demo", "codex", project, "session-1", session); err != nil {
		t.Fatal(err)
	}
	if _, err := sourceStore.AttachPi("demo", project, "/bin/contextctl"); err != nil {
		t.Fatal(err)
	}

	bundlePath := filepath.Join(root, "demo.context")
	bundle, err := sourceStore.Export("demo", bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.FormatVersion != 1 || bundle.Files != 2 || bundle.Bytes == 0 {
		t.Fatalf("unexpected bundle: %+v", bundle)
	}

	destinationStore := New(filepath.Join(root, "destination"))
	manifest, err := destinationStore.Import(bundlePath, "restored")
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Name != "restored" || manifest.Type != "state" {
		t.Fatalf("unexpected imported manifest: %+v", manifest)
	}
	if len(manifest.Attachments) != 0 {
		t.Fatalf("machine-local attachments were imported: %+v", manifest.Attachments)
	}
	files, err := destinationStore.SessionFiles("restored", "codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("imported session count = %d, want 1", len(files))
	}
	assertFileContains(t, files[0], "violet telescope")
	if _, err := destinationStore.Import(bundlePath, "restored"); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("second import error = %v, want ErrAlreadyExists", err)
	}
}

func TestExportRefusesToOverwriteBundle(t *testing.T) {
	root := t.TempDir()
	store := New(filepath.Join(root, "contexts"))
	if _, err := store.Create("demo", "state"); err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(root, "demo.context")
	writeFixture(t, bundlePath, "keep")
	if _, err := store.Export("demo", bundlePath); err == nil {
		t.Fatal("export overwrote an existing bundle")
	}
	assertFileContains(t, bundlePath, "keep")
}

func TestExportRefusesToOverwriteDanglingSymlink(t *testing.T) {
	root := t.TempDir()
	store := New(filepath.Join(root, "contexts"))
	if _, err := store.Create("demo", "state"); err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(root, "demo.context")
	if err := os.Symlink(filepath.Join(root, "missing"), bundlePath); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Export("demo", bundlePath); err == nil {
		t.Fatal("export replaced a dangling symlink")
	}
	if info, err := os.Lstat(bundlePath); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("output symlink was changed: info=%v err=%v", info, err)
	}
}

func TestExportIsDeterministic(t *testing.T) {
	root := t.TempDir()
	store := New(filepath.Join(root, "contexts"))
	if _, err := store.Create("demo", "state"); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(root, "first.context")
	second := filepath.Join(root, "second.context")
	if _, err := store.Export("demo", first); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Export("demo", second); err != nil {
		t.Fatal(err)
	}
	firstData, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	secondData, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstData, secondData) {
		t.Fatal("unchanged context produced different bundle content")
	}
}

func TestImportRejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	bundlePath := filepath.Join(root, "unsafe.context")
	writeTestBundle(t, bundlePath, map[string]string{"../outside": "bad"})
	store := New(filepath.Join(root, "contexts"))
	if _, err := store.Import(bundlePath, ""); err == nil || !strings.Contains(err.Error(), "unsafe context bundle path") {
		t.Fatalf("import error = %v, want unsafe path error", err)
	}
	if _, err := os.Stat(filepath.Join(root, "outside")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("path traversal created a file: %v", err)
	}
}

func TestImportRejectsChecksumMismatch(t *testing.T) {
	root := t.TempDir()
	bundlePath := filepath.Join(root, "bad-checksum.context")
	writeTestBundle(t, bundlePath, map[string]string{
		"manifest.json":  `{"version":1,"name":"demo","type":"state","backend":"filesystem"}`,
		"checksums.json": `{"formatVersion":1,"algorithm":"sha256","files":{"manifest.json":"wrong"}}`,
	})
	store := New(filepath.Join(root, "contexts"))
	if _, err := store.Import(bundlePath, ""); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("import error = %v, want checksum error", err)
	}
}

func TestImportRefusesToReplaceDanglingContextSymlink(t *testing.T) {
	root := t.TempDir()
	sourceStore := New(filepath.Join(root, "source"))
	if _, err := sourceStore.Create("demo", "state"); err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(root, "demo.context")
	if _, err := sourceStore.Export("demo", bundlePath); err != nil {
		t.Fatal(err)
	}
	destinationRoot := filepath.Join(root, "destination")
	if err := os.MkdirAll(destinationRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(destinationRoot, "demo")
	if err := os.Symlink(filepath.Join(root, "missing"), destination); err != nil {
		t.Fatal(err)
	}
	if _, err := New(destinationRoot).Import(bundlePath, ""); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("import error = %v, want ErrAlreadyExists", err)
	}
	if info, err := os.Lstat(destination); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("context symlink was changed: info=%v err=%v", info, err)
	}
}

func writeTestBundle(t *testing.T, bundlePath string, files map[string]string) {
	t.Helper()
	output, err := os.Create(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(output)
	tarWriter := tar.NewWriter(gzipWriter)
	for name, content := range files {
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
}
