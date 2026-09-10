package localcontext

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const artifactIndexVersion = 1

type Artifact struct {
	Name      string          `json:"name"`
	Version   string          `json:"version"`
	MediaType string          `json:"mediaType"`
	Size      int64           `json:"size"`
	Checksum  string          `json:"checksum"`
	CreatedAt time.Time       `json:"createdAt"`
	Producer  string          `json:"producer"`
	Source    SourceReference `json:"source"`
}

type artifactIndex struct {
	Version int        `json:"version"`
	Items   []Artifact `json:"items"`
}

func (s *Store) PublishArtifacts(contextName, inputPath, sourceName, producer, mediaType string) ([]Artifact, Manifest, error) {
	if strings.TrimSpace(producer) == "" {
		return nil, Manifest{}, errors.New("artifact producer is required")
	}
	source, err := s.Get(sourceName)
	if err != nil {
		return nil, Manifest{}, fmt.Errorf("source context: %w", err)
	}
	if source.Type != "workspace" && source.Type != "state" && source.Type != "history" {
		return nil, Manifest{}, errors.New("artifact source must be a workspace or state context")
	}
	if source.CurrentRevision == "" {
		if _, err := s.PublishRevision(sourceName, RevisionMetadata{Operation: "publish", Producer: producer}); err != nil {
			return nil, Manifest{}, err
		}
		source, err = s.Get(sourceName)
		if err != nil {
			return nil, Manifest{}, err
		}
	}
	absolute, err := filepath.Abs(inputPath)
	if err != nil {
		return nil, Manifest{}, err
	}
	files, err := artifactInputFiles(absolute)
	if err != nil {
		return nil, Manifest{}, err
	}
	if len(files) == 0 {
		return nil, Manifest{}, errors.New("artifact input contains no regular files")
	}
	manifest, err := s.Get(contextName)
	if errors.Is(err, ErrNotFound) {
		manifest, err = s.Create(contextName, "artifacts")
	}
	if err != nil {
		return nil, Manifest{}, err
	}
	if manifest.Type != "artifacts" {
		return nil, Manifest{}, fmt.Errorf("context %s is %s, not artifacts", contextName, manifest.Type)
	}
	unlock, err := acquireCaptureLock(s.contextDir(contextName), 30*time.Second)
	if err != nil {
		return nil, Manifest{}, err
	}
	defer unlock()
	index, err := s.readArtifactIndex(contextName)
	if err != nil {
		return nil, Manifest{}, err
	}
	objects := filepath.Join(s.contextDir(contextName), "artifacts", "objects")
	if err := os.MkdirAll(objects, 0o700); err != nil {
		return nil, Manifest{}, err
	}
	rootInfo, _ := os.Stat(absolute)
	var published []Artifact
	for _, file := range files {
		logicalName := filepath.Base(file)
		if rootInfo != nil && rootInfo.IsDir() {
			logicalName, _ = filepath.Rel(absolute, file)
		}
		logicalName = filepath.ToSlash(logicalName)
		checksum, err := checksumFile(file)
		if err != nil {
			return nil, Manifest{}, err
		}
		info, err := os.Stat(file)
		if err != nil {
			return nil, Manifest{}, err
		}
		resolvedMediaType := mediaType
		if resolvedMediaType == "" {
			resolvedMediaType = mime.TypeByExtension(filepath.Ext(file))
			if resolvedMediaType == "" {
				resolvedMediaType = "application/octet-stream"
			}
		}
		item := Artifact{
			Name: logicalName, Version: checksum, MediaType: resolvedMediaType, Size: info.Size(), Checksum: checksum,
			CreatedAt: s.now().UTC(), Producer: producer,
			Source: SourceReference{Context: source.Name, Type: source.Type, Revision: source.CurrentRevision},
		}
		duplicate := false
		for _, existing := range index.Items {
			if existing.Name == item.Name && existing.Version == item.Version {
				item = existing
				duplicate = true
				break
			}
		}
		if !duplicate {
			objectPath := filepath.Join(objects, checksum)
			if _, err := os.Stat(objectPath); errors.Is(err, os.ErrNotExist) {
				if err := copyArtifactFile(file, objectPath); err != nil {
					return nil, Manifest{}, err
				}
			} else if err != nil {
				return nil, Manifest{}, err
			}
			index.Items = append(index.Items, item)
		}
		published = append(published, item)
	}
	sortArtifacts(index.Items)
	if err := s.writeArtifactIndex(contextName, index); err != nil {
		return nil, Manifest{}, err
	}
	sources := []SourceReference{{Context: source.Name, Type: source.Type, Revision: source.CurrentRevision}}
	if _, err := s.recordRevisionUnlocked(&manifest, RevisionMetadata{Operation: "publish-artifacts", Producer: producer, Sources: sources}); err != nil {
		return nil, Manifest{}, err
	}
	manifest.Derivation = &Derivation{Sources: sources, GeneratedAt: s.now().UTC(), Generator: producer}
	if err := s.writeManifest(manifest); err != nil {
		return nil, Manifest{}, err
	}
	result, err := s.Get(contextName)
	return published, result, err
}

func (s *Store) ListArtifacts(contextName string) ([]Artifact, error) {
	manifest, err := s.Get(contextName)
	if err != nil {
		return nil, err
	}
	if manifest.Type != "artifacts" {
		return nil, fmt.Errorf("context %s is %s, not artifacts", contextName, manifest.Type)
	}
	index, err := s.readArtifactIndex(contextName)
	return append([]Artifact(nil), index.Items...), err
}

func (s *Store) GetArtifact(contextName, artifactName, version, outputPath string) (Artifact, string, error) {
	items, err := s.ListArtifacts(contextName)
	if err != nil {
		return Artifact{}, "", err
	}
	var matches []Artifact
	for _, item := range items {
		if item.Name == artifactName && (version == "" || item.Version == version || strings.HasPrefix(item.Version, version)) {
			matches = append(matches, item)
		}
	}
	if len(matches) == 0 {
		return Artifact{}, "", ErrNotFound
	}
	if version != "" && len(matches) > 1 {
		return Artifact{}, "", errors.New("artifact version prefix is ambiguous")
	}
	selected := matches[len(matches)-1]
	if outputPath == "" {
		outputPath = filepath.Base(selected.Name)
	}
	outputPath, err = filepath.Abs(outputPath)
	if err != nil {
		return Artifact{}, "", err
	}
	if _, err := os.Lstat(outputPath); err == nil {
		return Artifact{}, "", fmt.Errorf("output already exists: %s", outputPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Artifact{}, "", err
	}
	if err := copyArtifactFile(filepath.Join(s.contextDir(contextName), "artifacts", "objects", selected.Checksum), outputPath); err != nil {
		return Artifact{}, "", err
	}
	actual, err := checksumFile(outputPath)
	if err != nil || actual != selected.Checksum {
		_ = os.Remove(outputPath)
		if err != nil {
			return Artifact{}, "", err
		}
		return Artifact{}, "", errors.New("retrieved artifact checksum mismatch")
	}
	return selected, outputPath, nil
}

func artifactInputFiles(input string) ([]string, error) {
	info, err := os.Lstat(input)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
		return nil, errors.New("artifact input must be a regular file or directory without symlinks")
	}
	if info.Mode().IsRegular() {
		return []string{input}, nil
	}
	var files []string
	err = filepath.WalkDir(input, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("artifact directory contains symlink: %s", path)
		}
		if !entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("artifact directory contains unsupported file: %s", path)
			}
			files = append(files, path)
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

func (s *Store) readArtifactIndex(name string) (artifactIndex, error) {
	data, err := os.ReadFile(filepath.Join(s.contextDir(name), "artifacts", "index.json"))
	if errors.Is(err, os.ErrNotExist) {
		return artifactIndex{Version: artifactIndexVersion, Items: []Artifact{}}, nil
	}
	if err != nil {
		return artifactIndex{}, err
	}
	var index artifactIndex
	if json.Unmarshal(data, &index) != nil || index.Version != artifactIndexVersion {
		return artifactIndex{}, errors.New("invalid artifact index")
	}
	sortArtifacts(index.Items)
	return index, nil
}

func (s *Store) writeArtifactIndex(name string, index artifactIndex) error {
	directory := filepath.Join(s.contextDir(name), "artifacts")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	index.Version = artifactIndexVersion
	sortArtifacts(index.Items)
	return writeJSONFile(filepath.Join(directory, "index.json"), index)
}

func sortArtifacts(items []Artifact) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].Name != items[j].Name {
			return items[i].Name < items[j].Name
		}
		if !items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].CreatedAt.Before(items[j].CreatedAt)
		}
		return items[i].Version < items[j].Version
	})
}

func copyArtifactFile(source, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		_ = os.Remove(target)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(target)
		return closeErr
	}
	return nil
}
