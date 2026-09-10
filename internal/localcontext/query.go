package localcontext

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rossoctl/context-service/internal/contextquery"
	"github.com/rossoctl/context-service/internal/contextresource"
)

func (s *Store) QueryRecords(name string) ([]contextresource.QueryRecord, error) {
	manifest, err := s.Get(name)
	if err != nil {
		return nil, err
	}
	switch manifest.Type {
	case "knowledge":
		records, err := s.KnowledgeRecords(name)
		if err != nil {
			return nil, err
		}
		result := make([]contextresource.QueryRecord, 0, len(records))
		for _, record := range records {
			result = append(result, contextresource.QueryRecord{
				ID: record.ID, Context: name, Type: "knowledge", Revision: manifest.CurrentRevision,
				Title: record.Title, Text: record.Text, Keywords: append([]string(nil), record.Keywords...),
				Source: contextresource.SourceReference{Context: record.SourceContext, Type: record.SourceType, Revision: record.SourceRevision}, File: record.SourceFile,
			})
		}
		return result, nil
	case "memory":
		data, err := os.ReadFile(filepath.Join(manifest.Path, "memory", "MEMORY.md"))
		if err != nil {
			return nil, err
		}
		source := contextresource.SourceReference{}
		if manifest.Derivation != nil {
			source = contextresource.SourceReference{Context: manifest.Derivation.SourceContext, Type: manifest.Derivation.SourceType, Revision: manifest.Derivation.SourceRevision}
			if source.Context == "" && len(manifest.Derivation.Sources) > 0 {
				value := manifest.Derivation.Sources[0]
				source = contextresource.SourceReference{Context: value.Context, Type: value.Type, Revision: value.Revision}
			}
		}
		paragraphs := strings.FieldsFunc(string(data), func(r rune) bool { return r == '\n' })
		result := make([]contextresource.QueryRecord, 0, len(paragraphs))
		for _, paragraph := range paragraphs {
			text := strings.TrimSpace(strings.TrimLeft(paragraph, "#-* "))
			if text == "" {
				continue
			}
			title := text
			if characters := []rune(title); len(characters) > 80 {
				title = strings.TrimSpace(string(characters[:80]))
			}
			result = append(result, contextresource.QueryRecord{
				ID:      contextquery.StableID(name, manifest.CurrentRevision, "memory/MEMORY.md", text),
				Context: name, Type: "memory", Revision: manifest.CurrentRevision, Title: title, Text: text,
				Source: source, File: "memory/MEMORY.md",
			})
		}
		return result, nil
	default:
		return nil, errors.New("query requires a memory or knowledge context")
	}
}

func (s *Store) Query(request contextresource.QueryRequest) (contextresource.QueryResponse, error) {
	names := append([]string(nil), request.Contexts...)
	if len(names) == 0 {
		items, err := s.List()
		if err != nil {
			return contextresource.QueryResponse{}, err
		}
		for _, item := range items {
			if item.Type == "memory" || item.Type == "knowledge" {
				names = append(names, item.Name)
			}
		}
	}
	sort.Strings(names)
	var records []contextresource.QueryRecord
	var unavailable []string
	for _, name := range names {
		items, err := s.QueryRecords(name)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				unavailable = append(unavailable, name)
				continue
			}
			return contextresource.QueryResponse{}, err
		}
		records = append(records, items...)
	}
	response, err := contextquery.Search(records, request)
	if err != nil {
		return contextresource.QueryResponse{}, err
	}
	response.Unavailable = unavailable
	return response, nil
}
