package localcontext

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSnapshotCloneRestoreAndGarbageCollection(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	clock := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return clock }
	if _, err := store.Create("demo", "state"); err != nil {
		t.Fatal(err)
	}
	writePortableFile(t, root, "demo", "harnesses/claude/project/session.jsonl", "first")
	first, err := store.CreateSnapshot("demo", "first", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Hour)
	writePortableFile(t, root, "demo", "harnesses/claude/project/session.jsonl", "second")
	second, err := store.CreateSnapshot("demo", "second", false, map[string]string{"protected": "true"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision == second.Revision {
		t.Fatal("changed content produced the same revision")
	}

	clone, err := store.CloneSnapshot("demo", "first", "copy")
	if err != nil {
		t.Fatal(err)
	}
	if clone.Derivation == nil || clone.Derivation.SourceContext != "demo" || clone.Derivation.SourceRevision != first.Revision {
		t.Fatalf("clone provenance = %#v", clone.Derivation)
	}
	assertPortableFile(t, root, "copy", "first")

	clock = clock.Add(time.Hour)
	restored, safety, err := store.RestoreSnapshot("demo", "first")
	if err != nil {
		t.Fatal(err)
	}
	if safety == nil || !safety.Protected {
		t.Fatalf("restore safety snapshot = %#v", safety)
	}
	if restored.Revisions[len(restored.Revisions)-1].Operation != "restore" {
		t.Fatalf("last revision = %#v", restored.Revisions[len(restored.Revisions)-1])
	}
	assertPortableFile(t, root, "demo", "first")

	if _, err := store.SetRetention("demo", RetentionPolicy{KeepLast: 1}); err != nil {
		t.Fatal(err)
	}
	preview, err := store.GarbageCollect("demo", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Deleted) != 0 {
		// The first snapshot is referenced by the clone and the remaining two
		// are protected/newest, so safe GC must keep all three.
		t.Fatalf("unsafe candidates: %#v", preview.Deleted)
	}
}

func TestGarbageCollectionDeletesOnlyExpiredUnreferencedSnapshots(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	clock := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return clock }
	if _, err := store.Create("demo", "state"); err != nil {
		t.Fatal(err)
	}
	writePortableFile(t, root, "demo", "memory/value.txt", "one")
	old, err := store.CreateSnapshot("demo", "old", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(48 * time.Hour)
	writePortableFile(t, root, "demo", "memory/value.txt", "two")
	if _, err := store.CreateSnapshot("demo", "new", false, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetRetention("demo", RetentionPolicy{KeepLast: 1, MaxAge: 24 * time.Hour}); err != nil {
		t.Fatal(err)
	}
	result, err := store.GarbageCollect("demo", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Deleted) != 1 || result.Deleted[0].Name != "old" {
		t.Fatalf("deleted = %#v", result.Deleted)
	}
	if _, err := os.Stat(store.snapshotObjectPath("demo", old.Revision)); !os.IsNotExist(err) {
		t.Fatalf("old object still exists: %v", err)
	}
}

func writePortableFile(t *testing.T, root, name, relative, value string) {
	t.Helper()
	path := filepath.Join(root, name, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertPortableFile(t *testing.T, root, name, want string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, name, "harnesses", "claude", "project", "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("content = %q, want %q", data, want)
	}
}
