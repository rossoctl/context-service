package localcontext

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	codexHarness       = "codex"
	opencodeHarness    = "opencode"
	piHarness          = "pi"
	codexHookMarker    = "Context Service: capture "
	opencodeFileMarker = "// Managed by contextctl: OpenCode state capture"
	piFileMarker       = "// Managed by contextctl: Pi state capture"
)

func (s *Store) AttachCodex(name, projectPath, executable string) (Attachment, error) {
	manifest, project, executable, contextHome, err := s.prepareAttachment(name, projectPath, executable)
	if err != nil {
		return Attachment{}, err
	}
	settingsPath := filepath.Join(project, ".codex", "hooks.json")
	settings, mode, existed, err := readClaudeSettings(settingsPath)
	if err != nil {
		return Attachment{}, err
	}
	if err := validateCodexHooks(settings, settingsPath); err != nil {
		return Attachment{}, err
	}
	if other := attachedCodexContext(settings); other != "" && other != name {
		return Attachment{}, fmt.Errorf("this project is already attached to context %s for Codex; detach it first", other)
	}
	removeManagedCodexHooks(settings, "")
	addManagedCodexHooks(settings, executable, name, project, contextHome)
	if err := writeClaudeSettings(settingsPath, settings, mode); err != nil {
		return Attachment{}, err
	}
	return s.recordAttachment(manifest, codexHarness, project, settingsPath, existed)
}

func (s *Store) DetachCodex(name, projectPath string) (Attachment, error) {
	manifest, err := s.Get(name)
	if err != nil {
		return Attachment{}, err
	}
	attachment, attached := manifest.Attachments[codexHarness]
	if projectPath == "" {
		if !attached {
			return Attachment{}, fmt.Errorf("context %s is not attached to Codex", name)
		}
		projectPath = attachment.Project
	}
	project, err := canonicalProject(projectPath)
	if err != nil {
		return Attachment{}, err
	}
	settingsPath := filepath.Join(project, ".codex", "hooks.json")
	settings, mode, _, err := readClaudeSettings(settingsPath)
	if err != nil {
		return Attachment{}, err
	}
	if !removeManagedCodexHooks(settings, name) {
		return Attachment{}, fmt.Errorf("no Context Service Codex hook for %s", name)
	}
	if err := writeClaudeSettings(settingsPath, settings, mode); err != nil {
		return Attachment{}, err
	}
	delete(manifest.Attachments, codexHarness)
	if err := s.writeManifest(manifest); err != nil {
		return Attachment{}, err
	}
	return attachment, nil
}

func (s *Store) AttachOpenCode(name, projectPath, executable, opencodeExecutable string) (Attachment, error) {
	manifest, project, executable, contextHome, err := s.prepareAttachment(name, projectPath, executable)
	if err != nil {
		return Attachment{}, err
	}
	pluginPath := filepath.Join(project, ".opencode", "plugins", "context-service.ts")
	content := openCodePlugin(executable, opencodeExecutable, name, project, contextHome)
	existed, err := writeManagedIntegration(pluginPath, opencodeFileMarker, content)
	if err != nil {
		return Attachment{}, err
	}
	return s.recordAttachment(manifest, opencodeHarness, project, pluginPath, existed)
}

func (s *Store) DetachOpenCode(name, projectPath string) (Attachment, error) {
	return s.detachManagedIntegration(name, opencodeHarness, projectPath, ".opencode", "plugins", "context-service.ts")
}

func (s *Store) AttachPi(name, projectPath, executable string) (Attachment, error) {
	manifest, project, executable, contextHome, err := s.prepareAttachment(name, projectPath, executable)
	if err != nil {
		return Attachment{}, err
	}
	extensionPath := filepath.Join(project, ".pi", "extensions", "context-service.ts")
	content := piExtension(executable, name, project, contextHome)
	existed, err := writeManagedIntegration(extensionPath, piFileMarker, content)
	if err != nil {
		return Attachment{}, err
	}
	return s.recordAttachment(manifest, piHarness, project, extensionPath, existed)
}

func (s *Store) DetachPi(name, projectPath string) (Attachment, error) {
	return s.detachManagedIntegration(name, piHarness, projectPath, ".pi", "extensions", "context-service.ts")
}

func (s *Store) CaptureCodexHook(name, projectPath string, input io.Reader) (Capture, string, error) {
	var event ClaudeHookInput
	if err := json.NewDecoder(input).Decode(&event); err != nil {
		return Capture{}, "", fmt.Errorf("read Codex hook input: %w", err)
	}
	if event.HookEventName != "Stop" && event.HookEventName != "SessionEnd" {
		return Capture{}, "", fmt.Errorf("unexpected Codex hook event %q", event.HookEventName)
	}
	if event.TranscriptPath == "" {
		return Capture{}, "", errors.New("Codex hook did not provide a transcript path")
	}
	if event.HookEventName == "Stop" {
		time.Sleep(500 * time.Millisecond)
	}
	return s.CaptureSessionFile(name, codexHarness, projectPath, event.SessionID, event.TranscriptPath)
}

func (s *Store) prepareAttachment(name, projectPath, executable string) (Manifest, string, string, string, error) {
	manifest, err := s.Get(name)
	if err != nil {
		return Manifest{}, "", "", "", err
	}
	if !isStateType(manifest.Type) {
		return Manifest{}, "", "", "", fmt.Errorf("context %s has type %s; attach requires type state", name, manifest.Type)
	}
	project, err := canonicalProject(projectPath)
	if err != nil {
		return Manifest{}, "", "", "", err
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return Manifest{}, "", "", "", err
	}
	contextHome, err := filepath.Abs(s.root)
	if err != nil {
		return Manifest{}, "", "", "", err
	}
	return manifest, project, executable, contextHome, nil
}

// history was the prototype name for state. Accept existing local manifests so
// users do not need to recreate captured data.
func isStateType(value string) bool {
	return value == "state" || value == "history"
}

func (s *Store) recordAttachment(manifest Manifest, harness, project, settingsPath string, settingsExisted bool) (Attachment, error) {
	attachment := Attachment{
		Harness: harness, Project: project, SettingsPath: settingsPath, AttachedAt: s.now().UTC(),
	}
	manifest.Attachments[harness] = attachment
	if err := s.writeManifest(manifest); err != nil {
		if !settingsExisted {
			_ = os.Remove(settingsPath)
		}
		return Attachment{}, err
	}
	return attachment, nil
}

func validateCodexHooks(settings map[string]any, path string) error {
	rawHooks, exists := settings["hooks"]
	if !exists {
		return nil
	}
	hooks, ok := rawHooks.(map[string]any)
	if !ok {
		return fmt.Errorf("Codex hooks %s has a non-object hooks value; refusing to replace it", path)
	}
	for _, event := range []string{"Stop", "SessionEnd"} {
		if rawEvent, exists := hooks[event]; exists {
			if _, ok := rawEvent.([]any); !ok {
				return fmt.Errorf("Codex hooks %s has a non-array %s value; refusing to replace it", path, event)
			}
		}
	}
	return nil
}

func addManagedCodexHooks(settings map[string]any, executable, contextName, project, contextHome string) {
	hooks := objectValue(settings, "hooks")
	command := strings.Join([]string{
		shellQuote(executable), "hook", "codex-capture", "--context", shellQuote(contextName),
		"--project", shellQuote(project), "--context-home", shellQuote(contextHome),
	}, " ")
	for _, event := range []string{"Stop", "SessionEnd"} {
		handler := map[string]any{
			"type": "command", "command": command, "timeout": 3,
			"statusMessage": codexHookMarker + contextName,
		}
		eventHooks := arrayValue(hooks, event)
		hooks[event] = append(eventHooks, map[string]any{"hooks": []any{handler}})
	}
	settings["hooks"] = hooks
}

func removeManagedCodexHooks(settings map[string]any, contextName string) bool {
	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		return false
	}
	removed := false
	for _, event := range []string{"Stop", "SessionEnd"} {
		groups, _ := hooks[event].([]any)
		keptGroups := make([]any, 0, len(groups))
		for _, rawGroup := range groups {
			group, ok := rawGroup.(map[string]any)
			if !ok {
				keptGroups = append(keptGroups, rawGroup)
				continue
			}
			handlers, _ := group["hooks"].([]any)
			keptHandlers := make([]any, 0, len(handlers))
			for _, rawHandler := range handlers {
				handler, _ := rawHandler.(map[string]any)
				marker, _ := handler["statusMessage"].(string)
				if strings.HasPrefix(marker, codexHookMarker) && (contextName == "" || marker == codexHookMarker+contextName) {
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

func attachedCodexContext(settings map[string]any) string {
	hooks, _ := settings["hooks"].(map[string]any)
	for _, event := range []string{"Stop", "SessionEnd"} {
		groups, _ := hooks[event].([]any)
		for _, rawGroup := range groups {
			group, _ := rawGroup.(map[string]any)
			handlers, _ := group["hooks"].([]any)
			for _, rawHandler := range handlers {
				handler, _ := rawHandler.(map[string]any)
				marker, _ := handler["statusMessage"].(string)
				if strings.HasPrefix(marker, codexHookMarker) {
					return strings.TrimPrefix(marker, codexHookMarker)
				}
			}
		}
	}
	return ""
}

func writeManagedIntegration(path, marker, content string) (bool, error) {
	existing, err := os.ReadFile(path)
	existed := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if existed && !strings.HasPrefix(string(existing), marker) {
		return true, fmt.Errorf("refusing to replace existing integration: %s", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return existed, err
	}
	if err := writeFileReplacing(path, []byte(content), 0o600); err != nil {
		return existed, err
	}
	return existed, nil
}

func (s *Store) detachManagedIntegration(name, harness, projectPath string, pathParts ...string) (Attachment, error) {
	manifest, err := s.Get(name)
	if err != nil {
		return Attachment{}, err
	}
	attachment, attached := manifest.Attachments[harness]
	if projectPath == "" {
		if !attached {
			return Attachment{}, fmt.Errorf("context %s is not attached to %s", name, harness)
		}
		projectPath = attachment.Project
	}
	project, err := canonicalProject(projectPath)
	if err != nil {
		return Attachment{}, err
	}
	path := filepath.Join(append([]string{project}, pathParts...)...)
	data, err := os.ReadFile(path)
	if err != nil {
		return Attachment{}, err
	}
	marker := opencodeFileMarker
	if harness == piHarness {
		marker = piFileMarker
	}
	if !strings.HasPrefix(string(data), marker) {
		return Attachment{}, fmt.Errorf("refusing to remove unmanaged integration: %s", path)
	}
	if err := os.Remove(path); err != nil {
		return Attachment{}, err
	}
	delete(manifest.Attachments, harness)
	if err := s.writeManifest(manifest); err != nil {
		return Attachment{}, err
	}
	return attachment, nil
}

func openCodePlugin(executable, opencodeExecutable, contextName, project, contextHome string) string {
	return fmt.Sprintf(`%s
import { spawn } from "node:child_process"

const contextctl = %s
const opencode = %s
const args = ["hook", "opencode-capture", "--context", %s, "--project", %s, "--context-home", %s, "--opencode", opencode]

export const ContextService = async ({ directory }) => ({
  event: async ({ event }) => {
    if (event.type !== "session.idle") return
    const child = spawn(contextctl, [...args, "--session", event.properties.sessionID], {
      cwd: directory,
      detached: true,
      stdio: "ignore",
    })
    child.unref()
  },
})
`, opencodeFileMarker, strconv.Quote(executable), strconv.Quote(opencodeExecutable), strconv.Quote(contextName), strconv.Quote(project), strconv.Quote(contextHome))
}

func piExtension(executable, contextName, project, contextHome string) string {
	return fmt.Sprintf(`%s
import { spawn, spawnSync } from "node:child_process"

const contextctl = %s
const args = ["hook", "pi-capture", "--context", %s, "--project", %s, "--context-home", %s]

function capture(ctx, synchronous = false) {
  const sessionFile = ctx.sessionManager.getSessionFile()
  if (!sessionFile) return
  const callArgs = [...args, "--session-file", sessionFile]
  if (synchronous) {
    spawnSync(contextctl, callArgs, { cwd: ctx.cwd, stdio: "ignore" })
    return
  }
  const child = spawn(contextctl, callArgs, { cwd: ctx.cwd, detached: true, stdio: "ignore" })
  child.unref()
}

export default function (pi) {
  pi.on("agent_settled", async (_event, ctx) => capture(ctx))
  pi.on("session_shutdown", async (_event, ctx) => capture(ctx, true))
}
`, piFileMarker, strconv.Quote(executable), strconv.Quote(contextName), strconv.Quote(project), strconv.Quote(contextHome))
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
