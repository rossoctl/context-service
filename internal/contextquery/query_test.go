package contextquery

import (
	"testing"

	"github.com/rossoctl/context-service/internal/contextresource"
)

func TestSearchFiltersOrdersAndPaginates(t *testing.T) {
	records := []contextresource.QueryRecord{
		{ID: "b", Context: "knowledge", Type: "knowledge", Revision: "r2", Title: "Release", Text: "release schedule", Source: contextresource.SourceReference{Context: "state", Revision: "s2"}},
		{ID: "a", Context: "memory", Type: "memory", Revision: "r1", Title: "Release schedule", Text: "Friday release schedule", Source: contextresource.SourceReference{Context: "state", Revision: "s1"}},
		{ID: "c", Context: "other", Type: "knowledge", Revision: "r3", Title: "Unrelated", Text: "nothing here"},
	}
	first, err := Search(records, contextresource.QueryRequest{Query: "release schedule", Types: []string{"memory", "knowledge"}, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 1 || first.Items[0].Record.ID != "a" || first.NextCursor == "" {
		t.Fatalf("first page = %+v", first)
	}
	second, err := Search(records, contextresource.QueryRequest{Query: "release schedule", Types: []string{"memory", "knowledge"}, Limit: 1, Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].Record.ID != "b" || second.NextCursor != "" {
		t.Fatalf("second page = %+v", second)
	}
}

func TestLookupAndInvalidCursor(t *testing.T) {
	records := []contextresource.QueryRecord{
		{ID: "known", Context: "memory", Type: "memory", Revision: "current", Text: "value", Source: contextresource.SourceReference{Context: "session", Revision: "source-revision"}},
		{ID: "other", Context: "memory", Type: "memory", Revision: "old", Text: "value", Source: contextresource.SourceReference{Context: "other-session", Revision: "other-source"}},
	}
	response, err := Search(records, contextresource.QueryRequest{IDs: []string{"known"}, SourceContexts: []string{"session"}, Revisions: []string{"current"}})
	if err != nil || len(response.Items) != 1 {
		t.Fatalf("lookup = %+v, err = %v", response, err)
	}
	if _, err := Search(records, contextresource.QueryRequest{Query: "value", Cursor: "invalid"}); err == nil {
		t.Fatal("expected invalid cursor error")
	}
}
