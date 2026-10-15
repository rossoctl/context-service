package localcontext

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// RestoreResult reports a restore of captured session files into a harness's
// native state. Restored names sessions written by this call; Unchanged names
// sessions already present locally with identical content.
type RestoreResult struct {
	Capture
	Destination string   `json:"destination"`
	Restored    []string `json:"restored"`
	Unchanged   []string `json:"unchanged,omitempty"`
}

// sessionPlacement maps one captured session file to its native location.
// relative is confined to the restore root. existing lists native files that
// already hold the same session, so restore can detect local state.
type sessionPlacement func(data []byte, source string) (relative string, existing []string, err error)

// RestoreCodex restores captured Codex rollouts into CODEX_HOME/sessions using
// Codex's own layout: sessions/YYYY/MM/DD/rollout-<timestamp>-<id>.jsonl.
// Codex finds a rollout by thread ID under that tree, so `codex resume <id>`
// opens the restored session.
func (s *Store) RestoreCodex(name, projectPath, codexHome string) (RestoreResult, error) {
	if strings.TrimSpace(codexHome) == "" {
		return RestoreResult{}, errors.New("Codex home is required")
	}
	root := filepath.Join(codexHome, "sessions")
	place := func(data []byte, source string) (string, []string, error) {
		id, timestamp, err := codexSessionMeta(data)
		if err != nil {
			return "", nil, fmt.Errorf("%s: %w", filepath.Base(source), err)
		}
		relative := filepath.Join(timestamp.Format("2006"), timestamp.Format("01"), timestamp.Format("02"),
			"rollout-"+timestamp.Format("2006-01-02T15-04-05")+"-"+id+".jsonl")
		existing, err := findCodexRollouts(codexHome, id)
		return relative, existing, err
	}
	return s.restoreSessionFiles(name, codexHarness, projectPath, root, ".jsonl", place)
}

// RestorePi restores captured Pi session files into Pi's per-project session
// directory, <agentDir>/sessions/--<cwd>--/, or into sessionDir when Pi is
// configured with a custom flat session directory.
func (s *Store) RestorePi(name, projectPath, agentDir, sessionDir string) (RestoreResult, error) {
	project, err := canonicalProject(projectPath)
	if err != nil {
		return RestoreResult{}, err
	}
	root, subdirectory := sessionDir, ""
	if root == "" {
		if strings.TrimSpace(agentDir) == "" {
			return RestoreResult{}, errors.New("Pi agent directory is required")
		}
		root, subdirectory = filepath.Join(agentDir, "sessions"), PiProjectKey(project)
	}
	place := func(data []byte, source string) (string, []string, error) {
		if err := validatePiSession(data); err != nil {
			return "", nil, fmt.Errorf("%s: %w", filepath.Base(source), err)
		}
		relative := filepath.Join(subdirectory, filepath.Base(source))
		return relative, []string{filepath.Join(root, relative)}, nil
	}
	result, err := s.restoreSessionFiles(name, piHarness, project, root, ".jsonl", place)
	if result.Destination != "" {
		result.Destination = filepath.Join(result.Destination, subdirectory)
	}
	return result, err
}

// PiProjectKey mirrors Pi's default session directory name for a cwd.
func PiProjectKey(projectPath string) string {
	cleaned := strings.TrimLeft(filepath.Clean(projectPath), `/\`)
	return "--" + strings.NewReplacer("/", "-", `\`, "-", ":", "-").Replace(cleaned) + "--"
}

// RestoreOpenCode imports captured OpenCode session exports with
// `opencode import`. OpenCode keeps sessions in its own database, so restore
// goes through the harness rather than writing its storage directly. Import
// binds each session to the destination project, which is reported as the
// destination.
func (s *Store) RestoreOpenCode(name, projectPath, executable string) (RestoreResult, error) {
	manifest, err := s.Get(name)
	if err != nil {
		return RestoreResult{}, err
	}
	capture, ok := manifest.Captures[opencodeHarness]
	if !ok {
		return RestoreResult{}, errors.New("context has no OpenCode state; capture it first")
	}
	project, err := canonicalProject(projectPath)
	if err != nil {
		return RestoreResult{}, err
	}
	if executable == "" {
		return RestoreResult{}, errors.New("OpenCode executable is required")
	}
	unlock, err := acquireCaptureLock(s.contextDir(name), 30*time.Second)
	if err != nil {
		return RestoreResult{}, err
	}
	defer unlock()

	files, err := s.capturedSessionFiles(name, opencodeHarness, ".json")
	if err != nil {
		return RestoreResult{}, err
	}
	type planned struct{ id, source string }
	plan := make([]planned, 0, len(files))
	for _, source := range files {
		data, err := os.ReadFile(source)
		if err != nil {
			return RestoreResult{}, err
		}
		var export struct {
			Info struct {
				ID string `json:"id"`
			} `json:"info"`
		}
		if err := json.Unmarshal(data, &export); err != nil {
			return RestoreResult{}, fmt.Errorf("%s: OpenCode export is not valid JSON: %w", filepath.Base(source), err)
		}
		if err := validateName(export.Info.ID); err != nil {
			return RestoreResult{}, fmt.Errorf("%s: invalid OpenCode session ID: %w", filepath.Base(source), err)
		}
		exists, err := openCodeSessionExists(executable, project, export.Info.ID)
		if err != nil {
			return RestoreResult{}, err
		}
		if exists {
			return RestoreResult{}, fmt.Errorf("OpenCode session %s already exists locally; refusing to overwrite it", export.Info.ID)
		}
		plan = append(plan, planned{id: export.Info.ID, source: source})
	}
	result := RestoreResult{Capture: capture, Destination: project, Restored: []string{}}
	for _, item := range plan {
		command := exec.Command(executable, "import", item.source, "--pure")
		command.Dir = project
		if output, err := command.CombinedOutput(); err != nil {
			return result, fmt.Errorf("import OpenCode session %s: %w: %s", item.id, err, strings.TrimSpace(string(output)))
		}
		result.Restored = append(result.Restored, item.id)
	}
	return result, nil
}

func openCodeSessionExists(executable, project, sessionID string) (bool, error) {
	command := exec.Command(executable, "export", sessionID, "--pure")
	command.Dir = project
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	if err == nil {
		return json.Valid(bytes.TrimSpace(stdout.Bytes())), nil
	}
	var exitErr *exec.ExitError
	message := strings.ToLower(stdout.String() + stderr.String())
	if errors.As(err, &exitErr) && strings.Contains(message, "not found") {
		return false, nil
	}
	return false, fmt.Errorf("check local OpenCode session %s: %w: %s", sessionID, err, strings.TrimSpace(stderr.String()))
}

// restoreSessionFiles places every captured session file, refuses differing
// local copies before writing anything, then writes new files without
// replacing any that appear concurrently.
func (s *Store) restoreSessionFiles(name, harness, projectPath, root, extension string, place sessionPlacement) (RestoreResult, error) {
	manifest, err := s.Get(name)
	if err != nil {
		return RestoreResult{}, err
	}
	capture, ok := manifest.Captures[harness]
	if !ok {
		return RestoreResult{}, fmt.Errorf("context has no %s state; capture it first", harnessDisplayName(harness))
	}
	if _, err := canonicalProject(projectPath); err != nil {
		return RestoreResult{}, err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return RestoreResult{}, err
	}
	unlock, err := acquireCaptureLock(s.contextDir(name), 30*time.Second)
	if err != nil {
		return RestoreResult{}, err
	}
	defer unlock()

	files, err := s.capturedSessionFiles(name, harness, extension)
	if err != nil {
		return RestoreResult{}, err
	}
	type planned struct {
		session, destination string
		data                 []byte
	}
	result := RestoreResult{Capture: capture, Destination: root, Restored: []string{}}
	plan := make([]planned, 0, len(files))
	for _, source := range files {
		data, err := os.ReadFile(source)
		if err != nil {
			return RestoreResult{}, err
		}
		relative, existing, err := place(data, source)
		if err != nil {
			return RestoreResult{}, err
		}
		if !filepath.IsLocal(relative) {
			return RestoreResult{}, fmt.Errorf("refusing to restore %s outside %s", relative, root)
		}
		session := strings.TrimSuffix(filepath.Base(relative), extension)
		unchanged := false
		for _, path := range existing {
			current, err := os.ReadFile(path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return RestoreResult{}, err
			}
			if !bytes.Equal(current, data) {
				return RestoreResult{}, fmt.Errorf("%s session %s already exists with different content at %s; refusing to overwrite it",
					harnessDisplayName(harness), session, path)
			}
			unchanged = true
		}
		if unchanged {
			result.Unchanged = append(result.Unchanged, session)
			continue
		}
		plan = append(plan, planned{session: session, destination: filepath.Join(root, relative), data: data})
	}
	for _, item := range plan {
		if err := writeConfinedFile(root, item.destination, item.data); err != nil {
			return result, fmt.Errorf("restore %s session %s: %w", harnessDisplayName(harness), item.session, err)
		}
		result.Restored = append(result.Restored, item.session)
	}
	return result, nil
}

// capturedSessionFiles lists captured session files, rejecting anything that
// is not a regular file with the harness's capture extension.
func (s *Store) capturedSessionFiles(name, harness, extension string) ([]string, error) {
	files, err := s.SessionFiles(name, harness)
	if err != nil {
		return nil, err
	}
	kept := make([]string, 0, len(files))
	for _, file := range files {
		if strings.HasSuffix(file, ".tmp") {
			continue
		}
		info, err := os.Lstat(file)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("refusing to restore non-regular captured file %s", file)
		}
		if filepath.Ext(file) != extension {
			return nil, fmt.Errorf("unsupported captured %s file %s; expected %s", harnessDisplayName(harness), filepath.Base(file), extension)
		}
		if err := validateName(strings.TrimSuffix(filepath.Base(file), extension)); err != nil {
			return nil, err
		}
		kept = append(kept, file)
	}
	if len(kept) == 0 {
		return nil, fmt.Errorf("context has no captured %s sessions", harnessDisplayName(harness))
	}
	return kept, nil
}

// writeConfinedFile creates destination under root without following a
// symlink out of root and without replacing an existing file. root itself may
// be a symlink, as harness homes often are; directories below it may not.
func writeConfinedFile(root, destination string, data []byte) error {
	parent := filepath.Dir(destination)
	relative, err := filepath.Rel(root, parent)
	if err != nil || !filepath.IsLocal(relative) {
		return fmt.Errorf("refusing to write outside %s: %s", root, destination)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	current := root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}
			if info, err = os.Lstat(current); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("refusing to write outside %s: %s is not a directory", root, current)
		}
	}
	temporary, err := os.CreateTemp(parent, ".restore-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Link(temporary.Name(), destination); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%s appeared during restore; refusing to overwrite it", destination)
		}
		return err
	}
	return nil
}

// codexSessionMeta reads the thread ID and start time from a rollout's first
// session_meta record.
func codexSessionMeta(data []byte) (string, time.Time, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for lines := 0; lines < 20 && scanner.Scan(); lines++ {
		var record struct {
			Type    string `json:"type"`
			Payload struct {
				ID        string `json:"id"`
				SessionID string `json:"session_id"`
				Timestamp string `json:"timestamp"`
			} `json:"payload"`
		}
		if json.Unmarshal(scanner.Bytes(), &record) != nil || record.Type != "session_meta" {
			continue
		}
		id := record.Payload.ID
		if id == "" {
			id = record.Payload.SessionID
		}
		if err := validateName(id); err != nil {
			return "", time.Time{}, fmt.Errorf("invalid Codex thread ID: %w", err)
		}
		timestamp, err := time.Parse(time.RFC3339Nano, record.Payload.Timestamp)
		if err != nil {
			return "", time.Time{}, fmt.Errorf("invalid Codex session timestamp %q", record.Payload.Timestamp)
		}
		return id, timestamp.UTC(), nil
	}
	return "", time.Time{}, errors.New("not a Codex rollout: no session_meta record")
}

// findCodexRollouts finds local rollouts, live or archived, for a thread ID.
func findCodexRollouts(codexHome, id string) ([]string, error) {
	var matches []string
	for _, dir := range []string{"sessions", "archived_sessions"} {
		root := filepath.Join(codexHome, dir)
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return nil
				}
				return err
			}
			name := entry.Name()
			if !entry.IsDir() && strings.HasPrefix(name, "rollout-") &&
				(strings.HasSuffix(name, "-"+id+".jsonl") || strings.HasSuffix(name, "-"+id+".jsonl.zst")) {
				matches = append(matches, path)
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("search local Codex sessions: %w", err)
		}
	}
	return matches, nil
}

func validatePiSession(data []byte) error {
	line, _, _ := bytes.Cut(data, []byte("\n"))
	var header struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	if json.Unmarshal(line, &header) != nil || header.Type != "session" || header.ID == "" {
		return errors.New("not a Pi session: missing session header")
	}
	return nil
}

func harnessDisplayName(harness string) string {
	switch harness {
	case "claude":
		return "Claude"
	case codexHarness:
		return "Codex"
	case opencodeHarness:
		return "OpenCode"
	case piHarness:
		return "Pi"
	default:
		return harness
	}
}
