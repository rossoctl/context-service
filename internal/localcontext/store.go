package localcontext

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const manifestVersion = 1

var (
	ErrAlreadyExists = errors.New("local context already exists")
	ErrNotFound      = errors.New("local context not found")
)

type Manifest struct {
	Version         int                   `json:"version"`
	Name            string                `json:"name"`
	Type            string                `json:"type"`
	Backend         string                `json:"backend"`
	Path            string                `json:"-"`
	CreatedAt       time.Time             `json:"createdAt"`
	Captures        map[string]Capture    `json:"captures,omitempty"`
	Attachments     map[string]Attachment `json:"attachments,omitempty"`
	Derivation      *Derivation           `json:"derivation,omitempty"`
	CurrentRevision string                `json:"currentRevision,omitempty"`
	Revisions       []PublishedRevision   `json:"revisions,omitempty"`
}

type Derivation struct {
	SourceContext  string            `json:"sourceContext,omitempty"`
	SourceType     string            `json:"sourceType,omitempty"`
	SourceRevision string            `json:"sourceRevision,omitempty"`
	Sources        []SourceReference `json:"sources,omitempty"`
	GeneratedAt    time.Time         `json:"generatedAt"`
	Generator      string            `json:"generator"`
	Parameters     map[string]string `json:"parameters,omitempty"`
}

// PublishedRevision records a content-addressed point in a context's history.
// ID is the SHA-256 identity of the portable context content.
type PublishedRevision struct {
	ID         string            `json:"id"`
	CreatedAt  time.Time         `json:"createdAt"`
	Operation  string            `json:"operation"`
	Producer   string            `json:"producer,omitempty"`
	Parameters map[string]string `json:"parameters,omitempty"`
	Sources    []SourceReference `json:"sources,omitempty"`
	Files      int               `json:"files"`
	Bytes      int64             `json:"bytes"`
}

type RevisionMetadata struct {
	Operation  string
	Producer   string
	Parameters map[string]string
	Sources    []SourceReference
}

type SourceReference struct {
	Context  string `json:"context"`
	Type     string `json:"type"`
	Revision string `json:"revision"`
}

type Capture struct {
	Harness    string    `json:"harness"`
	Project    string    `json:"project"`
	CapturedAt time.Time `json:"capturedAt"`
	Sessions   int       `json:"sessions"`
	Files      int       `json:"files"`
	Bytes      int64     `json:"bytes"`
}

type Attachment struct {
	Harness      string    `json:"harness"`
	Project      string    `json:"project"`
	SettingsPath string    `json:"settingsPath"`
	AttachedAt   time.Time `json:"attachedAt"`
}

type Store struct {
	root string
	now  func() time.Time
}

func New(root string) *Store {
	return &Store{root: filepath.Clean(root), now: time.Now}
}

func (s *Store) Create(name, contextType string) (Manifest, error) {
	if err := validateName(name); err != nil {
		return Manifest{}, err
	}
	if strings.TrimSpace(contextType) == "" {
		return Manifest{}, errors.New("context type is required")
	}
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return Manifest{}, fmt.Errorf("create context home: %w", err)
	}
	dir := s.contextDir(name)
	if _, err := os.Stat(dir); err == nil {
		return Manifest{}, ErrAlreadyExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return Manifest{}, err
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return Manifest{}, fmt.Errorf("create local context: %w", err)
	}
	manifest := Manifest{
		Version: manifestVersion, Name: name, Type: contextType, Backend: "filesystem",
		Path: dir, CreatedAt: s.now().UTC(), Captures: map[string]Capture{},
		Attachments: map[string]Attachment{},
	}
	if err := s.writeManifest(manifest); err != nil {
		_ = os.Remove(dir)
		return Manifest{}, err
	}
	return manifest, nil
}

func (s *Store) Get(name string) (Manifest, error) {
	if err := validateName(name); err != nil {
		return Manifest{}, err
	}
	data, err := os.ReadFile(filepath.Join(s.contextDir(name), "manifest.json"))
	if errors.Is(err, os.ErrNotExist) {
		return Manifest{}, ErrNotFound
	}
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("read local context manifest: %w", err)
	}
	if manifest.Version != manifestVersion || manifest.Name != name || manifest.Backend != "filesystem" {
		return Manifest{}, errors.New("invalid local context manifest")
	}
	if manifest.Captures == nil {
		manifest.Captures = map[string]Capture{}
	}
	if manifest.Attachments == nil {
		manifest.Attachments = map[string]Attachment{}
	}
	manifest.Path = s.contextDir(name)
	return manifest, nil
}

func (s *Store) List() ([]Manifest, error) {
	entries, err := os.ReadDir(s.root)
	if errors.Is(err, os.ErrNotExist) {
		return []Manifest{}, nil
	}
	if err != nil {
		return nil, err
	}
	items := make([]Manifest, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		item, err := s.Get(entry.Name())
		if err != nil {
			continue
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items, nil
}

func (s *Store) CaptureClaude(name, projectPath, claudeHome string) (Capture, error) {
	_, err := s.Get(name)
	if err != nil {
		return Capture{}, err
	}
	project, err := canonicalProject(projectPath)
	if err != nil {
		return Capture{}, err
	}
	source, err := findClaudeProject(filepath.Join(claudeHome, "projects"), project)
	if err != nil {
		return Capture{}, err
	}
	unlock, err := acquireCaptureLock(s.contextDir(name), 30*time.Second)
	if err != nil {
		return Capture{}, err
	}
	defer unlock()
	manifest, err := s.Get(name)
	if err != nil {
		return Capture{}, err
	}

	target := filepath.Join(s.contextDir(name), "harnesses", "claude", "project")
	stats, err := replaceTree(source, target)
	if err != nil {
		return Capture{}, fmt.Errorf("capture Claude project state: %w", err)
	}
	capture := Capture{
		Harness: "claude", Project: project, CapturedAt: s.now().UTC(),
		Sessions: countSessions(target), Files: stats.files, Bytes: stats.bytes,
	}
	manifest.Captures["claude"] = capture
	if _, err := s.recordRevisionUnlocked(&manifest, RevisionMetadata{Operation: "capture", Producer: "claude"}); err != nil {
		return Capture{}, err
	}
	if err := s.writeManifest(manifest); err != nil {
		return Capture{}, err
	}
	return capture, nil
}

func acquireCaptureLock(contextDir string, timeout time.Duration) (func(), error) {
	lock := filepath.Join(contextDir, ".capture.lock")
	deadline := time.Now().Add(timeout)
	for {
		if err := os.Mkdir(lock, 0o700); err == nil {
			return func() { _ = os.Remove(lock) }, nil
		} else if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("lock context capture: %w", err)
		}
		if info, err := os.Stat(lock); err == nil && time.Since(info.ModTime()) > 5*time.Minute {
			_ = os.Remove(lock)
			continue
		}
		if time.Now().After(deadline) {
			return nil, errors.New("timed out waiting for another context capture")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (s *Store) RestoreClaude(name, projectPath, claudeHome string) (Capture, string, error) {
	manifest, err := s.Get(name)
	if err != nil {
		return Capture{}, "", err
	}
	capture, ok := manifest.Captures["claude"]
	if !ok {
		return Capture{}, "", errors.New("context has no Claude state; capture it first")
	}
	project, err := canonicalProject(projectPath)
	if err != nil {
		return Capture{}, "", err
	}
	source := filepath.Join(s.contextDir(name), "harnesses", "claude", "project")
	target := filepath.Join(claudeHome, "projects", ClaudeProjectKey(project))
	if entries, statErr := os.ReadDir(target); statErr == nil && len(entries) > 0 {
		return Capture{}, "", fmt.Errorf("Claude state already exists for %s; refusing to overwrite %s", project, target)
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return Capture{}, "", statErr
	}
	if _, err := copyTree(source, target); err != nil {
		return Capture{}, "", fmt.Errorf("restore Claude project state: %w", err)
	}
	return capture, target, nil
}

func ClaudeProjectKey(projectPath string) string {
	cleaned := filepath.Clean(projectPath)
	return strings.ReplaceAll(cleaned, string(filepath.Separator), "-")
}

func (s *Store) contextDir(name string) string {
	return filepath.Join(s.root, name)
}

func (s *Store) writeManifest(manifest Manifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	target := filepath.Join(s.contextDir(manifest.Name), "manifest.json")
	temporary := target + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporary, target); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func validateName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\\`) {
		return fmt.Errorf("invalid context name %q", name)
	}
	for _, char := range name {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' {
			continue
		}
		return fmt.Errorf("invalid context name %q", name)
	}
	return nil
}

func canonicalProject(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("project path: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("project path is not a directory: %s", absolute)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err == nil {
		absolute = resolved
	}
	return filepath.Clean(absolute), nil
}

func findClaudeProject(projectsRoot, project string) (string, error) {
	candidate := filepath.Join(projectsRoot, ClaudeProjectKey(project))
	if info, err := os.Stat(candidate); err == nil && info.IsDir() {
		return candidate, nil
	}

	entries, err := os.ReadDir(projectsRoot)
	if err != nil {
		return "", fmt.Errorf("read Claude projects: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(projectsRoot, entry.Name())
		matches, err := transcriptHasProject(dir, project)
		if err != nil {
			continue
		}
		if matches {
			return dir, nil
		}
	}
	return "", fmt.Errorf("no Claude state found for %s; run Claude in that project first", project)
}

func transcriptHasProject(dir, project string) (bool, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil || len(files) == 0 {
		return false, err
	}
	for _, file := range files {
		input, err := os.Open(file)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(input)
		scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
		for lines := 0; lines < 100 && scanner.Scan(); lines++ {
			var record struct {
				CWD string `json:"cwd"`
			}
			if json.Unmarshal(scanner.Bytes(), &record) == nil && samePath(record.CWD, project) {
				_ = input.Close()
				return true, nil
			}
		}
		_ = input.Close()
	}
	return false, nil
}

func samePath(a, b string) bool {
	if a == "" {
		return false
	}
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	if a == b {
		return true
	}
	resolved, err := filepath.EvalSymlinks(a)
	return err == nil && filepath.Clean(resolved) == b
}

type copyStats struct {
	files int
	bytes int64
}

func replaceTree(source, target string) (copyStats, error) {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return copyStats{}, err
	}
	staging := target + ".staging"
	backup := target + ".previous"
	_ = os.RemoveAll(staging)
	_ = os.RemoveAll(backup)
	stats, err := copyTree(source, staging)
	if err != nil {
		_ = os.RemoveAll(staging)
		return copyStats{}, err
	}
	if _, err := os.Stat(target); err == nil {
		if err := os.Rename(target, backup); err != nil {
			_ = os.RemoveAll(staging)
			return copyStats{}, err
		}
	}
	if err := os.Rename(staging, target); err != nil {
		_ = os.Rename(backup, target)
		return copyStats{}, err
	}
	_ = os.RemoveAll(backup)
	return stats, nil
}

func copyTree(source, target string) (copyStats, error) {
	var stats copyStats
	if err := os.MkdirAll(target, 0o700); err != nil {
		return stats, err
	}
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil || relative == "." {
			return err
		}
		destination := filepath.Join(target, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to copy symlink %s", path)
		}
		if entry.IsDir() {
			return os.MkdirAll(destination, info.Mode().Perm())
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("unsupported file type: %s", path)
		}
		if err := copyFile(path, destination, info.Mode().Perm()); err != nil {
			return err
		}
		stats.files++
		stats.bytes += info.Size()
		return nil
	})
	return stats, err
}

func copyFile(source, target string, mode fs.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func countSessions(dir string) int {
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	return len(files)
}
