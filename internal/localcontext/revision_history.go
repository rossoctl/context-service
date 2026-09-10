package localcontext

import (
	"errors"
	"sort"
	"strings"
	"time"
)

// PublishRevision records the current portable content as a revision. An
// unchanged current revision is returned without adding a duplicate history
// entry.
func (s *Store) PublishRevision(name string, metadata RevisionMetadata) (PublishedRevision, error) {
	unlock, err := acquireCaptureLock(s.contextDir(name), 30*time.Second)
	if err != nil {
		return PublishedRevision{}, err
	}
	defer unlock()
	manifest, err := s.Get(name)
	if err != nil {
		return PublishedRevision{}, err
	}
	revision, err := s.recordRevisionUnlocked(&manifest, metadata)
	if err != nil {
		return PublishedRevision{}, err
	}
	if err := s.writeManifest(manifest); err != nil {
		return PublishedRevision{}, err
	}
	return revision, nil
}

func (s *Store) Revisions(name string) ([]PublishedRevision, error) {
	manifest, err := s.Get(name)
	if err != nil {
		return nil, err
	}
	return append([]PublishedRevision(nil), manifest.Revisions...), nil
}

func (s *Store) recordRevisionUnlocked(manifest *Manifest, metadata RevisionMetadata) (PublishedRevision, error) {
	if strings.TrimSpace(metadata.Operation) == "" {
		return PublishedRevision{}, errors.New("revision operation is required")
	}
	computed, err := s.revisionUnlocked(*manifest)
	if err != nil {
		return PublishedRevision{}, err
	}
	if manifest.CurrentRevision == computed.Digest && len(manifest.Revisions) > 0 {
		return manifest.Revisions[len(manifest.Revisions)-1], nil
	}
	sources := append([]SourceReference(nil), metadata.Sources...)
	sort.Slice(sources, func(i, j int) bool {
		if sources[i].Context != sources[j].Context {
			return sources[i].Context < sources[j].Context
		}
		return sources[i].Revision < sources[j].Revision
	})
	revision := PublishedRevision{
		ID: computed.Digest, CreatedAt: s.now().UTC(), Operation: metadata.Operation,
		Producer: metadata.Producer, Parameters: cloneStringMap(metadata.Parameters),
		Sources: sources, Files: computed.Files, Bytes: computed.Bytes,
	}
	manifest.CurrentRevision = revision.ID
	manifest.Revisions = append(manifest.Revisions, revision)
	return revision, nil
}

func cloneStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}
