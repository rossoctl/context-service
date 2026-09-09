package localcontext

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexAttachAndDetachPreserveUnrelatedHooks(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	hooksPath := filepath.Join(project, ".codex", "hooks.json")
	writeFixture(t, hooksPath, `{"description":"keep","hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"keep-me"}]}]}}`)
	store := New(filepath.Join(root, "contexts"))
	if _, err := store.Create("demo", "state"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AttachCodex("demo", project, "/usr/local/bin/contextctl"); err != nil {
		t.Fatal(err)
	}
	settings := readSettingsFixture(t, hooksPath)
	if countCodexHooks(settings, "demo") != 2 {
		t.Fatalf("managed Codex hook count = %d, want 2", countCodexHooks(settings, "demo"))
	}
	encoded, _ := json.Marshal(settings)
	for _, expected := range []string{"keep-me", "codex-capture", `"timeout":3`} {
		if !strings.Contains(string(encoded), expected) {
			t.Errorf("Codex hooks missing %q: %s", expected, encoded)
		}
	}
	if _, err := store.DetachCodex("demo", ""); err != nil {
		t.Fatal(err)
	}
	settings = readSettingsFixture(t, hooksPath)
	if countCodexHooks(settings, "demo") != 0 {
		t.Fatal("managed Codex hook remains after detach")
	}
	encoded, _ = json.Marshal(settings)
	if !strings.Contains(string(encoded), "keep-me") {
		t.Fatalf("detach removed unrelated hook: %s", encoded)
	}
}

func TestOpenCodeAndPiManagedIntegrations(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	store := New(filepath.Join(root, "contexts"))
	if _, err := store.Create("demo", "state"); err != nil {
		t.Fatal(err)
	}

	if _, err := store.AttachOpenCode("demo", project, "/bin/contextctl", "/bin/opencode"); err != nil {
		t.Fatal(err)
	}
	openCodePath := filepath.Join(project, ".opencode", "plugins", "context-service.ts")
	assertFileContains(t, openCodePath, opencodeFileMarker, "session.idle", "opencode-capture")
	if _, err := store.DetachOpenCode("demo", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(openCodePath); !os.IsNotExist(err) {
		t.Fatalf("OpenCode integration remains after detach: %v", err)
	}

	if _, err := store.AttachPi("demo", project, "/bin/contextctl"); err != nil {
		t.Fatal(err)
	}
	piPath := filepath.Join(project, ".pi", "extensions", "context-service.ts")
	assertFileContains(t, piPath, piFileMarker, "agent_settled", "session_shutdown", "pi-capture")
	if _, err := store.DetachPi("demo", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(piPath); !os.IsNotExist(err) {
		t.Fatalf("Pi integration remains after detach: %v", err)
	}
}

func TestCodexHookCapturesNativeTranscript(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(root, "rollout-session-123.jsonl")
	writeFixture(t, transcript, `{"type":"session_meta"}`+"\n")
	store := New(filepath.Join(root, "contexts"))
	if _, err := store.Create("demo", "state"); err != nil {
		t.Fatal(err)
	}
	input := `{"session_id":"session-123","transcript_path":"` + transcript + `","cwd":"` + project + `","hook_event_name":"SessionEnd","reason":"other"}`
	capture, destination, err := store.CaptureCodexHook("demo", project, strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if capture.Harness != "codex" || capture.Sessions != 1 {
		t.Fatalf("unexpected capture: %+v", capture)
	}
	assertFileContains(t, destination, "session_meta")
}

func TestSessionExportRejectsInvalidJSON(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	store := New(filepath.Join(root, "contexts"))
	if _, err := store.Create("demo", "state"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CaptureSessionExport("demo", "opencode", project, "session-1", strings.NewReader("not json")); err == nil {
		t.Fatal("invalid export succeeded")
	}
}

func TestStateTypeAcceptsLegacyHistoryManifests(t *testing.T) {
	if !isStateType("state") || !isStateType("history") || isStateType("workspace") {
		t.Fatal("unexpected state type compatibility")
	}
}

func countCodexHooks(settings map[string]any, contextName string) int {
	hooks, _ := settings["hooks"].(map[string]any)
	count := 0
	for _, event := range []string{"Stop", "SessionEnd"} {
		groups, _ := hooks[event].([]any)
		for _, rawGroup := range groups {
			group, _ := rawGroup.(map[string]any)
			handlers, _ := group["hooks"].([]any)
			for _, rawHandler := range handlers {
				handler, _ := rawHandler.(map[string]any)
				marker, _ := handler["statusMessage"].(string)
				if marker == codexHookMarker+contextName {
					count++
				}
			}
		}
	}
	return count
}

func assertFileContains(t *testing.T, path string, expected ...string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range expected {
		if !strings.Contains(string(data), value) {
			t.Errorf("%s missing %q:\n%s", path, value, data)
		}
	}
}
