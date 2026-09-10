package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"time"

	"github.com/rossoctl/context-service/internal/client"
	"github.com/rossoctl/context-service/internal/contextresource"
	"github.com/rossoctl/context-service/internal/localcontext"
)

func revisionsContext(c *client.Client, args []string) error {
	flags := flag.NewFlagSet("context revisions", flag.ContinueOnError)
	backend := flags.String("backend", "filesystem", "storage backend: pvc or filesystem")
	namespace := flags.String("namespace", envOr("CS_NAMESPACE", "serverless-harness"), "Kubernetes namespace")
	jsonOutput := flags.Bool("json", false, "print JSON")
	flags.Usage = func() { showHelp([]string{"context", "revisions"}) }
	name, err := parseContextName(flags, args)
	if err != nil {
		return err
	}
	var revisions []contextresource.Revision
	if *backend == "filesystem" || *backend == "local" {
		localRevisions, err := localContextStore().Revisions(name)
		if err != nil {
			return err
		}
		for _, revision := range localRevisions {
			revisions = append(revisions, resourceRevision(revision))
		}
	} else if *backend == "pvc" {
		var err error
		revisions, err = c.ListContextRevisions(context.Background(), *namespace, name)
		if err != nil {
			return err
		}
	} else {
		return fmt.Errorf("unsupported context backend %q; use pvc or filesystem", *backend)
	}
	if *jsonOutput {
		encoded, _ := json.MarshalIndent(revisions, "", "  ")
		fmt.Println(string(encoded))
		return nil
	}
	fmt.Printf("REVISIONS (%d)\n", len(revisions))
	for index := len(revisions) - 1; index >= 0; index-- {
		revision := revisions[index]
		current := ""
		if index == len(revisions)-1 {
			current = " · current"
		}
		fmt.Printf("%s  %s · %s · %s%s\n", shortRevision(revision.ID), revision.Operation, revision.Producer, revision.CreatedAt.Format(time.RFC3339), current)
	}
	if len(revisions) == 0 {
		fmt.Println("None")
	}
	return nil
}

func resourceRevision(revision localcontext.PublishedRevision) contextresource.Revision {
	sources := make([]contextresource.SourceReference, 0, len(revision.Sources))
	for _, source := range revision.Sources {
		sources = append(sources, contextresource.SourceReference{Context: source.Context, Type: source.Type, Revision: source.Revision})
	}
	return contextresource.Revision{
		ID: revision.ID, CreatedAt: revision.CreatedAt, Operation: revision.Operation,
		Producer: revision.Producer, Parameters: revision.Parameters, Sources: sources,
		Files: revision.Files, Bytes: revision.Bytes,
	}
}

type lineageSource struct {
	localcontext.SourceReference
	Available bool `json:"available"`
}

type lineageRevision struct {
	ID        string          `json:"id"`
	Operation string          `json:"operation"`
	Producer  string          `json:"producer,omitempty"`
	Sources   []lineageSource `json:"sources,omitempty"`
}

func lineageContext(args []string) error {
	flags := flag.NewFlagSet("context lineage", flag.ContinueOnError)
	jsonOutput := flags.Bool("json", false, "print JSON")
	flags.Usage = func() { showHelp([]string{"context", "lineage"}) }
	name, err := parseContextName(flags, args)
	if err != nil {
		return err
	}
	store := localContextStore()
	revisions, err := store.Revisions(name)
	if err != nil {
		return err
	}
	result := make([]lineageRevision, 0, len(revisions))
	for _, revision := range revisions {
		entry := lineageRevision{ID: revision.ID, Operation: revision.Operation, Producer: revision.Producer}
		for _, source := range revision.Sources {
			available := false
			if manifest, getErr := store.Get(source.Context); getErr == nil {
				for _, candidate := range manifest.Revisions {
					if candidate.ID == source.Revision {
						available = true
						break
					}
				}
			} else if !errors.Is(getErr, localcontext.ErrNotFound) {
				return getErr
			}
			entry.Sources = append(entry.Sources, lineageSource{SourceReference: source, Available: available})
		}
		result = append(result, entry)
	}
	if *jsonOutput {
		encoded, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(encoded))
		return nil
	}
	fmt.Printf("LINEAGE · %s\n", name)
	for index := len(result) - 1; index >= 0; index-- {
		revision := result[index]
		fmt.Printf("%s  %s · %s\n", shortRevision(revision.ID), revision.Operation, revision.Producer)
		for _, source := range revision.Sources {
			status := "available"
			if !source.Available {
				status = "missing"
			}
			fmt.Printf("└── %s@%s  %s · %s\n", source.Context, shortRevision(source.Revision), source.Type, status)
		}
	}
	if len(result) == 0 {
		fmt.Println("None")
	}
	return nil
}
