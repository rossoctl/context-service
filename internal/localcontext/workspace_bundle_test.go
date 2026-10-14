package localcontext

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// goldenRevision is the digest the revision code produced for
// writeGoldenContext before workspace/ and executable modes were added. It
// must not change: existing contexts keep their published revisions.
const goldenRevision = "6b6135b67d9dacd778973c452a6e8aaf64fa938a1ab69ceb606be2ff7de8bab0"

func writeGoldenContext(t *testing.T, store *Store) {
	t.Helper()
	manifest, err := store.Create("golden", "state")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]os.FileMode{
		"harnesses/codex/project/session-1.jsonl": 0o600,
		"memory/MEMORY.md":                        0o644,
		"artifacts/reports/incident.txt":          0o600,
		"knowledge/sources/notes.txt":             0o640,
	}
	for name, mode := range files {
		target := filepath.Join(manifest.Path, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte("content of "+name+"\n"), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(target, mode); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRevisionWithoutWorkspaceOrExecutablesIsUnchanged(t *testing.T) {
	store := New(t.TempDir())
	writeGoldenContext(t, store)
	revision, err := store.Revision("golden")
	if err != nil {
		t.Fatal(err)
	}
	if revision.Digest != goldenRevision || revision.Files != 4 {
		t.Fatalf("revision = %+v, want digest %s and 4 files", revision, goldenRevision)
	}
}

func writeWorkspaceFixture(t *testing.T) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "demo-repo")
	for name, mode := range map[string]os.FileMode{
		"src/greet.py": 0o644,
		"test.sh":      0o755,
		"README.md":    0o600,
		".git/HEAD":    0o644,
		"lib/.git":     0o644,
	} {
		target := filepath.Join(directory, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte("content of "+name+"\n"), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(target, mode); err != nil {
			t.Fatal(err)
		}
	}
	return directory
}

func TestPackRoundTripsWorkspaceAndExecutableBit(t *testing.T) {
	directory := writeWorkspaceFixture(t)
	bundlePath := filepath.Join(t.TempDir(), "demo.context")
	result, err := Pack(directory, bundlePath, PackOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Name != "demo-repo" || result.Type != "workspace" || result.Files != 3 || result.Path != bundlePath {
		t.Fatalf("unexpected pack result: %+v", result)
	}

	modes := bundleModes(t, bundlePath)
	want := map[string]os.FileMode{
		"workspace":              portableDirMode,
		"workspace/src":          portableDirMode,
		"workspace/src/greet.py": portableFileMode,
		"workspace/README.md":    portableFileMode,
		"workspace/test.sh":      0o755,
		"manifest.json":          portableFileMode,
		"checksums.json":         portableFileMode,
	}
	for name, mode := range want {
		if got, ok := modes[name]; !ok || got != mode {
			t.Errorf("%s mode = %#o (present %v), want %#o", name, got, ok, mode)
		}
	}
	for name := range modes {
		if strings.Contains(name, ".git") {
			t.Errorf("bundle contains %s", name)
		}
	}
	if len(modes) != len(want) {
		t.Errorf("bundle entries = %v", modes)
	}

	local := New(filepath.Join(t.TempDir(), "local"))
	manifest, err := local.Import(bundlePath, "")
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Name != "demo-repo" || manifest.Type != "workspace" {
		t.Fatalf("imported manifest = %+v", manifest)
	}
	assertMode(t, filepath.Join(manifest.Path, "workspace", "test.sh"), 0o700)
	assertMode(t, filepath.Join(manifest.Path, "workspace", "src", "greet.py"), localFileMode)
	assertFileContains(t, filepath.Join(manifest.Path, "workspace", "src", "greet.py"), "content of src/greet.py")
	revision, err := local.Revision("demo-repo")
	if err != nil {
		t.Fatal(err)
	}
	if revision.Digest != result.Revision || revision.Files != result.Files || revision.Bytes != result.Bytes {
		t.Fatalf("local revision = %+v, pack result = %+v", revision, result)
	}

	input, err := os.Open(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	portable := New(filepath.Join(t.TempDir(), "portable"))
	shared, err := portable.ImportPortableReaderWithLimits(input, "shared", defaultImportLimits)
	if err != nil {
		t.Fatal(err)
	}
	assertMode(t, filepath.Join(shared.Path, "workspace", "test.sh"), 0o755)
	assertMode(t, filepath.Join(shared.Path, "workspace", "src", "greet.py"), portableFileMode)
	sharedRevision, err := portable.Revision("shared")
	if err != nil {
		t.Fatal(err)
	}
	if sharedRevision.Digest != result.Revision {
		t.Fatalf("portable revision = %s, pack revision = %s", sharedRevision.Digest, result.Revision)
	}

	// Exporting the imported context reproduces the same modes.
	exported := filepath.Join(t.TempDir(), "exported.context")
	if _, err := local.Export("demo-repo", exported); err != nil {
		t.Fatal(err)
	}
	if got := bundleModes(t, exported)["workspace/test.sh"]; got != 0o755 {
		t.Fatalf("re-exported test.sh mode = %#o", got)
	}
}

func TestPackIncludeGitKeepsGitEntries(t *testing.T) {
	directory := writeWorkspaceFixture(t)
	bundlePath := filepath.Join(t.TempDir(), "demo.context")
	if _, err := Pack(directory, bundlePath, PackOptions{IncludeGit: true}); err != nil {
		t.Fatal(err)
	}
	modes := bundleModes(t, bundlePath)
	for _, name := range []string{"workspace/.git/HEAD", "workspace/lib/.git"} {
		if _, ok := modes[name]; !ok {
			t.Errorf("bundle omitted %s", name)
		}
	}
}

func TestPackRejectsUnsupportedInput(t *testing.T) {
	directory := writeWorkspaceFixture(t)
	output := t.TempDir()
	if _, err := Pack(directory, filepath.Join(output, "state.context"), PackOptions{Type: "state"}); err == nil || !strings.Contains(err.Error(), "only workspace") {
		t.Fatalf("pack --type state error = %v", err)
	}
	if _, err := Pack(directory, filepath.Join(output, "bad.context"), PackOptions{Name: "bad name"}); err == nil || !strings.Contains(err.Error(), "invalid context name") {
		t.Fatalf("invalid name error = %v", err)
	}
	if _, err := Pack(filepath.Join(directory, "test.sh"), filepath.Join(output, "file.context"), PackOptions{Name: "file"}); err == nil {
		t.Fatal("packing a regular file succeeded")
	}
	existing := filepath.Join(output, "existing.context")
	writeFixture(t, existing, "keep")
	if _, err := Pack(directory, existing, PackOptions{}); err == nil {
		t.Fatal("pack overwrote an existing bundle")
	}
	assertFileContains(t, existing, "keep")

	if err := os.Symlink("test.sh", filepath.Join(directory, "link.sh")); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(output, "linked.context")
	if _, err := Pack(directory, linked, PackOptions{}); err == nil || !strings.Contains(err.Error(), "non-regular") {
		t.Fatalf("symlink pack error = %v", err)
	}
	if _, err := os.Stat(linked); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed pack left a bundle: %v", err)
	}
}

func TestPackEnforcesUploadLimits(t *testing.T) {
	directory := writeWorkspaceFixture(t)
	output := t.TempDir()
	for name, limits := range map[string]PackLimits{
		"entries": {MaxEntries: 6},
		"file":    {MaxFileBytes: 10},
		"total":   {MaxTotalBytes: 40},
		"archive": {MaxArchiveBytes: 64},
	} {
		bundlePath := filepath.Join(output, name+".context")
		if _, err := Pack(directory, bundlePath, PackOptions{Limits: limits}); err == nil || !strings.Contains(err.Error(), "limit") {
			t.Errorf("%s limit: error = %v", name, err)
		}
		if _, err := os.Stat(bundlePath); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s limit left a bundle: %v", name, err)
		}
	}
	// The fixture packs to exactly seven entries: workspace/, workspace/src/,
	// three files and the two metadata files (lib/ holds only .git).
	if _, err := Pack(directory, filepath.Join(output, "fits.context"), PackOptions{Limits: PackLimits{MaxEntries: 7}}); err != nil {
		t.Fatalf("pack at the entry limit failed: %v", err)
	}
}

func TestRevisionCoversOnlyTheOwnerExecutableBit(t *testing.T) {
	store := New(t.TempDir())
	manifest, err := store.Create("modes", "workspace")
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(manifest.Path, "workspace", "run.sh")
	if err := os.MkdirAll(filepath.Dir(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest := func(mode os.FileMode) string {
		t.Helper()
		if err := os.Chmod(script, mode); err != nil {
			t.Fatal(err)
		}
		revision, err := store.Revision("modes")
		if err != nil {
			t.Fatal(err)
		}
		return revision.Digest
	}
	plain := digest(0o600)
	if digest(0o644) != plain || digest(0o664) != plain {
		t.Fatal("read or write bits changed the revision")
	}
	executable := digest(0o700)
	if executable == plain {
		t.Fatal("owner-executable bit did not change the revision")
	}
	if digest(0o755) != executable {
		t.Fatal("group or world bits changed an executable revision")
	}
}

func TestImportNeverAppliesSpecialOrWritableBits(t *testing.T) {
	directory := writeWorkspaceFixture(t)
	root := t.TempDir()
	packed := filepath.Join(root, "packed.context")
	if _, err := Pack(directory, packed, PackOptions{}); err != nil {
		t.Fatal(err)
	}
	hostile := filepath.Join(root, "hostile.context")
	rewriteBundleModes(t, packed, hostile, func(header *tar.Header) {
		switch header.Name {
		case "workspace/test.sh":
			header.Mode = 0o7777
		case "workspace/src/greet.py":
			header.Mode = 0o666
		case "workspace/src":
			header.Mode = 0o1777
		}
	})
	input, err := os.Open(hostile)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	manifest, err := New(filepath.Join(root, "store")).ImportPortableReaderWithLimits(input, "hostile", defaultImportLimits)
	if err != nil {
		t.Fatal(err)
	}
	assertMode(t, filepath.Join(manifest.Path, "workspace", "test.sh"), 0o755)
	assertMode(t, filepath.Join(manifest.Path, "workspace", "src", "greet.py"), 0o644)
	assertMode(t, filepath.Join(manifest.Path, "workspace", "src"), 0o755)
}

func assertMode(t *testing.T, filePath string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky); got != want {
		t.Errorf("%s mode = %v, want %v", filePath, got, want)
	}
}

func rewriteBundleModes(t *testing.T, source, destination string, change func(*tar.Header)) {
	t.Helper()
	input, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	gzipReader, err := gzip.NewReader(input)
	if err != nil {
		t.Fatal(err)
	}
	output, err := os.Create(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	gzipWriter := gzip.NewWriter(output)
	tarWriter := tar.NewWriter(gzipWriter)
	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		change(header)
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(tarWriter, tarReader); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
}
