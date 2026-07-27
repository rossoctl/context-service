package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/rossoctl/context-service/internal/client"
	"github.com/rossoctl/context-service/internal/pool"
)

const help = `contextctl manages Context Service sandbox pools.

Usage:
  contextctl <command> [options]

Commands:
  health                Check the service
  create NAME           Create a pool
  get NAME              Show a pool
  wait NAME             Wait until a pool is ready
  rm NAME               Delete a pool (alias: delete)
  help [command]        Show help

Quick start:
  contextctl create demo --shared
  contextctl wait demo
  contextctl get demo
  contextctl rm demo

Environment:
  CS_URL                 Service URL (default http://localhost:8080)
  CS_TOKEN               Gateway token for public access
  CS_STORAGE_CLASS       Default storage class for create

Run "contextctl help <command>" for command options and examples.
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "contextctl:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		fmt.Print(help)
		return nil
	}
	if args[0] == "help" {
		showHelp(args[1:])
		return nil
	}
	baseURL := envOr("CS_URL", "http://localhost:8080")
	c := client.New(baseURL, os.Getenv("CS_TOKEN"), &http.Client{Timeout: 30 * time.Second})
	switch args[0] {
	case "health":
		if err := c.Health(context.Background()); err != nil {
			return err
		}
		fmt.Println("ok")
		return nil
	case "create":
		return create(c, args[1:])
	case "get":
		return get(c, args[1:])
	case "wait":
		return wait(c, args[1:])
	case "rm", "delete":
		return remove(c, args[1:])
	default:
		return fmt.Errorf("unknown command %q; run 'contextctl help'", args[0])
	}
}

func create(c *client.Client, args []string) error {
	flags := flag.NewFlagSet("create", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	replicas := flags.Int("n", 1, "number of sandboxes")
	size := flags.String("s", "1Gi", "workspace size")
	storageClass := flags.String("c", os.Getenv("CS_STORAGE_CLASS"), "storage class")
	shared := flags.Bool("shared", false, "use one RWX workspace shared by two sandboxes")
	jsonOutput := flags.Bool("json", false, "print JSON")
	flags.Usage = func() { showHelp([]string{"create"}) }
	name, err := parseName(flags, args)
	if err != nil {
		return err
	}
	accessMode := "ReadWriteOnce"
	if *shared {
		accessMode = "ReadWriteMany"
		if *replicas == 1 {
			*replicas = 2
		}
	}
	result, err := c.Create(context.Background(), pool.CreateRequest{
		Name: name, Replicas: *replicas,
		Workspace: pool.Workspace{Size: *size, AccessMode: accessMode, StorageClass: *storageClass},
	})
	if err != nil {
		return err
	}
	printPool(result, *jsonOutput)
	return nil
}

func get(c *client.Client, args []string) error {
	flags := flag.NewFlagSet("get", flag.ContinueOnError)
	jsonOutput := flags.Bool("json", false, "print JSON")
	flags.Usage = func() { showHelp([]string{"get"}) }
	name, err := parseName(flags, args)
	if err != nil {
		return err
	}
	result, err := c.Get(context.Background(), name)
	if err != nil {
		return err
	}
	printPool(result, *jsonOutput)
	return nil
}

func wait(c *client.Client, args []string) error {
	flags := flag.NewFlagSet("wait", flag.ContinueOnError)
	timeout := flags.Duration("t", 2*time.Minute, "maximum wait time")
	flags.Usage = func() { showHelp([]string{"wait"}) }
	name, err := parseName(flags, args)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		result, err := c.Get(ctx, name)
		if err != nil {
			return err
		}
		fmt.Printf("%s: %s (%d/%d ready)\n", result.Name, result.Status, result.ReadyReplicas, result.Replicas)
		if result.Status == "ready" {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for %s", name)
		case <-ticker.C:
		}
	}
}

func remove(c *client.Client, args []string) error {
	flags := flag.NewFlagSet("rm", flag.ContinueOnError)
	flags.Usage = func() { showHelp([]string{"rm"}) }
	name, err := parseName(flags, args)
	if err != nil {
		return err
	}
	if err := c.Delete(context.Background(), name); err != nil {
		return err
	}
	fmt.Println("deleted", name)
	return nil
}

func parseName(flags *flag.FlagSet, args []string) (string, error) {
	// The standard flag package stops at the first positional argument. Move a
	// leading name to the end so both "create demo --shared" and
	// "create --shared demo" work as users expect.
	if len(args) > 1 && !strings.HasPrefix(args[0], "-") {
		args = append(append([]string{}, args[1:]...), args[0])
	}
	if err := flags.Parse(args); err != nil {
		return "", err
	}
	if flags.NArg() != 1 {
		flags.Usage()
		return "", fmt.Errorf("exactly one pool name is required")
	}
	return flags.Arg(0), nil
}

func printPool(value pool.Pool, jsonOutput bool) {
	if jsonOutput {
		encoded, _ := json.MarshalIndent(value, "", "  ")
		fmt.Println(string(encoded))
		return
	}
	fmt.Printf("%s: %s (%d/%d ready), %s %s, selector %s\n",
		value.Name, value.Status, value.ReadyReplicas, value.Replicas,
		value.Workspace.Size, shortMode(value.Workspace.AccessMode), value.SandboxSelector)
}

func shortMode(mode string) string {
	return strings.NewReplacer("ReadWriteMany", "RWX", "ReadWriteOnce", "RWO").Replace(mode)
}

func showHelp(args []string) {
	if len(args) == 0 {
		fmt.Print(help)
		return
	}
	switch args[0] {
	case "create":
		fmt.Print(`Usage: contextctl create NAME [options]

Create one sandbox with a 1Gi RWO workspace by default.

Options:
  --shared             Create two sandboxes sharing one RWX workspace
  -n NUMBER            Number of sandboxes (default 1, or 2 with --shared)
  -s SIZE              Workspace size (default 1Gi)
  -c STORAGE_CLASS     Storage class (default CS_STORAGE_CLASS)
  --json               Print JSON

Examples:
  contextctl create demo
  contextctl create demo --shared
  contextctl create demo --shared -n 3 -s 5Gi
`)
	case "get":
		fmt.Print("Usage: contextctl get NAME [--json]\n")
	case "wait":
		fmt.Print("Usage: contextctl wait NAME [-t 2m]\n\nWait until every sandbox is ready.\n")
	case "rm", "delete":
		fmt.Print("Usage: contextctl rm NAME\n\nDelete the pool's sandboxes and workspace.\n")
	case "health":
		fmt.Print("Usage: contextctl health\n")
	default:
		fmt.Printf("Unknown command %q.\n\n%s", args[0], help)
	}
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
