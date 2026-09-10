package localcontext

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// CaptureSessionFile stores a harness-native session file without interpreting
// its contents. The harness adapter owns the format; Context Service owns its
// durable placement and inventory metadata.
func (s *Store) CaptureSessionFile(name, harness, projectPath, sessionID, source string) (Capture, string, error) {
	_, err := s.Get(name)
	if err != nil {
		return Capture{}, "", err
	}
	project, err := canonicalProject(projectPath)
	if err != nil {
		return Capture{}, "", err
	}
	if err := validateName(harness); err != nil {
		return Capture{}, "", fmt.Errorf("invalid harness: %w", err)
	}
	if sessionID == "" {
		sessionID = strings.TrimSuffix(filepath.Base(source), filepath.Ext(source))
	}
	if err := validateName(sessionID); err != nil {
		return Capture{}, "", fmt.Errorf("invalid session ID: %w", err)
	}
	info, err := os.Stat(source)
	if err != nil {
		return Capture{}, "", fmt.Errorf("read %s session: %w", harness, err)
	}
	if !info.Mode().IsRegular() {
		return Capture{}, "", fmt.Errorf("%s session is not a regular file: %s", harness, source)
	}

	unlock, err := acquireCaptureLock(s.contextDir(name), 30*time.Second)
	if err != nil {
		return Capture{}, "", err
	}
	defer unlock()
	manifest, err := s.Get(name)
	if err != nil {
		return Capture{}, "", err
	}
	destinationDir := filepath.Join(s.contextDir(name), "harnesses", harness, "sessions")
	if err := os.MkdirAll(destinationDir, 0o700); err != nil {
		return Capture{}, "", err
	}
	extension := filepath.Ext(source)
	if extension == "" {
		extension = ".jsonl"
	}
	destination := filepath.Join(destinationDir, sessionID+extension)
	if err := copyFileReplacing(source, destination, info.Mode().Perm()); err != nil {
		return Capture{}, "", err
	}
	capture, err := s.updateSessionCapture(manifest, harness, project, destinationDir)
	return capture, destination, err
}

// CaptureSessionExport stores a stable harness export received over stdout.
func (s *Store) CaptureSessionExport(name, harness, projectPath, sessionID string, input io.Reader) (Capture, string, error) {
	_, err := s.Get(name)
	if err != nil {
		return Capture{}, "", err
	}
	project, err := canonicalProject(projectPath)
	if err != nil {
		return Capture{}, "", err
	}
	if err := validateName(harness); err != nil {
		return Capture{}, "", fmt.Errorf("invalid harness: %w", err)
	}
	if err := validateName(sessionID); err != nil {
		return Capture{}, "", fmt.Errorf("invalid session ID: %w", err)
	}
	data, err := io.ReadAll(input)
	if err != nil {
		return Capture{}, "", err
	}
	if !json.Valid(data) {
		return Capture{}, "", fmt.Errorf("%s session export is not valid JSON", harness)
	}

	unlock, err := acquireCaptureLock(s.contextDir(name), 30*time.Second)
	if err != nil {
		return Capture{}, "", err
	}
	defer unlock()
	manifest, err := s.Get(name)
	if err != nil {
		return Capture{}, "", err
	}
	destinationDir := filepath.Join(s.contextDir(name), "harnesses", harness, "sessions")
	if err := os.MkdirAll(destinationDir, 0o700); err != nil {
		return Capture{}, "", err
	}
	destination := filepath.Join(destinationDir, sessionID+".json")
	if err := writeFileReplacing(destination, data, 0o600); err != nil {
		return Capture{}, "", err
	}
	capture, err := s.updateSessionCapture(manifest, harness, project, destinationDir)
	return capture, destination, err
}

func (s *Store) SessionFiles(name, harness string) ([]string, error) {
	if _, err := s.Get(name); err != nil {
		return nil, err
	}
	files, err := filepath.Glob(filepath.Join(s.contextDir(name), "harnesses", harness, "sessions", "*"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func (s *Store) updateSessionCapture(manifest Manifest, harness, project, directory string) (Capture, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return Capture{}, err
	}
	var bytes int64
	files := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return Capture{}, err
		}
		files++
		bytes += info.Size()
	}
	capture := Capture{
		Harness: harness, Project: project, CapturedAt: s.now().UTC(),
		Sessions: files, Files: files, Bytes: bytes,
	}
	manifest.Captures[harness] = capture
	if _, err := s.recordRevisionUnlocked(&manifest, RevisionMetadata{Operation: "capture", Producer: harness}); err != nil {
		return Capture{}, err
	}
	if err := s.writeManifest(manifest); err != nil {
		return Capture{}, err
	}
	return capture, nil
}

func copyFileReplacing(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	temporary := destination + ".tmp"
	output, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		_ = os.Remove(temporary)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(temporary)
		return closeErr
	}
	return os.Rename(temporary, destination)
}

func writeFileReplacing(destination string, data []byte, mode os.FileMode) error {
	temporary := destination + ".tmp"
	if err := os.WriteFile(temporary, data, mode); err != nil {
		return err
	}
	if err := os.Rename(temporary, destination); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}
