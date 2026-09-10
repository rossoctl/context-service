package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/rossoctl/context-service/internal/localcontext"
)

func artifactCommand(args []string) error {
	if len(args) == 0 {
		showHelp([]string{"context", "artifact"})
		return errors.New("an artifact command is required")
	}
	switch args[0] {
	case "publish":
		return publishArtifacts(args[1:])
	case "list":
		return listArtifacts(args[1:])
	case "get":
		return getArtifact(args[1:])
	default:
		return fmt.Errorf("unknown artifact command %q", args[0])
	}
}

func publishArtifacts(args []string) error {
	flags := flag.NewFlagSet("context artifact publish", flag.ContinueOnError)
	source := flags.String("from", "", "source workspace or state context")
	producer := flags.String("producer", "contextctl", "producer identity")
	mediaType := flags.String("media-type", "", "media type override")
	jsonOutput := flags.Bool("json", false, "print JSON")
	flags.Usage = func() { showHelp([]string{"context", "artifact", "publish"}) }
	positional, err := parseLifecycleArgs(flags, args, 2)
	if err != nil {
		return err
	}
	if len(positional) != 2 || *source == "" {
		flags.Usage()
		return errors.New("artifact context, path, and --from source are required")
	}
	items, manifest, err := localContextStore().PublishArtifacts(positional[0], positional[1], *source, *producer, *mediaType)
	if err != nil {
		return err
	}
	if *jsonOutput {
		encoded, _ := json.MarshalIndent(struct {
			Context   localcontext.Manifest   `json:"context"`
			Artifacts []localcontext.Artifact `json:"artifacts"`
		}{manifest, items}, "", "  ")
		fmt.Println(string(encoded))
		return nil
	}
	fmt.Printf("Published %d artifact(s) to %s@%s\n", len(items), manifest.Name, shortRevision(manifest.CurrentRevision))
	printArtifacts(items)
	return nil
}

func listArtifacts(args []string) error {
	flags := flag.NewFlagSet("context artifact list", flag.ContinueOnError)
	jsonOutput := flags.Bool("json", false, "print JSON")
	flags.Usage = func() { showHelp([]string{"context", "artifact", "list"}) }
	name, err := parseContextName(flags, args)
	if err != nil {
		return err
	}
	items, err := localContextStore().ListArtifacts(name)
	if err != nil {
		return err
	}
	if *jsonOutput {
		encoded, _ := json.MarshalIndent(items, "", "  ")
		fmt.Println(string(encoded))
		return nil
	}
	printArtifacts(items)
	return nil
}

func getArtifact(args []string) error {
	flags := flag.NewFlagSet("context artifact get", flag.ContinueOnError)
	output := flags.String("output", "", "output file")
	jsonOutput := flags.Bool("json", false, "print JSON")
	flags.Usage = func() { showHelp([]string{"context", "artifact", "get"}) }
	positional, err := parseLifecycleArgs(flags, args, 2)
	if err != nil {
		return err
	}
	if len(positional) != 2 {
		flags.Usage()
		return errors.New("artifact context and name are required")
	}
	artifactName, version, _ := strings.Cut(positional[1], "@")
	item, path, err := localContextStore().GetArtifact(positional[0], artifactName, version, *output)
	if err != nil {
		return err
	}
	if *jsonOutput {
		encoded, _ := json.MarshalIndent(struct {
			Artifact localcontext.Artifact `json:"artifact"`
			Path     string                `json:"path"`
		}{item, path}, "", "  ")
		fmt.Println(string(encoded))
		return nil
	}
	fmt.Printf("Retrieved %s@%s to %s\n", item.Name, shortRevision(item.Version), path)
	return nil
}

func printArtifacts(items []localcontext.Artifact) {
	fmt.Printf("\nARTIFACTS (%d)\n", len(items))
	if len(items) == 0 {
		fmt.Println("None")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tVERSION\tSIZE\tMEDIA TYPE\tSOURCE")
	for _, item := range items {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s@%s\n", item.Name, shortRevision(item.Version), formatBytes(item.Size), item.MediaType, item.Source.Context, shortRevision(item.Source.Revision))
	}
	_ = w.Flush()
}
