package localcontext

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateGetAndList(t *testing.T) {
	store := New(t.TempDir())
	created, err := store.Create("demo", "state")
	if err != nil {
		t.Fatal(err)
	}
	if created.Name != "demo" || created.Type != "state" || created.Backend != "filesystem" {
		t.Fatalf("unexpected manifest: %+v", created)
	}
	if _, err := store.Create("demo", "state"); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("second create error = %v, want ErrAlreadyExists", err)
	}
	loaded, err := store.Get("demo")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Path != created.Path {
		t.Fatalf("path = %q, want %q", loaded.Path, created.Path)
	}
	items, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Name != "demo" {
		t.Fatalf("unexpected contexts: %+v", items)
	}
}

func TestCaptureAndRestoreClaudeHistory(t *testing.T) {
	root := t.TempDir()
	claudeHome := filepath.Join(root, "claude")
	sourceProject := filepath.Join(root, "source-project")
	destinationProject := filepath.Join(root, "destination-project")
	for _, dir := range []string{sourceProject, destinationProject} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	sourceHistory := filepath.Join(claudeHome, "projects", ClaudeProjectKey(sourceProject))
	writeFixture(t, filepath.Join(sourceHistory, "session-1.jsonl"), `{"type":"user","cwd":"`+sourceProject+`","sessionId":"session-1"}`+"\n")
	writeFixture(t, filepath.Join(sourceHistory, "session-1", "tool-results", "result.txt"), "observable output\n")
	writeFixture(t, filepath.Join(sourceHistory, "memory", "MEMORY.md"), "remember this\n")

	store := New(filepath.Join(root, "contexts"))
	if _, err := store.Create("demo", "state"); err != nil {
		t.Fatal(err)
	}
	capture, err := store.CaptureClaude("demo", sourceProject, claudeHome)
	if err != nil {
		t.Fatal(err)
	}
	if capture.Sessions != 1 || capture.Files != 3 || capture.Bytes == 0 {
		t.Fatalf("unexpected capture: %+v", capture)
	}

	_, restoredPath, err := store.RestoreClaude("demo", destinationProject, claudeHome)
	if err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{
		"session-1.jsonl",
		filepath.Join("session-1", "tool-results", "result.txt"),
		filepath.Join("memory", "MEMORY.md"),
	} {
		if _, err := os.Stat(filepath.Join(restoredPath, relative)); err != nil {
			t.Errorf("restored %s: %v", relative, err)
		}
	}
	if _, _, err := store.RestoreClaude("demo", destinationProject, claudeHome); err == nil {
		t.Fatal("second restore succeeded; want overwrite refusal")
	}
}

func TestCaptureDiscoversProjectFromTranscript(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project-with-hyphens")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	claudeHome := filepath.Join(root, "claude")
	history := filepath.Join(claudeHome, "projects", "an-unexpected-key")
	writeFixture(t, filepath.Join(history, "session.jsonl"), `{"type":"user","cwd":"`+project+`"}`+"\n")

	store := New(filepath.Join(root, "contexts"))
	if _, err := store.Create("demo", "state"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CaptureClaude("demo", project, claudeHome); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidContextName(t *testing.T) {
	store := New(t.TempDir())
	if _, err := store.Create("../escape", "state"); err == nil {
		t.Fatal("invalid context name succeeded")
	}
}

func TestAttachClaudePreservesSettingsAndDetachRemovesOnlyManagedHook(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	settingsPath := filepath.Join(project, ".claude", "settings.local.json")
	writeFixture(t, settingsPath, `{
  "permissions": {"allow": ["Read"]},
  "hooks": {
    "SessionEnd": [{"matcher": "logout", "hooks": [{"type": "command", "command": "keep-me"}] }],
    "PostToolUse": [{"matcher": "Write", "hooks": [{"type": "command", "command": "also-keep-me"}] }]
  }
}`)
	store := New(filepath.Join(root, "contexts"))
	if _, err := store.Create("demo", "state"); err != nil {
		t.Fatal(err)
	}
	canonicalProject, err := filepath.EvalSymlinks(project)
	if err != nil {
		t.Fatal(err)
	}

	attachment, err := store.AttachClaude("demo", project, "/usr/local/bin/contextctl")
	if err != nil {
		t.Fatal(err)
	}
	settingsPath = filepath.Join(canonicalProject, ".claude", "settings.local.json")
	if attachment.Project != canonicalProject || attachment.SettingsPath != settingsPath {
		t.Fatalf("unexpected attachment: %+v", attachment)
	}
	// A second attach updates the same managed hook instead of adding another one.
	if _, err := store.AttachClaude("demo", project, "/opt/bin/contextctl"); err != nil {
		t.Fatal(err)
	}
	settings := readSettingsFixture(t, settingsPath)
	if countManagedHooks(settings, "demo") != 2 {
		t.Fatalf("managed hook count = %d, want 2", countManagedHooks(settings, "demo"))
	}
	encoded, _ := json.Marshal(settings)
	for _, expected := range []string{"permissions", "keep-me", "also-keep-me", "/opt/bin/contextctl"} {
		if !strings.Contains(string(encoded), expected) {
			t.Errorf("settings lost %q: %s", expected, encoded)
		}
	}
	if strings.Contains(string(encoded), `"async":true`) {
		t.Fatalf("Claude capture hook must finish synchronously: %s", encoded)
	}

	if _, err := store.DetachClaude("demo", ""); err != nil {
		t.Fatal(err)
	}
	settings = readSettingsFixture(t, settingsPath)
	if countManagedHooks(settings, "demo") != 0 {
		t.Fatal("managed hook remains after detach")
	}
	encoded, _ = json.Marshal(settings)
	for _, expected := range []string{"permissions", "keep-me", "also-keep-me"} {
		if !strings.Contains(string(encoded), expected) {
			t.Errorf("detach removed %q: %s", expected, encoded)
		}
	}
}

func TestClaudeHooksCaptureHistory(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	claudeHome := filepath.Join(root, "claude")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	canonicalProject, err := filepath.EvalSymlinks(project)
	if err != nil {
		t.Fatal(err)
	}
	history := filepath.Join(claudeHome, "projects", ClaudeProjectKey(canonicalProject))
	writeFixture(t, filepath.Join(history, "session.jsonl"), `{"type":"user","cwd":"`+project+`"}`+"\n")
	store := New(filepath.Join(root, "contexts"))
	if _, err := store.Create("demo", "state"); err != nil {
		t.Fatal(err)
	}
	input := `{"session_id":"abc123","transcript_path":"` + filepath.Join(history, "session.jsonl") + `","cwd":"` + project + `","hook_event_name":"Stop","stop_hook_active":false}`
	capture, err := store.CaptureClaudeHook("demo", project, claudeHome, strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if capture.Sessions != 1 || capture.Project != canonicalProject {
		t.Fatalf("unexpected capture: %+v", capture)
	}
	if _, err := os.Stat(filepath.Join(root, "contexts", "demo", "harnesses", "claude", "project", "session.jsonl")); err != nil {
		t.Fatalf("captured transcript: %v", err)
	}
}

func readSettingsFixture(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	return settings
}

func countManagedHooks(settings map[string]any, contextName string) int {
	hooks, _ := settings["hooks"].(map[string]any)
	count := 0
	for _, event := range []string{"Stop", "SessionEnd"} {
		groups, _ := hooks[event].([]any)
		for _, rawGroup := range groups {
			group, _ := rawGroup.(map[string]any)
			handlers, _ := group["hooks"].([]any)
			for _, rawHandler := range handlers {
				handler, _ := rawHandler.(map[string]any)
				if isManagedClaudeHook(handler, contextName) {
					count++
				}
			}
		}
	}
	return count
}

func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
