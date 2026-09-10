package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"strings"
)

func searchContext(args []string) error {
	flags := flag.NewFlagSet("context search", flag.ContinueOnError)
	limit := flags.Int("limit", 5, "maximum results")
	jsonOutput := flags.Bool("json", false, "print JSON")
	flags.Usage = func() { showHelp([]string{"context", "search"}) }
	if len(args) >= 2 && !strings.HasPrefix(args[0], "-") && !strings.HasPrefix(args[1], "-") {
		args = append(append([]string{}, args[2:]...), args[0], args[1])
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 2 {
		flags.Usage()
		return errors.New("a knowledge context and query are required")
	}
	results, err := localContextStore().SearchKnowledge(flags.Arg(0), flags.Arg(1), *limit)
	if err != nil {
		return err
	}
	if *jsonOutput {
		encoded, _ := json.MarshalIndent(results, "", "  ")
		fmt.Println(string(encoded))
		return nil
	}
	fmt.Printf("\nRESULTS (%d)\n", len(results))
	if len(results) == 0 {
		fmt.Println("None")
		return nil
	}
	for index, result := range results {
		if index > 0 {
			fmt.Println()
		}
		fmt.Printf("%d. %s\n", index+1, result.Record.Title)
		fmt.Printf("   %s\n", result.Record.Text)
		fmt.Printf("   source  %s@%s · %s\n", result.Record.SourceContext, shortRevision(result.Record.SourceRevision), result.Record.SourceFile)
	}
	return nil
}
