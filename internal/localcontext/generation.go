package localcontext

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

type GenerationSource struct {
	Name     string
	Type     string
	Revision Revision
	Content  string
	Files    []string
}

// StateForGeneration returns a stable text view and revision while capture is
// locked. Binary files contribute to the revision but are not sent to the
// generator.
func (s *Store) StateForGeneration(name string, maxTextBytes int64) (GenerationSource, error) {
	return s.contextForGeneration(name, maxTextBytes, map[string]bool{"state": true, "history": true}, []string{"harnesses"}, "memory generation requires state")
}

func (s *Store) ContextForKnowledge(name string, maxTextBytes int64) (GenerationSource, error) {
	return s.contextForGeneration(name, maxTextBytes, map[string]bool{"state": true, "history": true, "memory": true}, []string{"harnesses", "memory"}, "knowledge generation requires state or memory")
}

func (s *Store) contextForGeneration(name string, maxTextBytes int64, allowedTypes map[string]bool, roots []string, typeError string) (GenerationSource, error) {
	manifest, err := s.Get(name)
	if err != nil {
		return GenerationSource{}, err
	}
	if !allowedTypes[manifest.Type] {
		return GenerationSource{}, fmt.Errorf("source context %s is %s; %s", name, manifest.Type, typeError)
	}
	unlock, err := acquireCaptureLock(s.contextDir(name), 0)
	if err != nil {
		return GenerationSource{}, err
	}
	defer unlock()
	revision, err := s.revisionUnlocked(manifest)
	if err != nil {
		return GenerationSource{}, err
	}

	var paths []string
	contextRoot := s.contextDir(name)
	for _, rootName := range roots {
		root := filepath.Join(contextRoot, rootName)
		err = filepath.WalkDir(root, func(filePath string, item fs.DirEntry, walkErr error) error {
			if errors.Is(walkErr, os.ErrNotExist) && filePath == root {
				return fs.SkipDir
			}
			if walkErr != nil {
				return walkErr
			}
			if !item.IsDir() {
				paths = append(paths, filePath)
			}
			return nil
		})
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return GenerationSource{}, err
		}
	}
	sort.Strings(paths)
	var content bytes.Buffer
	var textBytes int64
	var files []string
	for _, filePath := range paths {
		info, err := os.Stat(filePath)
		if err != nil {
			return GenerationSource{}, err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if maxTextBytes > 0 && textBytes+info.Size() > maxTextBytes {
			return GenerationSource{}, fmt.Errorf("captured state exceeds generation limit of %d bytes", maxTextBytes)
		}
		data, err := os.ReadFile(filePath)
		if err != nil {
			return GenerationSource{}, err
		}
		if !utf8.Valid(data) {
			continue
		}
		relative, _ := filepath.Rel(contextRoot, filePath)
		fmt.Fprintf(&content, "\n--- %s ---\n", filepath.ToSlash(relative))
		content.Write(data)
		if len(data) > 0 && data[len(data)-1] != '\n' {
			content.WriteByte('\n')
		}
		textBytes += int64(len(data))
		files = append(files, filepath.ToSlash(relative))
	}
	if content.Len() == 0 {
		return GenerationSource{}, errors.New("state context has no readable captured files")
	}
	return GenerationSource{Name: name, Type: manifest.Type, Revision: revision, Content: content.String(), Files: files}, nil
}

func (s *Store) CreateDerivedMemory(name string, source GenerationSource, generator, memory string, transformationParameters ...map[string]string) (Manifest, error) {
	if err := validateName(name); err != nil {
		return Manifest{}, err
	}
	memory = strings.TrimSpace(memory)
	if memory == "" {
		return Manifest{}, errors.New("generator returned empty memory")
	}
	if strings.TrimSpace(generator) == "" {
		return Manifest{}, errors.New("generator identity is required")
	}
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return Manifest{}, err
	}
	destination := s.contextDir(name)
	if _, err := os.Lstat(destination); err == nil {
		return Manifest{}, ErrAlreadyExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return Manifest{}, err
	}
	staging, err := os.MkdirTemp(s.root, ".context-memory-")
	if err != nil {
		return Manifest{}, err
	}
	installed := false
	defer func() {
		if !installed {
			_ = os.RemoveAll(staging)
		}
	}()
	parameters := firstParameters(transformationParameters)
	manifest := Manifest{
		Version: manifestVersion, Name: name, Type: "memory", Backend: "filesystem",
		CreatedAt: s.now().UTC(), Captures: map[string]Capture{}, Attachments: map[string]Attachment{},
		Derivation: &Derivation{
			SourceContext: source.Name, SourceType: source.Type, SourceRevision: source.Revision.Digest,
			GeneratedAt: s.now().UTC(), Generator: generator, Parameters: parameters,
		},
	}
	if err := s.writeManifestAt(staging, manifest); err != nil {
		return Manifest{}, err
	}
	memoryDir := filepath.Join(staging, "memory")
	if err := os.MkdirAll(memoryDir, 0o700); err != nil {
		return Manifest{}, err
	}
	if err := os.WriteFile(filepath.Join(memoryDir, "MEMORY.md"), []byte(memory+"\n"), 0o600); err != nil {
		return Manifest{}, err
	}
	if err := os.Rename(staging, destination); err != nil {
		return Manifest{}, err
	}
	installed = true
	revision, err := s.PublishRevision(name, RevisionMetadata{
		Operation: "derive", Producer: generator, Parameters: parameters,
		Sources: []SourceReference{{Context: source.Name, Type: source.Type, Revision: source.Revision.Digest}},
	})
	if err != nil {
		return Manifest{}, err
	}
	manifest, err = s.Get(name)
	if err != nil {
		return Manifest{}, err
	}
	manifest.CurrentRevision = revision.ID
	return manifest, nil
}

func firstParameters(values []map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	return cloneStringMap(values[0])
}
