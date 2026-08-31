package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
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

Concepts:
  Context               Persistent agent data, such as a workspace, memory, or artifacts
  Sandbox pool          One or more isolated agent environments with workspace context
  Sandbox profile       Platform-managed runtime settings for sandbox Pods
  Storage class         Kubernetes storage available to contexts and sandboxes

Commands:
  health                Check the service
  status                Show Context Service and Kubernetes resources
  storage-class COMMAND Discover Kubernetes storage classes (alias: sc)
  context COMMAND       Create, list, show, or delete named contexts (alias: ctx)
  sandbox-pool COMMAND  Create, list, show, wait for, or delete sandbox pools (alias: sb)
  help [command]        Show help

Quick start:
  contextctl sb create demo --replicas 2 --shared
  contextctl sb wait demo
  contextctl status
  contextctl sb delete demo

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
	case "status":
		return showStatus(c, args[1:])
	case "storage-class", "sc":
		return storageClassCommand(c, args[1:])
	case "storage-classes":
		return errors.New("storage-classes moved; use 'contextctl storage-class list'")
	case "context", "ctx":
		return contextCommand(c, args[1:])
	case "sandbox-pool", "sb":
		return sandboxPoolCommand(c, args[1:])
	case "create", "get", "wait", "rm", "delete":
		return fmt.Errorf("%q requires a resource; use 'contextctl sandbox-pool %s'", args[0], args[0])
	default:
		return fmt.Errorf("unknown command %q; run 'contextctl help'", args[0])
	}
}

type statusView struct {
	Namespace      string                     `json:"namespace"`
	SandboxPools   []pool.Pool                `json:"sandboxPools"`
	Contexts       []contextresource.Resource `json:"contexts"`
	StorageClasses []storageclass.Resource    `json:"storageClasses"`
}

func showStatus(c *client.Client, args []string) error {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	namespace := flags.String("namespace", envOr("CS_NAMESPACE", "serverless-harness"), "Kubernetes namespace")
	jsonOutput := flags.Bool("json", false, "print JSON")
	flags.Usage = func() { showHelp([]string{"status"}) }
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		flags.Usage()
		return errors.New("status does not accept arguments")
	}

	view, err := loadStatus(c, *namespace)
	if err != nil {
		return err
	}
	if *jsonOutput {
		encoded, _ := json.MarshalIndent(view, "", "  ")
		fmt.Println(string(encoded))
		return nil
	}

	fmt.Printf("Context Service · namespace: %s\n", view.Namespace)
	writePools(os.Stdout, view.SandboxPools)
	writeContexts(os.Stdout, view.Contexts)
	writeStorageClasses(os.Stdout, view.StorageClasses)
	return nil
}

func loadStatus(c *client.Client, namespace string) (statusView, error) {
	pools, err := c.List(context.Background())
	if err != nil {
		return statusView{}, err
	}
	contexts, err := c.ListContexts(context.Background(), namespace)
	if err != nil {
		return statusView{}, err
	}
	classes, err := c.ListStorageClasses(context.Background())
	if err != nil {
		return statusView{}, err
	}
	return statusView{
		Namespace: namespace, SandboxPools: pools, Contexts: contexts, StorageClasses: classes,
	}, nil
}

func storageClassCommand(c *client.Client, args []string) error {
	if len(args) == 0 {
		showHelp([]string{"storage-class"})
		return errors.New("a storage-class command is required")
	}
	switch args[0] {
	case "help", "-h", "--help":
		showHelp([]string{"storage-class"})
		return nil
	case "list":
		return listStorageClasses(c, args[1:])
	default:
		return fmt.Errorf("unknown storage-class command %q; run 'contextctl help storage-class'", args[0])
	}
}

func listStorageClasses(c *client.Client, args []string) error {
	flags := flag.NewFlagSet("storage-class list", flag.ContinueOnError)
	jsonOutput := flags.Bool("json", false, "print JSON")
	flags.Usage = func() { showHelp([]string{"storage-class", "list"}) }
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		flags.Usage()
		return errors.New("storage-class list does not accept arguments")
	}
	items, err := c.ListStorageClasses(context.Background())
	if err != nil {
		return err
	}
	printStorageClasses(items, *jsonOutput)
	return nil
}

func sandboxPoolCommand(c *client.Client, args []string) error {
	if len(args) == 0 {
		showHelp([]string{"sandbox-pool"})
		return errors.New("a sandbox-pool command is required")
	}
	switch args[0] {
	case "help", "-h", "--help":
		showHelp([]string{"sandbox-pool"})
		return nil
	case "create":
		return createSandboxPool(c, args[1:])
	case "list":
		return listSandboxPools(c, args[1:])
	case "get":
		return getSandboxPool(c, args[1:])
	case "wait":
		return waitForSandboxPool(c, args[1:])
	case "delete", "rm":
		return deleteSandboxPool(c, args[1:])
	default:
		return fmt.Errorf("unknown sandbox-pool command %q; run 'contextctl help sandbox-pool'", args[0])
	}
}

func listSandboxPools(c *client.Client, args []string) error {
	flags := flag.NewFlagSet("sandbox-pool list", flag.ContinueOnError)
	jsonOutput := flags.Bool("json", false, "print JSON")
	flags.Usage = func() { showHelp([]string{"sandbox-pool", "list"}) }
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		flags.Usage()
		return errors.New("sandbox-pool list does not accept arguments")
	}
	items, err := c.List(context.Background())
	if err != nil {
		return err
	}
	printPools(items, *jsonOutput)
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
	case "delete", "rm":
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
	flags := flag.NewFlagSet("context delete", flag.ContinueOnError)
	namespace := flags.String("namespace", envOr("CS_NAMESPACE", "serverless-harness"), "Kubernetes namespace")
	flags.Usage = func() { showHelp([]string{"context", "delete"}) }
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

func createSandboxPool(c *client.Client, args []string) error {
	flags := flag.NewFlagSet("sandbox-pool create", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	replicas := 1
	flags.IntVar(&replicas, "replicas", 1, "number of sandboxes")
	flags.IntVar(&replicas, "n", 1, "number of sandboxes (shorthand)")
	size := "1Gi"
	flags.StringVar(&size, "workspace-size", "1Gi", "workspace size")
	flags.StringVar(&size, "s", "1Gi", "workspace size (shorthand)")
	storageClass := os.Getenv("CS_STORAGE_CLASS")
	flags.StringVar(&storageClass, "storage-class", storageClass, "storage class")
	flags.StringVar(&storageClass, "c", storageClass, "storage class (shorthand)")
	shared := flags.Bool("shared", false, "use one RWX workspace shared by all sandboxes")
	claimName := flags.String("claim", "", "mount an existing PVC instead of creating workspace storage")
	sandboxProfile := flags.String("sandbox-profile", "", "use an existing SandboxTemplate")
	warmPoolRef := flags.String("warm-pool", "", "claim ready sandboxes from an existing SandboxWarmPool")
	readOnly := flags.Bool("read-only", false, "mount the existing claim read-only")
	readWrite := flags.Bool("read-write", false, "mount the existing claim read-write")
	jsonOutput := flags.Bool("json", false, "print JSON")
	flags.Usage = func() { showHelp([]string{"sandbox-pool", "create"}) }
	name, err := parseName(flags, args)
	if err != nil {
		return err
	}
	workspace := pool.Workspace{Size: size, AccessMode: "ReadWriteOnce", StorageClass: storageClass}
	if *warmPoolRef != "" {
		if *sandboxProfile != "" {
			return errors.New("--sandbox-profile cannot be combined with --warm-pool; the warm pool already selects a template")
		}
		incompatible := map[string]bool{
			"shared": false, "claim": false, "read-only": false, "read-write": false,
			"workspace-size": false, "s": false, "storage-class": false, "c": false,
		}
		flags.Visit(func(candidate *flag.Flag) {
			if _, found := incompatible[candidate.Name]; found {
				incompatible[candidate.Name] = true
			}
		})
		for _, option := range []string{"shared", "claim", "read-only", "read-write", "workspace-size", "s", "storage-class", "c"} {
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
	}
	result, err := c.Create(context.Background(), pool.CreateRequest{
		Name: name, Replicas: replicas, SandboxProfile: *sandboxProfile, WarmPoolRef: *warmPoolRef,
		Workspace: workspace,
	})
	if err != nil {
		return err
	}
	printPool(result, *jsonOutput)
	return nil
}

func getSandboxPool(c *client.Client, args []string) error {
	flags := flag.NewFlagSet("sandbox-pool get", flag.ContinueOnError)
	jsonOutput := flags.Bool("json", false, "print JSON")
	flags.Usage = func() { showHelp([]string{"sandbox-pool", "get"}) }
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

func waitForSandboxPool(c *client.Client, args []string) error {
	flags := flag.NewFlagSet("sandbox-pool wait", flag.ContinueOnError)
	timeout := 2 * time.Minute
	flags.DurationVar(&timeout, "timeout", timeout, "maximum wait time")
	flags.DurationVar(&timeout, "t", timeout, "maximum wait time (shorthand)")
	flags.Usage = func() { showHelp([]string{"sandbox-pool", "wait"}) }
	name, err := parseName(flags, args)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
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

func deleteSandboxPool(c *client.Client, args []string) error {
	flags := flag.NewFlagSet("sandbox-pool delete", flag.ContinueOnError)
	flags.Usage = func() { showHelp([]string{"sandbox-pool", "delete"}) }
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
	// leading name to the end so both "sandbox-pool create demo --shared" and
	// "sandbox-pool create --shared demo" work as users expect.
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
	writePools(os.Stdout, []pool.Pool{value})
}

func printPools(items []pool.Pool, jsonOutput bool) {
	if jsonOutput {
		encoded, _ := json.MarshalIndent(items, "", "  ")
		fmt.Println(string(encoded))
		return
	}
	writePools(os.Stdout, items)
}

func writePools(w io.Writer, items []pool.Pool) {
	fmt.Fprintf(w, "\nSANDBOX POOLS (%d)\n", len(items))
	if len(items) == 0 {
		fmt.Fprintln(w, "None")
		return
	}
	for index, item := range items {
		if index > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "%s  %s · %d/%d · %s\n", item.Name, displayStatus(item.Status),
			item.ReadyReplicas, item.Replicas, workspaceDescription(item))
		writePoolResources(w, item)
	}
}

type poolResourceNode struct {
	resource pool.KubernetesResource
	children []string
}

func writePoolResources(w io.Writer, value pool.Pool) {
	used := make(map[int]bool)
	nodes := make([]poolResourceNode, 0, len(value.Resources))
	for index, resource := range value.Resources {
		if resource.Kind != "sandbox" {
			continue
		}
		used[index] = true
		node := poolResourceNode{resource: resource}
		if podIndex := resourceIndex(value.Resources, "pod", resource.Name); podIndex >= 0 {
			pod := value.Resources[podIndex]
			used[podIndex] = true
			node.children = append(node.children, fmt.Sprintf("pod/%s  %s", pod.Name, displayStatus(pod.Status)))
		}
		if pvcName := workspacePVCName(value, resource.Name); pvcName != "" {
			if pvcIndex := resourceIndex(value.Resources, "pvc", pvcName); pvcIndex >= 0 {
				pvc := value.Resources[pvcIndex]
				used[pvcIndex] = true
				node.children = append(node.children, fmt.Sprintf("workspace → pvc/%s  %s", pvc.Name, displayStatus(pvc.Status)))
			}
		}
		nodes = append(nodes, node)
	}
	for index, resource := range value.Resources {
		if !used[index] {
			nodes = append(nodes, poolResourceNode{resource: resource})
		}
	}

	for index, node := range nodes {
		lastNode := index == len(nodes)-1
		connector := "├──"
		childIndent := "│   "
		if lastNode {
			connector = "└──"
			childIndent = "    "
		}
		fmt.Fprintf(w, "%s %s/%s  %s\n", connector, node.resource.Kind, node.resource.Name,
			displayStatus(node.resource.Status))
		for childIndex, child := range node.children {
			childConnector := "├──"
			if childIndex == len(node.children)-1 {
				childConnector = "└──"
			}
			fmt.Fprintf(w, "%s%s %s\n", childIndent, childConnector, child)
		}
	}
}

func resourceIndex(resources []pool.KubernetesResource, kind, name string) int {
	for index, resource := range resources {
		if resource.Kind == kind && resource.Name == name {
			return index
		}
	}
	return -1
}

func workspacePVCName(value pool.Pool, sandboxName string) string {
	if value.Workspace.ClaimName != "" {
		return value.Workspace.ClaimName
	}
	if value.Workspace.AccessMode == "ReadWriteMany" {
		return value.Name + "-workspace"
	}
	prefix := "sandbox-" + value.Name + "-"
	index, found := strings.CutPrefix(sandboxName, prefix)
	if !found {
		return ""
	}
	return value.Name + "-workspace-" + index
}

func printStorageClasses(items []storageclass.Resource, jsonOutput bool) {
	if jsonOutput {
		encoded, _ := json.MarshalIndent(items, "", "  ")
		fmt.Println(string(encoded))
		return
	}
	writeStorageClasses(os.Stdout, items)
}

func writeStorageClasses(w io.Writer, items []storageclass.Resource) {
	fmt.Fprintf(w, "\nSTORAGE CLASSES (%d)\n", len(items))
	if len(items) == 0 {
		fmt.Fprintln(w, "None")
		return
	}
	writer := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, item := range items {
		name := item.Name
		if item.Default {
			name += " (default)"
		}
		fmt.Fprintf(writer, "%s\t%s · %s\n", name, item.Provisioner, item.VolumeBindingMode)
	}
	_ = writer.Flush()
}

func printContexts(items []contextresource.Resource, jsonOutput bool) {
	if jsonOutput {
		encoded, _ := json.MarshalIndent(items, "", "  ")
		fmt.Println(string(encoded))
		return
	}
	writeContexts(os.Stdout, items)
}

func writeContexts(w io.Writer, items []contextresource.Resource) {
	fmt.Fprintf(w, "\nCONTEXTS (%d)\n", len(items))
	if len(items) == 0 {
		fmt.Fprintln(w, "None")
		return
	}
	for index, item := range items {
		if index > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "%s  %s · %s\n", item.Name, displayStatus(item.Status), item.Type)
		fmt.Fprintf(w, "└── %s/%s  %s %s · %s\n", strings.ToLower(item.Attachment.Kind),
			item.Attachment.ClaimName, item.Storage.Size, shortMode(item.Storage.AccessMode),
			storageClassName(item.Storage.StorageClass))
	}
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

func workspaceDescription(value pool.Pool) string {
	if value.WarmPoolRef != "" {
		return "warm pool " + value.WarmPoolRef
	}
	topology := "dedicated"
	if value.Workspace.ClaimName != "" {
		topology = "existing"
	} else if value.Workspace.AccessMode == "ReadWriteMany" {
		topology = "shared"
	}
	description := fmt.Sprintf("%s · %s", topology, workspaceSummary(value))
	if value.SandboxProfile != "" {
		description += " · profile " + value.SandboxProfile
	}
	return description
}

func displayStatus(value string) string {
	if value == "" {
		return "Unknown"
	}
	return strings.ToUpper(value[:1]) + value[1:]
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
	case "storage-class", "sc":
		if len(args) == 1 {
			fmt.Print(`Usage: contextctl storage-class COMMAND [options]

Alias: contextctl sc

Commands:
  list                 List available Kubernetes storage classes
`)
			return
		}
		switch args[1] {
		case "list":
			fmt.Print("Usage: contextctl storage-class list [--json]\n")
		default:
			fmt.Printf("Unknown storage-class command %q.\n", args[1])
		}
	case "context", "ctx":
		if len(args) == 1 {
			fmt.Print(`Usage: contextctl context COMMAND [options]

Alias: contextctl ctx

Commands:
  create NAME          Create a named context
  list                 List named contexts
  get NAME             Show a named context
  delete NAME          Delete a named context (alias: rm)

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
		case "delete", "rm":
			fmt.Print("Usage: contextctl context delete NAME [--namespace NAME]\n")
		default:
			fmt.Printf("Unknown context command %q.\n", args[1])
		}
	case "sandbox-pool", "sb":
		if len(args) == 1 {
			fmt.Print(`Usage: contextctl sandbox-pool COMMAND [options]

Alias: contextctl sb

Commands:
  create NAME          Create a sandbox pool
  list                 List sandbox pools
  get NAME             Show a sandbox pool
  wait NAME            Wait until every sandbox is ready
  delete NAME          Delete a sandbox pool (alias: rm)

Run "contextctl help sandbox-pool COMMAND" for command options.
`)
			return
		}
		switch args[1] {
		case "create":
			fmt.Print(`Usage: contextctl sandbox-pool create NAME [options]

Create one sandbox with a 1Gi RWO workspace by default.

Options:
  --sandbox-profile NAME Use an existing SandboxTemplate for the runtime
  --shared             Use one RWX workspace shared by all sandboxes
  --warm-pool NAME      Claim sandboxes from an existing SandboxWarmPool
  --claim NAME          Mount an existing PVC; CS will not delete it
  --read-only           Mount the existing claim read-only (requires --claim)
  --read-write          Mount the existing claim read-write (requires --claim)
  --replicas NUMBER     Number of sandboxes (default 1; shorthand: -n)
  --workspace-size SIZE Workspace size (default 1Gi; shorthand: -s)
  --storage-class NAME  Storage class (default CS_STORAGE_CLASS; shorthand: -c)
  --json               Print JSON

Examples:
  contextctl sandbox-pool create demo
  contextctl sandbox-pool create developer --sandbox-profile shell
  contextctl sandbox-pool create demo --shared --replicas 2
  contextctl sandbox-pool create review --shared --replicas 3 --workspace-size 5Gi
  contextctl sandbox-pool create fast-run --warm-pool research-agents --replicas 3
  contextctl sandbox-pool create readers --claim prepared-workspace --read-only --replicas 3
  contextctl sandbox-pool create writer --claim prepared-workspace --read-write
`)
		case "list":
			fmt.Print("Usage: contextctl sandbox-pool list [--json]\n")
		case "get":
			fmt.Print("Usage: contextctl sandbox-pool get NAME [--json]\n")
		case "wait":
			fmt.Print("Usage: contextctl sandbox-pool wait NAME [--timeout 2m]\n\nWait until every sandbox is ready.\n")
		case "delete", "rm":
			fmt.Print("Usage: contextctl sandbox-pool delete NAME\n\nDelete the pool's sandboxes and managed workspace.\n")
		default:
			fmt.Printf("Unknown sandbox-pool command %q.\n", args[1])
		}
	case "health":
		fmt.Print("Usage: contextctl health\n")
	case "status":
		fmt.Print("Usage: contextctl status [--namespace NAME] [--json]\n")
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
