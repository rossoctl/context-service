package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/rossoctl/context-service/internal/client"
	"github.com/rossoctl/context-service/internal/contextresource"
)

func queryContext(c *client.Client, args []string) error {
	flags := flag.NewFlagSet("context query", flag.ContinueOnError)
	backend := flags.String("backend", "filesystem", "query backend: filesystem or pvc")
	namespace := flags.String("namespace", envOr("CS_NAMESPACE", "serverless-harness"), "Kubernetes namespace")
	limit := flags.Int("limit", 20, "maximum results (1-100)")
	cursor := flags.String("cursor", "", "pagination cursor")
	jsonOutput := flags.Bool("json", false, "print JSON")
	var contexts, types, sources, revisions, ids queryValues
	flags.Var(&contexts, "context", "context to query (repeatable; default all accessible)")
	flags.Var(&types, "type", "memory or knowledge filter (repeatable)")
	flags.Var(&sources, "source", "source context filter (repeatable)")
	flags.Var(&revisions, "revision", "context revision filter (repeatable)")
	flags.Var(&ids, "id", "exact record ID lookup (repeatable)")
	flags.Usage = func() { showHelp([]string{"context", "query"}) }
	query, err := parseOptionalQuery(flags, args)
	if err != nil {
		return err
	}
	if query == "" && len(ids) == 0 {
		return errors.New("query text or --id is required")
	}
	request := contextresource.QueryRequest{
		Contexts: contexts, Types: types, SourceContexts: sources, Revisions: revisions,
		IDs: ids, Query: query, Limit: *limit, Cursor: *cursor,
	}
	var response contextresource.QueryResponse
	switch *backend {
	case "filesystem", "local":
		response, err = localContextStore().Query(request)
	case "pvc":
		response, err = c.QueryContexts(context.Background(), *namespace, request)
	default:
		return fmt.Errorf("unsupported query backend %q; use filesystem or pvc", *backend)
	}
	if err != nil {
		return err
	}
	printQueryResponse(response, *jsonOutput)
	return nil
}

type queryValues []string

func (values *queryValues) String() string { return strings.Join(*values, ",") }
func (values *queryValues) Set(value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New("filter value cannot be empty")
	}
	*values = append(*values, value)
	return nil
}

func parseOptionalQuery(flags *flag.FlagSet, args []string) (string, error) {
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		args = append(append([]string(nil), args[1:]...), args[0])
	}
	if err := flags.Parse(args); err != nil {
		return "", err
	}
	if flags.NArg() > 1 {
		flags.Usage()
		return "", errors.New("query must be a single quoted argument")
	}
	if flags.NArg() == 1 {
		return flags.Arg(0), nil
	}
	return "", nil
}

func printQueryResponse(response contextresource.QueryResponse, jsonOutput bool) {
	if jsonOutput {
		encoded, _ := json.MarshalIndent(response, "", "  ")
		fmt.Println(string(encoded))
		return
	}
	fmt.Printf("\nRESULTS (%d)\n", len(response.Items))
	if len(response.Items) == 0 {
		fmt.Println("None")
	}
	for i, result := range response.Items {
		if i > 0 {
			fmt.Println()
		}
		fmt.Printf("%d. %s  %.0f\n", i+1, result.Record.Title, result.Score)
		fmt.Printf("   %s\n", result.Record.Text)
		fmt.Printf("   context  %s@%s · %s\n", result.Record.Context, shortRevision(result.Record.Revision), result.Record.Type)
		if result.Record.Source.Context != "" {
			fmt.Printf("   source   %s@%s · %s\n", result.Record.Source.Context, shortRevision(result.Record.Source.Revision), result.Record.File)
		}
	}
	if len(response.Unavailable) > 0 {
		fmt.Printf("\nUnavailable indexes: %v\n", response.Unavailable)
	}
	if response.NextCursor != "" {
		fmt.Printf("\nNext cursor: %s\n", response.NextCursor)
	}
}
