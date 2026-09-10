package contextquery

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode"

	"github.com/rossoctl/context-service/internal/contextresource"
)

const DefaultLimit = 20
const MaxLimit = 100

type cursor struct {
	Offset int    `json:"o"`
	Query  string `json:"q"`
}

func Search(records []contextresource.QueryRecord, request contextresource.QueryRequest) (contextresource.QueryResponse, error) {
	if strings.TrimSpace(request.Query) == "" && len(request.IDs) == 0 {
		return contextresource.QueryResponse{}, errors.New("query or ids is required")
	}
	if request.Limit == 0 {
		request.Limit = DefaultLimit
	}
	if request.Limit < 1 || request.Limit > MaxLimit {
		return contextresource.QueryResponse{}, errors.New("limit must be between 1 and 100")
	}
	fingerprint := queryFingerprint(request)
	offset, err := decodeCursor(request.Cursor, fingerprint)
	if err != nil {
		return contextresource.QueryResponse{}, err
	}
	terms := tokenize(request.Query)
	results := make([]contextresource.QueryResult, 0)
	for _, record := range records {
		if !matches(record.Type, request.Types) || !matches(record.Source.Context, request.SourceContexts) || !matches(record.Revision, request.Revisions) || !matches(record.ID, request.IDs) {
			continue
		}
		score := score(record, terms)
		if len(terms) > 0 && score == 0 {
			continue
		}
		results = append(results, contextresource.QueryResult{Score: score, Record: record})
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		if results[i].Record.Context != results[j].Record.Context {
			return results[i].Record.Context < results[j].Record.Context
		}
		return results[i].Record.ID < results[j].Record.ID
	})
	if offset > len(results) {
		return contextresource.QueryResponse{}, errors.New("cursor is beyond the result set")
	}
	end := offset + request.Limit
	if end > len(results) {
		end = len(results)
	}
	response := contextresource.QueryResponse{Items: append([]contextresource.QueryResult{}, results[offset:end]...)}
	if end < len(results) {
		response.NextCursor = encodeCursor(cursor{Offset: end, Query: fingerprint})
	}
	return response, nil
}

func matches(value string, filter []string) bool {
	if len(filter) == 0 {
		return true
	}
	for _, expected := range filter {
		if value == expected {
			return true
		}
	}
	return false
}

func score(record contextresource.QueryRecord, terms []string) float64 {
	if len(terms) == 0 {
		return 1
	}
	title := tokenCounts(record.Title)
	text := tokenCounts(record.Text)
	keywords := tokenCounts(strings.Join(record.Keywords, " "))
	var result float64
	for _, term := range terms {
		result += float64(title[term]*3 + keywords[term]*2 + text[term])
	}
	return result
}

func tokenCounts(value string) map[string]int {
	result := map[string]int{}
	for _, term := range tokenize(value) {
		result[term]++
	}
	return result
}

func tokenize(value string) []string {
	return strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

func queryFingerprint(request contextresource.QueryRequest) string {
	copy := request
	copy.Cursor = ""
	copy.Limit = 0
	copy.Contexts = append([]string(nil), request.Contexts...)
	copy.Types = append([]string(nil), request.Types...)
	copy.SourceContexts = append([]string(nil), request.SourceContexts...)
	copy.Revisions = append([]string(nil), request.Revisions...)
	copy.IDs = append([]string(nil), request.IDs...)
	sort.Strings(copy.Contexts)
	sort.Strings(copy.Types)
	sort.Strings(copy.SourceContexts)
	sort.Strings(copy.Revisions)
	sort.Strings(copy.IDs)
	data, _ := json.Marshal(copy)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:8])
}

func encodeCursor(value cursor) string {
	data, _ := json.Marshal(value)
	return base64.RawURLEncoding.EncodeToString(data)
}

func decodeCursor(value, fingerprint string) (int, error) {
	if value == "" {
		return 0, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return 0, errors.New("invalid query cursor")
	}
	var decoded cursor
	if json.Unmarshal(data, &decoded) != nil || decoded.Offset < 0 || decoded.Query != fingerprint {
		return 0, errors.New("invalid query cursor")
	}
	return decoded.Offset, nil
}

func StableID(parts ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(digest[:16])
}

func Offset(cursorValue string) int {
	decoded, err := base64.RawURLEncoding.DecodeString(cursorValue)
	if err != nil {
		return -1
	}
	var value cursor
	if json.Unmarshal(decoded, &value) != nil {
		return -1
	}
	return value.Offset
}
