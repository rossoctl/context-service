package localcontext

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPublishListAndRetrieveImmutableArtifacts(t *testing.T) {
	root := t.TempDir()
	store := New(filepath.Join(root, "contexts"))
	source, err := store.Create("workspace", "workspace")
	if err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(root, "report.txt")
	if err := os.WriteFile(input, []byte("first report"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, manifest, err := store.PublishArtifacts("outputs", input, source.Name, "agent:test", "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || manifest.Type != "artifacts" || first[0].Source.Context != "workspace" || first[0].Source.Revision == "" {
		t.Fatalf("first publication = %+v, manifest = %+v", first, manifest)
	}
	if _, _, err := store.PublishArtifacts("outputs", input, source.Name, "agent:test", "text/plain"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, []byte("second report"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, _, err := store.PublishArtifacts("outputs", input, source.Name, "agent:test", "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	items, err := store.ListArtifacts("outputs")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || first[0].Version == second[0].Version {
		t.Fatalf("artifact versions = %+v", items)
	}
	output := filepath.Join(root, "retrieved.txt")
	selected, path, err := store.GetArtifact("outputs", "report.txt", first[0].Version[:12], output)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if selected.Version != first[0].Version || string(data) != "first report" {
		t.Fatalf("retrieved %q as %+v", data, selected)
	}
}

func TestPublishArtifactDirectoryRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	store := New(filepath.Join(root, "contexts"))
	if _, err := store.Create("workspace", "state"); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "output")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", filepath.Join(directory, "link")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.PublishArtifacts("outputs", directory, "workspace", "agent:test", ""); err == nil {
		t.Fatal("expected symlink rejection")
	}
}
