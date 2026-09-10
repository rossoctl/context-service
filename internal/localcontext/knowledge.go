package localcontext

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

type KnowledgeRecord struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	Text           string   `json:"text"`
	Keywords       []string `json:"keywords,omitempty"`
	SourceContext  string   `json:"sourceContext"`
	SourceType     string   `json:"sourceType"`
	SourceRevision string   `json:"sourceRevision"`
	SourceFile     string   `json:"sourceFile"`
	Generator      string   `json:"generator"`
}

type KnowledgeSearchResult struct {
	Score  int             `json:"score"`
	Record KnowledgeRecord `json:"record"`
}

type knowledgeIndex struct {
	Version int                 `json:"version"`
	Terms   map[string][]string `json:"terms"`
}

func (s *Store) KnowledgeRecords(name string) ([]KnowledgeRecord, error) {
	manifest, err := s.Get(name)
	if err != nil {
		return nil, err
	}
	if manifest.Type != "knowledge" {
		return nil, fmt.Errorf("context %s is %s, not knowledge", name, manifest.Type)
	}
	file, err := os.Open(filepath.Join(manifest.Path, "knowledge", "records.jsonl"))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var records []KnowledgeRecord
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16<<20)
	for scanner.Scan() {
		var record KnowledgeRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return nil, fmt.Errorf("read knowledge record: %w", err)
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

func (s *Store) WriteKnowledge(name string, sources []GenerationSource, generator string, records []KnowledgeRecord) (Manifest, error) {
	if err := validateName(name); err != nil {
		return Manifest{}, err
	}
	if len(sources) == 0 || len(records) == 0 {
		return Manifest{}, errors.New("knowledge requires sources and records")
	}
	if strings.TrimSpace(generator) == "" {
		return Manifest{}, errors.New("generator identity is required")
	}
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return Manifest{}, err
	}
	records = append([]KnowledgeRecord(nil), records...)
	sources = append([]GenerationSource(nil), sources...)
	sort.Slice(records, func(i, j int) bool {
		left := records[i].SourceContext + "\x00" + records[i].SourceFile + "\x00" + records[i].Title + "\x00" + records[i].Text
		right := records[j].SourceContext + "\x00" + records[j].SourceFile + "\x00" + records[j].Title + "\x00" + records[j].Text
		return left < right
	})
	unique := make([]KnowledgeRecord, 0, len(records))
	seenIDs := map[string]bool{}
	for index := range records {
		records[index].Title = strings.TrimSpace(records[index].Title)
		records[index].Text = strings.TrimSpace(records[index].Text)
		records[index].Keywords = normalizeTerms(records[index].Keywords)
		if records[index].Title == "" || records[index].Text == "" || records[index].SourceContext == "" || records[index].SourceRevision == "" || records[index].SourceFile == "" {
			return Manifest{}, fmt.Errorf("invalid knowledge record at index %d", index)
		}
		records[index].ID = knowledgeRecordID(records[index])
		if !seenIDs[records[index].ID] {
			unique = append(unique, records[index])
			seenIDs[records[index].ID] = true
		}
	}
	records = unique
	sort.Slice(sources, func(i, j int) bool { return sources[i].Name < sources[j].Name })
	references := make([]SourceReference, 0, len(sources))
	for _, source := range sources {
		references = append(references, SourceReference{Context: source.Name, Type: source.Type, Revision: source.Revision.Digest})
	}

	staging, err := os.MkdirTemp(s.root, ".context-knowledge-")
	if err != nil {
		return Manifest{}, err
	}
	installed := false
	defer func() {
		if !installed {
			_ = os.RemoveAll(staging)
		}
	}()
	manifest := Manifest{
		Version: manifestVersion, Name: name, Type: "knowledge", Backend: "filesystem",
		CreatedAt: s.now().UTC(), Captures: map[string]Capture{}, Attachments: map[string]Attachment{},
		Derivation: &Derivation{Sources: references, GeneratedAt: s.now().UTC(), Generator: generator},
	}
	if err := s.writeManifestAt(staging, manifest); err != nil {
		return Manifest{}, err
	}
	knowledgeDir := filepath.Join(staging, "knowledge")
	if err := os.MkdirAll(filepath.Join(knowledgeDir, "sources"), 0o700); err != nil {
		return Manifest{}, err
	}
	for _, source := range sources {
		header := fmt.Sprintf("Context: %s\nType: %s\nRevision: %s\n", source.Name, source.Type, source.Revision.Digest)
		if err := os.WriteFile(filepath.Join(knowledgeDir, "sources", source.Name+".txt"), []byte(header+source.Content), 0o600); err != nil {
			return Manifest{}, err
		}
	}
	if err := writeKnowledgeRecords(filepath.Join(knowledgeDir, "records.jsonl"), records); err != nil {
		return Manifest{}, err
	}
	index := buildKnowledgeIndex(records)
	if err := writeJSONFile(filepath.Join(knowledgeDir, "index.json"), index); err != nil {
		return Manifest{}, err
	}
	if err := installKnowledgeDirectory(s.contextDir(name), staging); err != nil {
		return Manifest{}, err
	}
	installed = true
	manifest.Path = s.contextDir(name)
	return manifest, nil
}

func (s *Store) SearchKnowledge(name, query string, limit int) ([]KnowledgeSearchResult, error) {
	if limit < 1 {
		return nil, errors.New("search limit must be positive")
	}
	records, err := s.KnowledgeRecords(name)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(s.contextDir(name), "knowledge", "index.json"))
	if err != nil {
		return nil, err
	}
	var index knowledgeIndex
	if err := json.Unmarshal(data, &index); err != nil || index.Version != 1 {
		return nil, errors.New("invalid knowledge index")
	}
	scores := map[string]int{}
	for _, term := range tokenize(query) {
		for _, id := range index.Terms[term] {
			scores[id]++
		}
	}
	byID := make(map[string]KnowledgeRecord, len(records))
	for _, record := range records {
		byID[record.ID] = record
	}
	results := make([]KnowledgeSearchResult, 0, len(scores))
	for id, score := range scores {
		if record, ok := byID[id]; ok {
			results = append(results, KnowledgeSearchResult{Score: score, Record: record})
		}
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].Record.ID < results[j].Record.ID
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func writeKnowledgeRecords(path string, records []KnowledgeRecord) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			_ = file.Close()
			return err
		}
	}
	return file.Close()
}

func writeJSONFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func buildKnowledgeIndex(records []KnowledgeRecord) knowledgeIndex {
	terms := map[string][]string{}
	for _, record := range records {
		values := tokenize(record.Title + " " + record.Text + " " + strings.Join(record.Keywords, " "))
		seen := map[string]bool{}
		for _, term := range values {
			if !seen[term] {
				terms[term] = append(terms[term], record.ID)
				seen[term] = true
			}
		}
	}
	for term := range terms {
		sort.Strings(terms[term])
	}
	return knowledgeIndex{Version: 1, Terms: terms}
}

func tokenize(value string) []string {
	return normalizeTerms(strings.FieldsFunc(strings.ToLower(value), func(char rune) bool {
		return !unicode.IsLetter(char) && !unicode.IsDigit(char)
	}))
}

func normalizeTerms(values []string) []string {
	seen := map[string]bool{}
	var result []string
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if len([]rune(value)) < 2 || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func knowledgeRecordID(record KnowledgeRecord) string {
	value := strings.Join([]string{record.SourceContext, record.SourceRevision, record.SourceFile, record.Title, record.Text}, "\x00")
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:16])
}

func installKnowledgeDirectory(destination, staging string) error {
	if _, err := os.Lstat(destination); errors.Is(err, os.ErrNotExist) {
		return os.Rename(staging, destination)
	} else if err != nil {
		return err
	}
	previous, err := os.MkdirTemp(filepath.Dir(destination), ".context-previous-")
	if err != nil {
		return err
	}
	if err := os.Remove(previous); err != nil {
		return err
	}
	if err := os.Rename(destination, previous); err != nil {
		return err
	}
	if err := os.Rename(staging, destination); err != nil {
		_ = os.Rename(previous, destination)
		return err
	}
	_ = os.RemoveAll(previous)
	return nil
}
