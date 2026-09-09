package localcontext

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const (
	claudeHarness      = "claude"
	claudeHookCommand  = "claude-capture"
	legacyHookCommand  = "claude-session-end"
	claudeHookTimeout  = 60
	claudeSettingsFile = "settings.local.json"
)

// AttachClaude adds Context Service Stop and SessionEnd hooks to this project's
// local Claude settings. Existing settings and unrelated hooks are preserved.
func (s *Store) AttachClaude(name, projectPath, executable string) (Attachment, error) {
	manifest, err := s.Get(name)
	if err != nil {
		return Attachment{}, err
	}
	if !isStateType(manifest.Type) {
		return Attachment{}, fmt.Errorf("context %s has type %s; attach requires type state", name, manifest.Type)
	}
	project, err := canonicalProject(projectPath)
	if err != nil {
		return Attachment{}, err
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return Attachment{}, err
	}
	contextHome, err := filepath.Abs(s.root)
	if err != nil {
		return Attachment{}, err
	}
	settingsPath := filepath.Join(project, ".claude", claudeSettingsFile)
	settings, mode, existed, err := readClaudeSettings(settingsPath)
	if err != nil {
		return Attachment{}, err
	}
	if err := validateClaudeSettings(settings, settingsPath); err != nil {
		return Attachment{}, err
	}
	if other := attachedContext(settings); other != "" && other != name {
		return Attachment{}, fmt.Errorf("this project is already attached to context %s; detach it first", other)
	}
	removeManagedClaudeHooks(settings, "")
	addManagedClaudeHook(settings, executable, name, project, contextHome)
	if err := writeClaudeSettings(settingsPath, settings, mode); err != nil {
		return Attachment{}, err
	}

	attachment := Attachment{
		Harness: claudeHarness, Project: project, SettingsPath: settingsPath, AttachedAt: s.now().UTC(),
	}
	manifest.Attachments[claudeHarness] = attachment
	if err := s.writeManifest(manifest); err != nil {
		if !existed {
			_ = os.Remove(settingsPath)
		}
		return Attachment{}, err
	}
	return attachment, nil
}

// DetachClaude removes only the Context Service hook for this context.
func (s *Store) DetachClaude(name, projectPath string) (Attachment, error) {
	manifest, err := s.Get(name)
	if err != nil {
		return Attachment{}, err
	}
	attachment, attached := manifest.Attachments[claudeHarness]
	if projectPath == "" {
		if !attached {
			return Attachment{}, fmt.Errorf("context %s is not attached to Claude", name)
		}
		projectPath = attachment.Project
	}
	project, err := canonicalProject(projectPath)
	if err != nil {
		return Attachment{}, err
	}
	settingsPath := filepath.Join(project, ".claude", claudeSettingsFile)
	settings, mode, _, err := readClaudeSettings(settingsPath)
	if err != nil {
		return Attachment{}, err
	}
	if !removeManagedClaudeHooks(settings, name) {
		return Attachment{}, fmt.Errorf("no Context Service hook for %s in %s", name, settingsPath)
	}
	if err := writeClaudeSettings(settingsPath, settings, mode); err != nil {
		return Attachment{}, err
	}
	delete(manifest.Attachments, claudeHarness)
	if err := s.writeManifest(manifest); err != nil {
		return Attachment{}, err
	}
	attachment.Harness = claudeHarness
	attachment.Project = project
	attachment.SettingsPath = settingsPath
	return attachment, nil
}

type ClaudeHookInput struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	CWD            string `json:"cwd"`
	HookEventName  string `json:"hook_event_name"`
	Reason         string `json:"reason"`
}

func (s *Store) CaptureClaudeHook(name, projectPath, claudeHome string, input io.Reader) (Capture, error) {
	var event ClaudeHookInput
	if err := json.NewDecoder(input).Decode(&event); err != nil {
		return Capture{}, fmt.Errorf("read Claude hook input: %w", err)
	}
	if event.HookEventName != "Stop" && event.HookEventName != "SessionEnd" {
		return Capture{}, fmt.Errorf("unexpected Claude hook event %q", event.HookEventName)
	}
	// Claude emits Stop just before its transcript becomes visible to another
	// process. Keep the hook synchronous, but allow that final write to land.
	if event.HookEventName == "Stop" {
		time.Sleep(500 * time.Millisecond)
	}
	return s.CaptureClaude(name, projectPath, claudeHome)
}

func readClaudeSettings(path string) (map[string]any, os.FileMode, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, 0o600, false, nil
	}
	if err != nil {
		return nil, 0, false, fmt.Errorf("read Claude settings: %w", err)
	}
	settings := map[string]any{}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &settings); err != nil {
			return nil, 0, true, fmt.Errorf("parse Claude settings %s: %w", path, err)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, 0, true, err
	}
	return settings, info.Mode().Perm(), true, nil
}

func writeClaudeSettings(path string, settings map[string]any, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create Claude settings directory: %w", err)
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temporary := path + ".contextctl.tmp"
	if err := os.WriteFile(temporary, data, mode); err != nil {
		return fmt.Errorf("write Claude settings: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("replace Claude settings: %w", err)
	}
	return nil
}

func validateClaudeSettings(settings map[string]any, path string) error {
	rawHooks, exists := settings["hooks"]
	if !exists {
		return nil
	}
	hooks, ok := rawHooks.(map[string]any)
	if !ok {
		return fmt.Errorf("Claude settings %s has a non-object hooks value; refusing to replace it", path)
	}
	for _, event := range []string{"Stop", "SessionEnd"} {
		if rawEvent, exists := hooks[event]; exists {
			if _, ok := rawEvent.([]any); !ok {
				return fmt.Errorf("Claude settings %s has a non-array %s value; refusing to replace it", path, event)
			}
		}
	}
	return nil
}

func addManagedClaudeHook(settings map[string]any, executable, contextName, project, contextHome string) {
	hooks := objectValue(settings, "hooks")
	for _, event := range []string{"Stop", "SessionEnd"} {
		handler := map[string]any{
			"type":    "command",
			"command": executable,
			"args": []any{
				"hook", claudeHookCommand, "--context", contextName, "--project", project,
				"--context-home", contextHome,
			},
			"timeout": claudeHookTimeout,
		}
		group := map[string]any{"hooks": []any{handler}}
		if event == "SessionEnd" {
			group["matcher"] = "*"
		}
		eventHooks := arrayValue(hooks, event)
		hooks[event] = append(eventHooks, group)
	}
	settings["hooks"] = hooks
}

func removeManagedClaudeHooks(settings map[string]any, contextName string) bool {
	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		return false
	}
	removed := false
	for _, event := range []string{"Stop", "SessionEnd"} {
		eventHooks, ok := hooks[event].([]any)
		if !ok {
			continue
		}
		keptGroups := make([]any, 0, len(eventHooks))
		for _, rawGroup := range eventHooks {
			group, ok := rawGroup.(map[string]any)
			if !ok {
				keptGroups = append(keptGroups, rawGroup)
				continue
			}
			handlers, ok := group["hooks"].([]any)
			if !ok {
				keptGroups = append(keptGroups, rawGroup)
				continue
			}
			keptHandlers := make([]any, 0, len(handlers))
			for _, rawHandler := range handlers {
				handler, ok := rawHandler.(map[string]any)
				if ok && isManagedClaudeHook(handler, contextName) {
					removed = true
					continue
				}
				keptHandlers = append(keptHandlers, rawHandler)
			}
			if len(keptHandlers) > 0 {
				group["hooks"] = keptHandlers
				keptGroups = append(keptGroups, group)
			}
		}
		if len(keptGroups) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = keptGroups
		}
	}
	if len(hooks) == 0 {
		delete(settings, "hooks")
	}
	return removed
}

func isManagedClaudeHook(handler map[string]any, contextName string) bool {
	args, ok := handler["args"].([]any)
	if !ok || len(args) < 4 || args[0] != "hook" || args[2] != "--context" {
		return false
	}
	if args[1] != claudeHookCommand && args[1] != legacyHookCommand {
		return false
	}
	return contextName == "" || args[3] == contextName
}

func attachedContext(settings map[string]any) string {
	hooks, _ := settings["hooks"].(map[string]any)
	for _, event := range []string{"Stop", "SessionEnd"} {
		eventHooks, _ := hooks[event].([]any)
		for _, rawGroup := range eventHooks {
			group, _ := rawGroup.(map[string]any)
			handlers, _ := group["hooks"].([]any)
			for _, rawHandler := range handlers {
				handler, _ := rawHandler.(map[string]any)
				args, _ := handler["args"].([]any)
				if len(args) >= 4 && isManagedClaudeHook(handler, "") {
					name, _ := args[3].(string)
					return name
				}
			}
		}
	}
	return ""
}

func objectValue(parent map[string]any, key string) map[string]any {
	if value, ok := parent[key].(map[string]any); ok {
		return value
	}
	value := map[string]any{}
	parent[key] = value
	return value
}

func arrayValue(parent map[string]any, key string) []any {
	if value, ok := parent[key].([]any); ok {
		return value
	}
	return []any{}
}
