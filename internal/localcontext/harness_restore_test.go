package localcontext

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const codexThreadID = "019a2b3c-4d5e-7f60-8a9b-0c1d2e3f4a5b"

func codexRollout(id, message string) string {
	return `{"timestamp":"2026-03-04T05:06:07.890Z","type":"session_meta","payload":{"id":"` + id +
		`","timestamp":"2026-03-04T05:06:07.890Z","cwd":"/work/source","originator":"codex_cli_rs"}}` + "\n" +
		`{"timestamp":"2026-03-04T05:06:09.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"` + message + `"}]}}` + "\n"
}

const piSessionName = "2026-03-04T05-06-07-890Z_6f1e2d3c-4b5a-4968-8776-655443322110"

func piSession(message string) string {
	return `{"type":"session","version":3,"id":"6f1e2d3c-4b5a-4968-8776-655443322110","timestamp":"2026-03-04T05:06:07.890Z","cwd":"/work/source"}` + "\n" +
		`{"type":"message","id":"a1","parentId":null,"message":{"role":"user","content":"` + message + `"}}` + "\n"
}

// roundTripStore captures a session in one store, exports a bundle, and
// imports it into a fresh store, as a restore on another machine would.
func roundTripStore(t *testing.T, root, harness, sessionID, sessionPath string) (*Store, string) {
	t.Helper()
	project := filepath.Join(root, "source-project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	source := New(filepath.Join(root, "source-contexts"))
	if _, err := source.Create("demo", "state"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := source.CaptureSessionFile("demo", harness, project, sessionID, sessionPath); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(root, "demo.context")
	if _, err := source.Export("demo", bundle); err != nil {
		t.Fatal(err)
	}
	destination := New(filepath.Join(root, "destination-contexts"))
	if _, err := destination.Import(bundle, "demo"); err != nil {
		t.Fatal(err)
	}
	destinationProject := filepath.Join(root, "destination-project")
	if err := os.MkdirAll(destinationProject, 0o700); err != nil {
		t.Fatal(err)
	}
	return destination, destinationProject
}

func TestCodexRestoreRoundTrip(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, "source-codex", "sessions", "2026", "03", "03", "rollout-2026-03-03T21-06-07-"+codexThreadID+".jsonl")
	content := codexRollout(codexThreadID, "violet telescope")
	writeFixture(t, original, content)
	store, project := roundTripStore(t, root, "codex", codexThreadID, original)

	codexHome := filepath.Join(root, "fresh-codex")
	result, err := store.RestoreCodex("demo", project, codexHome)
	if err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(codexHome, "sessions", "2026", "03", "04", "rollout-2026-03-04T05-06-07-"+codexThreadID+".jsonl")
	assertFileContent(t, restored, content)
	if len(result.Restored) != 1 || result.Destination != filepath.Join(codexHome, "sessions") {
		t.Fatalf("unexpected result: %+v", result)
	}

	again, err := store.RestoreCodex("demo", project, codexHome)
	if err != nil {
		t.Fatalf("identical restore: %v", err)
	}
	if len(again.Restored) != 0 || len(again.Unchanged) != 1 {
		t.Fatalf("identical restore result = %+v, want one unchanged", again)
	}
}

func TestCodexRestoreRefusesDifferentLocalSession(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, "rollout.jsonl")
	writeFixture(t, original, codexRollout(codexThreadID, "captured"))
	store, project := roundTripStore(t, root, "codex", codexThreadID, original)

	codexHome := filepath.Join(root, "codex")
	// Codex names the file with local time, so the local copy can sit under
	// another date directory and still be the same thread.
	local := filepath.Join(codexHome, "sessions", "2026", "03", "03", "rollout-2026-03-03T21-06-07-"+codexThreadID+".jsonl")
	newer := codexRollout(codexThreadID, "captured") + `{"type":"response_item","payload":{"text":"newer local turn"}}` + "\n"
	writeFixture(t, local, newer)
	if _, err := store.RestoreCodex("demo", project, codexHome); err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("restore error = %v, want refusal", err)
	}
	assertFileContent(t, local, newer)
	if _, err := os.Stat(filepath.Join(codexHome, "sessions", "2026", "03", "04")); !os.IsNotExist(err) {
		t.Fatalf("restore wrote a second copy despite refusal: %v", err)
	}
}

func TestCodexRestoreRejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, "rollout.jsonl")
	writeFixture(t, original, codexRollout("../../../escaped", "hostile"))
	store, project := roundTripStore(t, root, "codex", "hostile", original)

	codexHome := filepath.Join(root, "codex")
	if _, err := store.RestoreCodex("demo", project, codexHome); err == nil || !strings.Contains(err.Error(), "invalid Codex thread ID") {
		t.Fatalf("restore error = %v, want invalid thread ID", err)
	}
	assertNoFiles(t, root, "escaped")
}

func TestCodexRestoreRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	root := t.TempDir()
	original := filepath.Join(root, "rollout.jsonl")
	writeFixture(t, original, codexRollout(codexThreadID, "captured"))
	store, project := roundTripStore(t, root, "codex", codexThreadID, original)

	codexHome := filepath.Join(root, "codex")
	outside := filepath.Join(root, "outside")
	for _, dir := range []string{outside, filepath.Join(codexHome, "sessions")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// A planted year directory link must not redirect the write out of
	// CODEX_HOME/sessions.
	if err := os.Symlink(outside, filepath.Join(codexHome, "sessions", "2026")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RestoreCodex("demo", project, codexHome); err == nil || !strings.Contains(err.Error(), "refusing to write outside") {
		t.Fatalf("restore error = %v, want confinement refusal", err)
	}
	assertNoFiles(t, outside, "")
}

func TestPiRestoreRoundTrip(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, "source-pi", "sessions", "--work-source--", piSessionName+".jsonl")
	content := piSession("violet telescope")
	writeFixture(t, original, content)
	store, project := roundTripStore(t, root, "pi", "", original)

	agentDir := filepath.Join(root, "fresh-pi", "agent")
	result, err := store.RestorePi("demo", project, agentDir, "")
	if err != nil {
		t.Fatal(err)
	}
	canonical, _ := canonicalProject(project)
	sessionDir := filepath.Join(agentDir, "sessions", PiProjectKey(canonical))
	assertFileContent(t, filepath.Join(sessionDir, piSessionName+".jsonl"), content)
	if result.Destination != sessionDir || len(result.Restored) != 1 || result.Restored[0] != piSessionName {
		t.Fatalf("unexpected result: %+v", result)
	}

	flat := filepath.Join(root, "custom-sessions")
	if _, err := store.RestorePi("demo", project, agentDir, flat); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, filepath.Join(flat, piSessionName+".jsonl"), content)
}

func TestPiRestoreRefusesDifferentLocalSession(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, piSessionName+".jsonl")
	writeFixture(t, original, piSession("captured"))
	store, project := roundTripStore(t, root, "pi", "", original)

	agentDir := filepath.Join(root, "pi")
	canonical, _ := canonicalProject(project)
	local := filepath.Join(agentDir, "sessions", PiProjectKey(canonical), piSessionName+".jsonl")
	newer := piSession("captured") + `{"type":"message","id":"a2","message":{"role":"assistant","content":"newer"}}` + "\n"
	writeFixture(t, local, newer)
	if _, err := store.RestorePi("demo", project, agentDir, ""); err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("restore error = %v, want refusal", err)
	}
	assertFileContent(t, local, newer)
}

func TestPiRestoreRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	root := t.TempDir()
	original := filepath.Join(root, piSessionName+".jsonl")
	writeFixture(t, original, piSession("captured"))
	store, project := roundTripStore(t, root, "pi", "", original)

	// A planted project session directory link must not redirect the write.
	agentDir := filepath.Join(root, "pi")
	canonical, _ := canonicalProject(project)
	outside := filepath.Join(root, "outside")
	for _, dir := range []string{outside, filepath.Join(agentDir, "sessions")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(agentDir, "sessions", PiProjectKey(canonical))); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RestorePi("demo", project, agentDir, ""); err == nil || !strings.Contains(err.Error(), "refusing to write outside") {
		t.Fatalf("restore error = %v, want confinement refusal", err)
	}
	assertNoFiles(t, outside, "")

	// A captured session that is itself a symlink is rejected before restore.
	captured, err := store.SessionFiles("demo", "pi")
	if err != nil || len(captured) != 1 {
		t.Fatalf("captured files = %v, %v", captured, err)
	}
	if err := os.Remove(captured[0]); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hosts", captured[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RestorePi("demo", project, filepath.Join(root, "fresh-pi"), ""); err == nil || !strings.Contains(err.Error(), "non-regular") {
		t.Fatalf("restore error = %v, want non-regular rejection", err)
	}
}

func TestOpenCodeRestoreRoundTrip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake opencode is a shell script")
	}
	root := t.TempDir()
	fake, database := fakeOpenCode(t, root)
	project := filepath.Join(root, "source-project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	source := New(filepath.Join(root, "source-contexts"))
	if _, err := source.Create("demo", "state"); err != nil {
		t.Fatal(err)
	}
	export := `{"info":{"id":"ses_violet","title":"violet telescope","directory":"/work/source"},"messages":[]}`
	if _, _, err := source.CaptureSessionExport("demo", "opencode", project, "ses_violet", strings.NewReader(export)); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(root, "demo.context")
	if _, err := source.Export("demo", bundle); err != nil {
		t.Fatal(err)
	}
	store := New(filepath.Join(root, "destination-contexts"))
	if _, err := store.Import(bundle, "demo"); err != nil {
		t.Fatal(err)
	}
	destinationProject := filepath.Join(root, "destination-project")
	if err := os.MkdirAll(destinationProject, 0o700); err != nil {
		t.Fatal(err)
	}

	result, err := store.RestoreOpenCode("demo", destinationProject, fake)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Restored) != 1 || result.Restored[0] != "ses_violet" {
		t.Fatalf("unexpected result: %+v", result)
	}
	assertFileContent(t, filepath.Join(database, "ses_violet.json"), export)
	canonical, _ := canonicalProject(destinationProject)
	assertFileContent(t, filepath.Join(database, "ses_violet.dir"), canonical+"\n")

	// The session now exists locally, so a second restore must refuse.
	if _, err := store.RestoreOpenCode("demo", destinationProject, fake); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second restore error = %v, want refusal", err)
	}
}

func TestOpenCodeRestoreRejectsHostileSessionID(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake opencode is a shell script")
	}
	root := t.TempDir()
	fake, database := fakeOpenCode(t, root)
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	store := New(filepath.Join(root, "contexts"))
	if _, err := store.Create("demo", "state"); err != nil {
		t.Fatal(err)
	}
	export := `{"info":{"id":"../../escaped"},"messages":[]}`
	if _, _, err := store.CaptureSessionExport("demo", "opencode", project, "hostile", strings.NewReader(export)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RestoreOpenCode("demo", project, fake); err == nil || !strings.Contains(err.Error(), "invalid OpenCode session ID") {
		t.Fatalf("restore error = %v, want invalid session ID", err)
	}
	assertNoFiles(t, database, "")
}

func TestRestoreRequiresCapturedHarness(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	store := New(filepath.Join(root, "contexts"))
	if _, err := store.Create("demo", "state"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RestoreCodex("demo", project, filepath.Join(root, "codex")); err == nil || !strings.Contains(err.Error(), "no Codex state") {
		t.Fatalf("Codex restore error = %v", err)
	}
	if _, err := store.RestorePi("demo", project, filepath.Join(root, "pi"), ""); err == nil || !strings.Contains(err.Error(), "no Pi state") {
		t.Fatalf("Pi restore error = %v", err)
	}
	if _, err := store.RestoreOpenCode("demo", project, "/bin/false"); err == nil || !strings.Contains(err.Error(), "no OpenCode state") {
		t.Fatalf("OpenCode restore error = %v", err)
	}
}

func TestPiProjectKeyMatchesPi(t *testing.T) {
	if got := PiProjectKey("/Users/me/work/app"); got != "--Users-me-work-app--" {
		t.Fatalf("PiProjectKey = %q", got)
	}
}

// fakeOpenCode writes a stand-in for the opencode CLI that keeps imported
// sessions as files, reporting "Session not found" for unknown exports.
func fakeOpenCode(t *testing.T, root string) (string, string) {
	t.Helper()
	database := filepath.Join(root, "opencode-db")
	if err := os.MkdirAll(database, 0o700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "bin", "opencode")
	writeFixture(t, script, `#!/bin/sh
db='`+database+`'
case "$1" in
export)
  if [ -f "$db/$2.json" ]; then cat "$db/$2.json"; exit 0; fi
  echo "Session not found: $2" >&2; exit 1 ;;
import)
  id=$(sed -n 's/.*"info":{"id":"\([^"]*\)".*/\1/p' "$2")
  cp "$2" "$db/$id.json"; pwd -P > "$db/$id.dir"
  echo "Imported session: $id" ;;
*) exit 2 ;;
esac
`)
	if err := os.Chmod(script, 0o700); err != nil {
		t.Fatal(err)
	}
	return script, database
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", path, data, want)
	}
}

func assertNoFiles(t *testing.T, root, nameContains string) {
	t.Helper()
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && (nameContains == "" || strings.Contains(path, nameContains)) {
			t.Errorf("unexpected file %s", path)
		}
		return nil
	})
}
