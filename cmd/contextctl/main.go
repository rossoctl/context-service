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
	"text/tabwriter"
	"time"

	"github.com/rossoctl/context-service/internal/client"
	"github.com/rossoctl/context-service/internal/contextresource"
	"github.com/rossoctl/context-service/internal/pool"
	"github.com/rossoctl/context-service/internal/storageclass"
)

const help = `contextctl manages Context Service resources.

Usage:
  contextctl <command> [options]

Commands:
  health                Check the service
  storage-classes       List available Kubernetes storage classes
  context COMMAND       Create, list, show, or delete named contexts
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
  CS_NAMESPACE           Default context namespace (default serverless-harness)
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
	case "storage-classes":
		return listStorageClasses(c, args[1:])
	case "context":
		return contextCommand(c, args[1:])
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

func listStorageClasses(c *client.Client, args []string) error {
	flags := flag.NewFlagSet("storage-classes", flag.ContinueOnError)
	jsonOutput := flags.Bool("json", false, "print JSON")
	flags.Usage = func() { showHelp([]string{"storage-classes"}) }
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		flags.Usage()
		return errors.New("storage-classes does not accept arguments")
	}
	items, err := c.ListStorageClasses(context.Background())
	if err != nil {
		return err
	}
	printStorageClasses(items, *jsonOutput)
	return nil
}

func contextCommand(c *client.Client, args []string) error {
	if len(args) == 0 {
		showHelp([]string{"context"})
		return errors.New("a context command is required")
	}
	switch args[0] {
	case "help", "-h", "--help":
		showHelp([]string{"context"})
		return nil
	case "create":
		return createContext(c, args[1:])
	case "list":
		return listContexts(c, args[1:])
	case "get":
		return getContext(c, args[1:])
	case "rm", "delete":
		return removeContext(c, args[1:])
	default:
		return fmt.Errorf("unknown context command %q; run 'contextctl help context'", args[0])
	}
}

func createContext(c *client.Client, args []string) error {
	flags := flag.NewFlagSet("context create", flag.ContinueOnError)
	namespace := flags.String("namespace", envOr("CS_NAMESPACE", "serverless-harness"), "Kubernetes namespace")
	contextType := flags.String("type", "workspace", "context type")
	size := flags.String("size", "1Gi", "storage size")
	storageClass := flags.String("storage-class", os.Getenv("CS_STORAGE_CLASS"), "storage class")
	accessMode := flags.String("access-mode", "ReadWriteOnce", "storage access mode")
	jsonOutput := flags.Bool("json", false, "print JSON")
	flags.Usage = func() { showHelp([]string{"context", "create"}) }
	name, err := parseContextName(flags, args)
	if err != nil {
		return err
	}
	result, err := c.CreateContext(context.Background(), contextresource.CreateRequest{
		Name: name, Namespace: *namespace, Type: *contextType,
		Storage: contextresource.Storage{
			Backend: "pvc", Size: *size, AccessMode: *accessMode, StorageClass: *storageClass,
		},
	})
	if err != nil {
		return err
	}
	printContext(result, *jsonOutput)
	return nil
}

func listContexts(c *client.Client, args []string) error {
	flags := flag.NewFlagSet("context list", flag.ContinueOnError)
	namespace := flags.String("namespace", envOr("CS_NAMESPACE", "serverless-harness"), "Kubernetes namespace")
	jsonOutput := flags.Bool("json", false, "print JSON")
	flags.Usage = func() { showHelp([]string{"context", "list"}) }
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		flags.Usage()
		return errors.New("context list does not accept arguments")
	}
	items, err := c.ListContexts(context.Background(), *namespace)
	if err != nil {
		return err
	}
	printContexts(items, *jsonOutput)
	return nil
}

func getContext(c *client.Client, args []string) error {
	flags := flag.NewFlagSet("context get", flag.ContinueOnError)
	namespace := flags.String("namespace", envOr("CS_NAMESPACE", "serverless-harness"), "Kubernetes namespace")
	jsonOutput := flags.Bool("json", false, "print JSON")
	flags.Usage = func() { showHelp([]string{"context", "get"}) }
	name, err := parseContextName(flags, args)
	if err != nil {
		return err
	}
	result, err := c.GetContext(context.Background(), *namespace, name)
	if err != nil {
		return err
	}
	printContext(result, *jsonOutput)
	return nil
}

func removeContext(c *client.Client, args []string) error {
	flags := flag.NewFlagSet("context rm", flag.ContinueOnError)
	namespace := flags.String("namespace", envOr("CS_NAMESPACE", "serverless-harness"), "Kubernetes namespace")
	flags.Usage = func() { showHelp([]string{"context", "rm"}) }
	name, err := parseContextName(flags, args)
	if err != nil {
		return err
	}
	if err := c.DeleteContext(context.Background(), *namespace, name); err != nil {
		return err
	}
	fmt.Println("deleted", name)
	return nil
}

func create(c *client.Client, args []string) error {
	flags := flag.NewFlagSet("create", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	replicas := flags.Int("n", 1, "number of sandboxes")
	size := flags.String("s", "1Gi", "workspace size")
	storageClass := flags.String("c", os.Getenv("CS_STORAGE_CLASS"), "storage class")
	shared := flags.Bool("shared", false, "use one RWX workspace shared by two sandboxes")
	claimName := flags.String("claim", "", "mount an existing PVC instead of creating workspace storage")
	warmPoolRef := flags.String("warm-pool", "", "claim ready sandboxes from an existing SandboxWarmPool")
	readOnly := flags.Bool("read-only", false, "mount the existing claim read-only")
	readWrite := flags.Bool("read-write", false, "mount the existing claim read-write")
	jsonOutput := flags.Bool("json", false, "print JSON")
	flags.Usage = func() { showHelp([]string{"create"}) }
	name, err := parseName(flags, args)
	if err != nil {
		return err
	}
	workspace := pool.Workspace{Size: *size, AccessMode: "ReadWriteOnce", StorageClass: *storageClass}
	if *warmPoolRef != "" {
		incompatible := map[string]bool{
			"shared": false, "claim": false, "read-only": false, "read-write": false, "s": false, "c": false,
		}
		flags.Visit(func(candidate *flag.Flag) {
			if _, found := incompatible[candidate.Name]; found {
				incompatible[candidate.Name] = true
			}
		})
		for _, option := range []string{"shared", "claim", "read-only", "read-write", "s", "c"} {
			if incompatible[option] {
				return fmt.Errorf("--warm-pool cannot be combined with --%s", option)
			}
		}
		workspace = pool.Workspace{}
	} else if *claimName != "" {
		if *readOnly == *readWrite {
			return errors.New("--claim requires exactly one of --read-only or --read-write")
		}
		workspace = pool.Workspace{ClaimName: *claimName, ReadOnly: readOnly}
	} else if *readOnly || *readWrite {
		return errors.New("--read-only and --read-write require --claim")
	} else if *shared {
		workspace.AccessMode = "ReadWriteMany"
		if *replicas == 1 {
			*replicas = 2
		}
	}
	result, err := c.Create(context.Background(), pool.CreateRequest{
		Name: name, Replicas: *replicas, WarmPoolRef: *warmPoolRef,
		Workspace: workspace,
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

func parseContextName(flags *flag.FlagSet, args []string) (string, error) {
	if len(args) > 1 && !strings.HasPrefix(args[0], "-") {
		args = append(append([]string{}, args[1:]...), args[0])
	}
	if err := flags.Parse(args); err != nil {
		return "", err
	}
	if flags.NArg() != 1 {
		flags.Usage()
		return "", errors.New("exactly one context name is required")
	}
	return flags.Arg(0), nil
}

func printPool(value pool.Pool, jsonOutput bool) {
	if jsonOutput {
		encoded, _ := json.MarshalIndent(value, "", "  ")
		fmt.Println(string(encoded))
		return
	}
	fmt.Printf("%s: %s (%d/%d ready), %s, selector %s\n",
		value.Name, value.Status, value.ReadyReplicas, value.Replicas,
		workspaceSummary(value), value.SandboxSelector)
}

func printStorageClasses(items []storageclass.Resource, jsonOutput bool) {
	if jsonOutput {
		encoded, _ := json.MarshalIndent(items, "", "  ")
		fmt.Println(string(encoded))
		return
	}
	if len(items) == 0 {
		fmt.Println("No storage classes found.")
		return
	}
	writer := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "NAME\tDEFAULT\tPROVISIONER\tBINDING MODE\tEXPANSION")
	for _, item := range items {
		fmt.Fprintf(writer, "%s\t%t\t%s\t%s\t%t\n", item.Name, item.Default,
			item.Provisioner, item.VolumeBindingMode, item.AllowVolumeExpansion)
	}
	_ = writer.Flush()
}

func printContexts(items []contextresource.Resource, jsonOutput bool) {
	if jsonOutput {
		encoded, _ := json.MarshalIndent(items, "", "  ")
		fmt.Println(string(encoded))
		return
	}
	if len(items) == 0 {
		fmt.Println("No contexts found.")
		return
	}
	writer := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "NAME\tNAMESPACE\tTYPE\tSTATUS\tSTORAGE\tKUBERNETES RESOURCE")
	for _, item := range items {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s %s (%s)\t%s/%s\n", item.Name, item.Namespace,
			item.Type, item.Status, item.Storage.Size, item.Storage.AccessMode,
			storageClassName(item.Storage.StorageClass), strings.ToLower(item.Attachment.Kind),
			item.Attachment.ClaimName)
	}
	_ = writer.Flush()
}

func printContext(value contextresource.Resource, jsonOutput bool) {
	if jsonOutput {
		encoded, _ := json.MarshalIndent(value, "", "  ")
		fmt.Println(string(encoded))
		return
	}
	fmt.Printf(`Context
  Name:               %s
  Namespace:          %s
  Type:               %s
  Status:             %s
  Storage:            %s %s
  Storage class:      %s
  Kubernetes resource: %s/%s
`, value.Name, value.Namespace, value.Type, value.Status, value.Storage.Size,
		value.Storage.AccessMode, storageClassName(value.Storage.StorageClass),
		strings.ToLower(value.Attachment.Kind), value.Attachment.ClaimName)
	if value.Status == "provisioning" {
		fmt.Printf("\nInspect: kubectl -n %s get %s %s\n", value.Namespace,
			strings.ToLower(value.Attachment.Kind), value.Attachment.ClaimName)
		fmt.Println("Hint: a WaitForFirstConsumer storage class binds the PVC after a workload mounts it.")
	}
}

func storageClassName(name string) string {
	if name == "" {
		return "<default>"
	}
	return name
}

func workspaceSummary(value pool.Pool) string {
	if value.WarmPoolRef != "" {
		return "warm pool " + value.WarmPoolRef
	}
	return fmt.Sprintf("%s %s", value.Workspace.Size, shortMode(value.Workspace.AccessMode))
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
	case "storage-classes":
		fmt.Print("Usage: contextctl storage-classes [--json]\n")
	case "context":
		if len(args) == 1 {
			fmt.Print(`Usage: contextctl context COMMAND [options]

Commands:
  create NAME          Create a named context
  list                 List named contexts
  get NAME             Show a named context
  rm NAME              Delete a named context (alias: delete)

Run "contextctl help context COMMAND" for command options.
`)
			return
		}
		switch args[1] {
		case "create":
			fmt.Print(`Usage: contextctl context create NAME [options]

Options:
  --namespace NAME      Kubernetes namespace (default CS_NAMESPACE or serverless-harness)
  --type TYPE           Context type (default workspace)
  --size SIZE           Storage size (default 1Gi)
  --storage-class NAME  Kubernetes storage class (default CS_STORAGE_CLASS)
  --access-mode MODE    ReadWriteOnce or ReadWriteMany (default ReadWriteOnce)
  --json                Print JSON
`)
		case "list":
			fmt.Print("Usage: contextctl context list [--namespace NAME] [--json]\n")
		case "get":
			fmt.Print("Usage: contextctl context get NAME [--namespace NAME] [--json]\n")
		case "rm", "delete":
			fmt.Print("Usage: contextctl context rm NAME [--namespace NAME]\n")
		default:
			fmt.Printf("Unknown context command %q.\n", args[1])
		}
	case "create":
		fmt.Print(`Usage: contextctl create NAME [options]

Create one sandbox with a 1Gi RWO workspace by default.

Options:
  --shared             Create two sandboxes sharing one RWX workspace
  --warm-pool NAME      Claim sandboxes from an existing SandboxWarmPool
  --claim NAME          Mount an existing PVC; CS will not delete it
  --read-only           Mount the existing claim read-only (requires --claim)
  --read-write          Mount the existing claim read-write (requires --claim)
  -n NUMBER            Number of sandboxes (default 1, or 2 with --shared)
  -s SIZE              Workspace size (default 1Gi)
  -c STORAGE_CLASS     Storage class (default CS_STORAGE_CLASS)
  --json               Print JSON

Examples:
  contextctl create demo
  contextctl create demo --shared
  contextctl create demo --shared -n 3 -s 5Gi
  contextctl create fast-run --warm-pool research-agents -n 3
  contextctl create readers --claim prepared-workspace --read-only -n 3
  contextctl create writer --claim prepared-workspace --read-write
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
