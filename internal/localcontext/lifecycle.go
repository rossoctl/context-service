package localcontext

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const snapshotIndexVersion = 1

type Snapshot struct {
	Name      string            `json:"name"`
	Revision  string            `json:"revision"`
	CreatedAt time.Time         `json:"createdAt"`
	Protected bool              `json:"protected,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
	Files     int               `json:"files"`
	Bytes     int64             `json:"bytes"`
}

type RetentionPolicy struct {
	KeepLast int           `json:"keepLast,omitempty"`
	MaxAge   time.Duration `json:"maxAge,omitempty"`
}

type GarbageCollection struct {
	DryRun  bool       `json:"dryRun"`
	Deleted []Snapshot `json:"deleted,omitempty"`
	Kept    []Snapshot `json:"kept,omitempty"`
}

type snapshotIndex struct {
	Version   int             `json:"version"`
	Retention RetentionPolicy `json:"retention,omitempty"`
	Items     []Snapshot      `json:"items,omitempty"`
}

func (s *Store) CreateSnapshot(name, snapshotName string, protected bool, labels map[string]string) (Snapshot, error) {
	if err := validateName(snapshotName); err != nil {
		return Snapshot{}, fmt.Errorf("snapshot name: %w", err)
	}
	unlock, err := acquireCaptureLock(s.contextDir(name), 30*time.Second)
	if err != nil {
		return Snapshot{}, err
	}
	defer unlock()
	manifest, err := s.Get(name)
	if err != nil {
		return Snapshot{}, err
	}
	protected = protected || strings.EqualFold(labels["protected"], "true")
	return s.createSnapshotUnlocked(&manifest, snapshotName, protected, labels, "snapshot")
}

func (s *Store) createSnapshotUnlocked(manifest *Manifest, snapshotName string, protected bool, labels map[string]string, operation string) (Snapshot, error) {
	index, err := s.readSnapshotIndex(manifest.Name)
	if err != nil {
		return Snapshot{}, err
	}
	for _, item := range index.Items {
		if item.Name == snapshotName {
			return Snapshot{}, ErrAlreadyExists
		}
	}
	revision, err := s.recordRevisionUnlocked(manifest, RevisionMetadata{
		Operation: operation, Producer: "contextctl", Parameters: map[string]string{"snapshot": snapshotName},
	})
	if err != nil {
		return Snapshot{}, err
	}
	objects := filepath.Join(s.contextDir(manifest.Name), ".snapshots", "objects")
	if err := os.MkdirAll(objects, 0o700); err != nil {
		return Snapshot{}, err
	}
	bundlePath := filepath.Join(objects, revision.ID+".context")
	if _, err := os.Stat(bundlePath); errors.Is(err, os.ErrNotExist) {
		if _, err := s.exportUnlocked(*manifest, bundlePath); err != nil {
			return Snapshot{}, err
		}
	} else if err != nil {
		return Snapshot{}, err
	}
	if err := s.writeManifest(*manifest); err != nil {
		return Snapshot{}, err
	}
	item := Snapshot{
		Name: snapshotName, Revision: revision.ID, CreatedAt: s.now().UTC(), Protected: protected,
		Labels: cloneStringMap(labels), Files: revision.Files, Bytes: revision.Bytes,
	}
	index.Items = append(index.Items, item)
	sortSnapshots(index.Items)
	if err := s.writeSnapshotIndex(manifest.Name, index); err != nil {
		return Snapshot{}, err
	}
	return item, nil
}

func (s *Store) ListSnapshots(name string) ([]Snapshot, RetentionPolicy, error) {
	if _, err := s.Get(name); err != nil {
		return nil, RetentionPolicy{}, err
	}
	index, err := s.readSnapshotIndex(name)
	if err != nil {
		return nil, RetentionPolicy{}, err
	}
	return append([]Snapshot(nil), index.Items...), index.Retention, nil
}

func (s *Store) GetSnapshot(name, snapshotName string) (Snapshot, error) {
	items, _, err := s.ListSnapshots(name)
	if err != nil {
		return Snapshot{}, err
	}
	for _, item := range items {
		if item.Name == snapshotName {
			return item, nil
		}
	}
	return Snapshot{}, ErrNotFound
}

func (s *Store) CloneSnapshot(sourceName, snapshotName, destinationName string) (Manifest, error) {
	snapshot, err := s.GetSnapshot(sourceName, snapshotName)
	if err != nil {
		return Manifest{}, err
	}
	bundlePath := s.snapshotObjectPath(sourceName, snapshot.Revision)
	manifest, err := s.Import(bundlePath, destinationName)
	if err != nil {
		return Manifest{}, err
	}
	cleaned := false
	defer func() {
		if !cleaned {
			_ = os.RemoveAll(s.contextDir(destinationName))
		}
	}()
	manifest.CreatedAt = s.now().UTC()
	manifest.Attachments = map[string]Attachment{}
	manifest.Revisions = nil
	manifest.CurrentRevision = ""
	manifest.Derivation = &Derivation{
		SourceContext: sourceName, SourceType: manifest.Type, SourceRevision: snapshot.Revision,
		GeneratedAt: s.now().UTC(), Generator: "snapshot-clone",
	}
	revision, err := s.recordRevisionUnlocked(&manifest, RevisionMetadata{
		Operation: "clone", Producer: "contextctl",
		Parameters: map[string]string{"snapshot": snapshotName},
		Sources:    []SourceReference{{Context: sourceName, Type: manifest.Type, Revision: snapshot.Revision}},
	})
	if err != nil {
		return Manifest{}, err
	}
	manifest.CurrentRevision = revision.ID
	if err := s.writeManifest(manifest); err != nil {
		return Manifest{}, err
	}
	cleaned = true
	return s.Get(destinationName)
}

// RestoreSnapshot first preserves the current state when it differs, then
// replaces portable content and records the selected snapshot as provenance.
func (s *Store) RestoreSnapshot(name, snapshotName string) (Manifest, *Snapshot, error) {
	unlock, err := acquireCaptureLock(s.contextDir(name), 30*time.Second)
	if err != nil {
		return Manifest{}, nil, err
	}
	defer unlock()
	manifest, err := s.Get(name)
	if err != nil {
		return Manifest{}, nil, err
	}
	index, err := s.readSnapshotIndex(name)
	if err != nil {
		return Manifest{}, nil, err
	}
	var selected *Snapshot
	for i := range index.Items {
		if index.Items[i].Name == snapshotName {
			copy := index.Items[i]
			selected = &copy
			break
		}
	}
	if selected == nil {
		return Manifest{}, nil, ErrNotFound
	}
	current, err := s.revisionUnlocked(manifest)
	if err != nil {
		return Manifest{}, nil, err
	}
	var safety *Snapshot
	if current.Digest != selected.Revision {
		safetyName := "before-restore-" + s.now().UTC().Format("20060102t150405.000000000z")
		created, err := s.createSnapshotUnlocked(&manifest, safetyName, true, map[string]string{"reason": "automatic-restore-safety"}, "restore-safety")
		if err != nil {
			return Manifest{}, nil, err
		}
		safety = &created
	}

	stageName := "restore-stage-" + strconv.FormatInt(s.now().UnixNano(), 36)
	staged, err := s.Import(s.snapshotObjectPath(name, selected.Revision), stageName)
	if err != nil {
		return Manifest{}, safety, err
	}
	defer os.RemoveAll(staged.Path)
	for _, root := range portableRoots {
		source := filepath.Join(staged.Path, root)
		target := filepath.Join(s.contextDir(name), root)
		if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
			if err := os.RemoveAll(target); err != nil {
				return Manifest{}, safety, err
			}
			continue
		} else if err != nil {
			return Manifest{}, safety, err
		}
		if _, err := replaceTree(source, target); err != nil {
			return Manifest{}, safety, err
		}
	}
	manifest.Captures = staged.Captures
	manifest.Derivation = staged.Derivation
	manifest.CurrentRevision = ""
	revision, err := s.recordRevisionUnlocked(&manifest, RevisionMetadata{
		Operation: "restore", Producer: "contextctl", Parameters: map[string]string{"snapshot": snapshotName},
		Sources: []SourceReference{{Context: name, Type: manifest.Type, Revision: selected.Revision}},
	})
	if err != nil {
		return Manifest{}, safety, err
	}
	manifest.CurrentRevision = revision.ID
	if err := s.writeManifest(manifest); err != nil {
		return Manifest{}, safety, err
	}
	result, err := s.Get(name)
	return result, safety, err
}

func (s *Store) SetRetention(name string, policy RetentionPolicy) (RetentionPolicy, error) {
	if policy.KeepLast < 0 || policy.MaxAge < 0 {
		return RetentionPolicy{}, errors.New("retention values cannot be negative")
	}
	if _, err := s.Get(name); err != nil {
		return RetentionPolicy{}, err
	}
	index, err := s.readSnapshotIndex(name)
	if err != nil {
		return RetentionPolicy{}, err
	}
	index.Retention = policy
	return policy, s.writeSnapshotIndex(name, index)
}

func (s *Store) GarbageCollect(name string, dryRun bool) (GarbageCollection, error) {
	if _, err := s.Get(name); err != nil {
		return GarbageCollection{}, err
	}
	index, err := s.readSnapshotIndex(name)
	if err != nil {
		return GarbageCollection{}, err
	}
	result := GarbageCollection{DryRun: dryRun}
	if index.Retention.KeepLast == 0 && index.Retention.MaxAge == 0 {
		result.Kept = append(result.Kept, index.Items...)
		return result, nil
	}
	newest := append([]Snapshot(nil), index.Items...)
	sort.Slice(newest, func(i, j int) bool { return newest[i].CreatedAt.After(newest[j].CreatedAt) })
	keepByCount := map[string]bool{}
	for i := 0; i < len(newest) && i < index.Retention.KeepLast; i++ {
		keepByCount[newest[i].Name] = true
	}
	cutoff := s.now().UTC().Add(-index.Retention.MaxAge)
	for _, item := range index.Items {
		protected := item.Protected || strings.EqualFold(item.Labels["protected"], "true")
		recent := index.Retention.MaxAge > 0 && !item.CreatedAt.Before(cutoff)
		referenced, err := s.snapshotReferenced(name, item.Revision)
		if err != nil {
			return GarbageCollection{}, err
		}
		if protected || recent || keepByCount[item.Name] || referenced {
			result.Kept = append(result.Kept, item)
			continue
		}
		result.Deleted = append(result.Deleted, item)
	}
	if dryRun || len(result.Deleted) == 0 {
		return result, nil
	}
	deleted := map[string]bool{}
	for _, item := range result.Deleted {
		deleted[item.Name] = true
	}
	kept := index.Items[:0]
	for _, item := range index.Items {
		if !deleted[item.Name] {
			kept = append(kept, item)
		}
	}
	index.Items = kept
	if err := s.writeSnapshotIndex(name, index); err != nil {
		return GarbageCollection{}, err
	}
	for _, item := range result.Deleted {
		used := false
		for _, retained := range kept {
			if retained.Revision == item.Revision {
				used = true
				break
			}
		}
		if !used {
			_ = os.Remove(s.snapshotObjectPath(name, item.Revision))
		}
	}
	return result, nil
}

func (s *Store) snapshotReferenced(source, revision string) (bool, error) {
	contexts, err := s.List()
	if err != nil {
		return false, err
	}
	for _, manifest := range contexts {
		for _, published := range manifest.Revisions {
			for _, ref := range published.Sources {
				if ref.Context == source && ref.Revision == revision {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

func (s *Store) readSnapshotIndex(name string) (snapshotIndex, error) {
	data, err := os.ReadFile(filepath.Join(s.contextDir(name), ".snapshots", "index.json"))
	if errors.Is(err, os.ErrNotExist) {
		return snapshotIndex{Version: snapshotIndexVersion}, nil
	}
	if err != nil {
		return snapshotIndex{}, err
	}
	var index snapshotIndex
	if err := json.Unmarshal(data, &index); err != nil {
		return snapshotIndex{}, fmt.Errorf("read snapshot index: %w", err)
	}
	if index.Version != snapshotIndexVersion {
		return snapshotIndex{}, errors.New("unsupported snapshot index")
	}
	sortSnapshots(index.Items)
	return index, nil
}

func (s *Store) writeSnapshotIndex(name string, index snapshotIndex) error {
	directory := filepath.Join(s.contextDir(name), ".snapshots")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	index.Version = snapshotIndexVersion
	sortSnapshots(index.Items)
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temporary := filepath.Join(directory, "index.json.tmp")
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, filepath.Join(directory, "index.json"))
}

func (s *Store) snapshotObjectPath(name, revision string) string {
	return filepath.Join(s.contextDir(name), ".snapshots", "objects", revision+".context")
}

func sortSnapshots(items []Snapshot) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].Name < items[j].Name
		}
		return items[i].CreatedAt.Before(items[j].CreatedAt)
	})
}
