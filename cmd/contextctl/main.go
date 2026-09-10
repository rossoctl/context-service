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
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/rossoctl/context-service/internal/client"
	"github.com/rossoctl/context-service/internal/contextresource"
	"github.com/rossoctl/context-service/internal/contextsync"
	"github.com/rossoctl/context-service/internal/localcontext"
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
  CS_SUBJECT             Authenticated identity as kind:name (default user:anonymous)
  CS_NAMESPACE           Default context namespace (default serverless-harness)
  CS_STORAGE_CLASS       Default storage class for create
  CS_CONTEXT_HOME        Local context directory (default ~/.contexts)

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
	c.SetSubject(os.Getenv("CS_SUBJECT"))
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
	case "hook":
		return hookCommand(args[1:], os.Stdin)
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
	case "capture":
		return captureContext(args[1:])
	case "restore":
		return restoreContext(args[1:])
	case "attach":
		return attachContext(args[1:])
	case "detach":
		return detachContext(args[1:])
	case "export":
		return exportContext(args[1:])
	case "import":
		return importContext(args[1:])
	case "sync":
		return syncContext(c, args[1:])
	case "backup":
		return backupCommand(c, args[1:])
	case "derive":
		return deriveContext(args[1:])
	case "search":
		return searchContext(args[1:])
	case "revisions":
		return revisionsContext(c, args[1:])
	case "lineage":
		return lineageContext(args[1:])
	case "access":
		return contextAccess(c, args[1:])
	case "grant":
		return contextGrant(c, args[1:])
	case "grants":
		return contextGrants(c, args[1:])
	case "revoke":
		return contextRevoke(c, args[1:])
	case "consumers":
		return contextConsumers(c, args[1:])
	case "audit":
		return contextAudit(c, args[1:])
	default:
		return fmt.Errorf("unknown context command %q; run 'contextctl help context'", args[0])
	}
}

func createContext(c *client.Client, args []string) error {
	flags := flag.NewFlagSet("context create", flag.ContinueOnError)
	backend := flags.String("backend", "pvc", "storage backend: pvc or filesystem")
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
	if *backend == "filesystem" || *backend == "local" {
		result, err := localContextStore().Create(name, *contextType)
		if err != nil {
			return err
		}
		printLocalContext(result, *jsonOutput)
		return nil
	}
	if *backend != "pvc" {
		return fmt.Errorf("unsupported context backend %q; use pvc or filesystem", *backend)
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
	backend := flags.String("backend", "all", "storage backend: all, pvc, or filesystem")
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
	if *backend == "filesystem" || *backend == "local" {
		items, err := localContextStore().List()
		if err != nil {
			return err
		}
		printLocalContexts(items, *jsonOutput)
		return nil
	}
	if *backend == "all" {
		localItems, err := localContextStore().List()
		if err != nil {
			return err
		}
		kubernetesItems, kubernetesErr := c.ListContexts(context.Background(), *namespace)
		warning := ""
		if kubernetesErr != nil {
			warning = "Kubernetes contexts unavailable: " + kubernetesErr.Error()
		}
		printContextInventory(localItems, kubernetesItems, warning, *jsonOutput)
		return nil
	}
	if *backend != "pvc" {
		return fmt.Errorf("unsupported context backend %q; use all, pvc, or filesystem", *backend)
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
	backend := flags.String("backend", "pvc", "storage backend: pvc or filesystem")
	namespace := flags.String("namespace", envOr("CS_NAMESPACE", "serverless-harness"), "Kubernetes namespace")
	jsonOutput := flags.Bool("json", false, "print JSON")
	flags.Usage = func() { showHelp([]string{"context", "get"}) }
	name, err := parseContextName(flags, args)
	if err != nil {
		return err
	}
	if *backend == "filesystem" || *backend == "local" {
		result, err := localContextStore().Get(name)
		if err != nil {
			return err
		}
		printLocalContext(result, *jsonOutput)
		return nil
	}
	if *backend != "pvc" {
		return fmt.Errorf("unsupported context backend %q; use pvc or filesystem", *backend)
	}
	result, err := c.GetContext(context.Background(), *namespace, name)
	if err != nil {
		return err
	}
	printContext(result, *jsonOutput)
	return nil
}

func captureContext(args []string) error {
	flags := flag.NewFlagSet("context capture", flag.ContinueOnError)
	harness := flags.String("harness", "claude", "agent harness")
	project := flags.String("project", ".", "project directory")
	jsonOutput := flags.Bool("json", false, "print JSON")
	flags.Usage = func() { showHelp([]string{"context", "capture"}) }
	name, err := parseContextName(flags, args)
	if err != nil {
		return err
	}
	if *harness != "claude" {
		return fmt.Errorf("unsupported harness %q; only claude is available in this demo", *harness)
	}
	store := localContextStore()
	capture, err := store.CaptureClaude(name, *project, claudeConfigHome())
	if err != nil {
		return err
	}
	_ = notifyBackup(store, name)
	if *jsonOutput {
		encoded, _ := json.MarshalIndent(capture, "", "  ")
		fmt.Println(string(encoded))
		return nil
	}
	fmt.Printf("Captured Claude state in %s\n", name)
	fmt.Printf("Project:  %s\n", capture.Project)
	fmt.Printf("Sessions: %d\n", capture.Sessions)
	fmt.Printf("Files:    %d (%s)\n", capture.Files, formatBytes(capture.Bytes))
	return nil
}

func restoreContext(args []string) error {
	flags := flag.NewFlagSet("context restore", flag.ContinueOnError)
	harness := flags.String("harness", "claude", "agent harness")
	project := flags.String("project", ".", "destination project directory")
	jsonOutput := flags.Bool("json", false, "print JSON")
	flags.Usage = func() { showHelp([]string{"context", "restore"}) }
	name, err := parseContextName(flags, args)
	if err != nil {
		return err
	}
	if *harness != "claude" {
		return fmt.Errorf("unsupported harness %q; only claude is available in this demo", *harness)
	}
	capture, destination, err := localContextStore().RestoreClaude(name, *project, claudeConfigHome())
	if err != nil {
		return err
	}
	if *jsonOutput {
		encoded, _ := json.MarshalIndent(struct {
			localcontext.Capture
			Destination string `json:"destination"`
		}{Capture: capture, Destination: destination}, "", "  ")
		fmt.Println(string(encoded))
		return nil
	}
	fmt.Printf("Restored Claude state to %s\n", destination)
	fmt.Printf("Sessions: %d\n", capture.Sessions)
	fmt.Printf("Next:     cd %s && claude --resume\n", *project)
	return nil
}

func attachContext(args []string) error {
	flags := flag.NewFlagSet("context attach", flag.ContinueOnError)
	harness := flags.String("harness", "claude", "agent harness")
	project := flags.String("project", ".", "project directory")
	jsonOutput := flags.Bool("json", false, "print JSON")
	flags.Usage = func() { showHelp([]string{"context", "attach"}) }
	name, err := parseContextName(flags, args)
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate contextctl: %w", err)
	}
	var attachment localcontext.Attachment
	switch *harness {
	case "claude":
		attachment, err = localContextStore().AttachClaude(name, *project, executable)
	case "codex":
		attachment, err = localContextStore().AttachCodex(name, *project, executable)
	case "opencode":
		opencodeExecutable, lookupErr := exec.LookPath("opencode")
		if lookupErr != nil {
			return errors.New("opencode is not installed or not on PATH")
		}
		attachment, err = localContextStore().AttachOpenCode(name, *project, executable, opencodeExecutable)
	case "pi":
		attachment, err = localContextStore().AttachPi(name, *project, executable)
	default:
		return fmt.Errorf("unsupported harness %q; use claude, codex, opencode, or pi", *harness)
	}
	if err != nil {
		return err
	}
	if *jsonOutput {
		encoded, _ := json.MarshalIndent(attachment, "", "  ")
		fmt.Println(string(encoded))
		return nil
	}
	fmt.Printf("Attached %s to %s in %s\n", name, displayHarness(*harness), attachment.Project)
	fmt.Println("State will be captured automatically after completed responses.")
	fmt.Printf("Next: %s\n", *harness)
	if *harness == "codex" {
		fmt.Println("In Codex, open /hooks once to review and trust the new project hooks.")
	}
	return nil
}

func detachContext(args []string) error {
	flags := flag.NewFlagSet("context detach", flag.ContinueOnError)
	harness := flags.String("harness", "claude", "agent harness")
	project := flags.String("project", "", "override attached project directory")
	jsonOutput := flags.Bool("json", false, "print JSON")
	flags.Usage = func() { showHelp([]string{"context", "detach"}) }
	name, err := parseContextName(flags, args)
	if err != nil {
		return err
	}
	var attachment localcontext.Attachment
	switch *harness {
	case "claude":
		attachment, err = localContextStore().DetachClaude(name, *project)
	case "codex":
		attachment, err = localContextStore().DetachCodex(name, *project)
	case "opencode":
		attachment, err = localContextStore().DetachOpenCode(name, *project)
	case "pi":
		attachment, err = localContextStore().DetachPi(name, *project)
	default:
		return fmt.Errorf("unsupported harness %q; use claude, codex, opencode, or pi", *harness)
	}
	if err != nil {
		return err
	}
	if *jsonOutput {
		encoded, _ := json.MarshalIndent(attachment, "", "  ")
		fmt.Println(string(encoded))
		return nil
	}
	fmt.Printf("Detached %s from %s in %s\n", name, displayHarness(*harness), attachment.Project)
	return nil
}

func exportContext(args []string) error {
	flags := flag.NewFlagSet("context export", flag.ContinueOnError)
	output := flags.String("output", "", "output .context file")
	jsonOutput := flags.Bool("json", false, "print JSON")
	flags.Usage = func() { showHelp([]string{"context", "export"}) }
	name, err := parseContextName(flags, args)
	if err != nil {
		return err
	}
	bundle, err := localContextStore().Export(name, *output)
	if err != nil {
		return err
	}
	if *jsonOutput {
		encoded, _ := json.MarshalIndent(bundle, "", "  ")
		fmt.Println(string(encoded))
		return nil
	}
	fmt.Printf("Exported %s to %s\n", name, bundle.File)
	fmt.Printf("Files: %d (%s)\n", bundle.Files, formatBytes(bundle.Bytes))
	return nil
}

func importContext(args []string) error {
	flags := flag.NewFlagSet("context import", flag.ContinueOnError)
	name := flags.String("name", "", "override context name")
	jsonOutput := flags.Bool("json", false, "print JSON")
	flags.Usage = func() { showHelp([]string{"context", "import"}) }
	bundlePath, err := parseSingleArgument(flags, args, "context bundle file")
	if err != nil {
		return err
	}
	manifest, err := localContextStore().Import(bundlePath, *name)
	if err != nil {
		return err
	}
	if *jsonOutput {
		encoded, _ := json.MarshalIndent(manifest, "", "  ")
		fmt.Println(string(encoded))
		return nil
	}
	fmt.Printf("Imported %s from %s\n", manifest.Name, bundlePath)
	printLocalContext(manifest, false)
	return nil
}

func syncContext(c *client.Client, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		showHelp([]string{"context", "sync"})
		return nil
	}
	switch args[0] {
	case "push":
		return pushContext(c, args[1:])
	case "pull":
		return pullContext(c, args[1:])
	default:
		return fmt.Errorf("unknown sync direction %q; use push or pull", args[0])
	}
}

func pushContext(c *client.Client, args []string) error {
	return pushContextWithContext(context.Background(), c, args)
}

func pushContextWithContext(ctx context.Context, c *client.Client, args []string) error {
	flags := flag.NewFlagSet("context sync push", flag.ContinueOnError)
	remoteName := flags.String("remote-name", "", "remote context name (default local name)")
	target := flags.String("to", "", "S3 destination (s3://bucket/prefix)")
	namespace := flags.String("namespace", envOr("CS_NAMESPACE", "serverless-harness"), "Kubernetes namespace")
	image := flags.String("helper-image", "busybox:1.36", "temporary transfer Pod image")
	s3Endpoint := flags.String("s3-endpoint", os.Getenv("CS_S3_ENDPOINT"), "S3-compatible endpoint")
	s3Region := flags.String("s3-region", envOr("CS_S3_REGION", envOr("AWS_REGION", "us-east-1")), "S3 region")
	flags.Usage = func() { showHelp([]string{"context", "sync", "push"}) }
	name, err := parseContextName(flags, args)
	if err != nil {
		return err
	}
	local, err := localContextStore().Get(name)
	if err != nil {
		return err
	}
	if local.CurrentRevision == "" {
		if _, err := localContextStore().PublishRevision(name, localcontext.RevisionMetadata{Operation: "sync", Producer: "contextctl"}); err != nil {
			return err
		}
		local, err = localContextStore().Get(name)
		if err != nil {
			return err
		}
	}
	if *target != "" {
		if *remoteName != "" {
			return errors.New("--to cannot be combined with --remote-name")
		}
		location, err := contextsync.ParseS3Location(*target)
		if err != nil {
			return err
		}
		temporary, err := os.MkdirTemp("", "contextctl-sync-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(temporary)
		bundlePath := filepath.Join(temporary, name+".context")
		bundle, err := localContextStore().Export(name, bundlePath)
		if err != nil {
			return err
		}
		transport, err := contextsync.NewS3(ctx, *s3Region, *s3Endpoint)
		if err != nil {
			return err
		}
		transport.SetProgress(newTransferProgress(os.Stderr).Update)
		if _, err := transport.Push(ctx, location, bundlePath); err != nil {
			return err
		}
		fmt.Printf("Synced %s to %s\n", name, location)
		fmt.Printf("Files: %d (%s)\n", bundle.Files, formatBytes(bundle.Bytes))
		return nil
	}
	if *remoteName == "" {
		*remoteName = name
	}
	remote, err := c.GetContext(ctx, *namespace, *remoteName)
	if err != nil {
		return fmt.Errorf("get remote context %s: %w", *remoteName, err)
	}
	if remote.Attachment.Kind != "pvc" || remote.Attachment.ClaimName == "" {
		return fmt.Errorf("remote context %s is not backed by a PVC", *remoteName)
	}
	localType := local.Type
	if localType == "history" {
		localType = "state"
	}
	if remote.Type != localType {
		return fmt.Errorf("context type mismatch: local %s is %s, remote %s is %s", name, localType, *remoteName, remote.Type)
	}

	temporary, err := os.MkdirTemp("", "contextctl-sync-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	bundlePath := filepath.Join(temporary, name+".context")
	bundle, err := localContextStore().Export(name, bundlePath)
	if err != nil {
		return err
	}
	kubectl, err := exec.LookPath("kubectl")
	if err != nil {
		return errors.New("kubectl is required for Kubernetes context sync")
	}
	transport := contextsync.NewKubectl(kubectl, *image)
	transport.SetProgress(newTransferProgress(os.Stderr).Update)
	if err := transport.Push(ctx, remote.Namespace, remote.Attachment.ClaimName, bundlePath); err != nil {
		return err
	}
	if len(local.Revisions) == 0 {
		return errors.New("local context has no published revision")
	}
	if _, err := c.PublishContextRevision(ctx, remote.Namespace, *remoteName, resourceRevision(local.Revisions[len(local.Revisions)-1])); err != nil {
		return fmt.Errorf("publish remote revision: %w", err)
	}
	fmt.Printf("Synced %s to pvc/%s in namespace %s\n", name, remote.Attachment.ClaimName, remote.Namespace)
	fmt.Printf("Files: %d (%s)\n", bundle.Files, formatBytes(bundle.Bytes))
	return nil
}

func pullContext(c *client.Client, args []string) error {
	flags := flag.NewFlagSet("context sync pull", flag.ContinueOnError)
	name := flags.String("name", "", "local context name (default remote name)")
	namespace := flags.String("namespace", envOr("CS_NAMESPACE", "serverless-harness"), "Kubernetes namespace")
	image := flags.String("helper-image", "busybox:1.36", "temporary transfer Pod image")
	s3Endpoint := flags.String("s3-endpoint", os.Getenv("CS_S3_ENDPOINT"), "S3-compatible endpoint")
	s3Region := flags.String("s3-region", envOr("CS_S3_REGION", envOr("AWS_REGION", "us-east-1")), "S3 region")
	flags.Usage = func() { showHelp([]string{"context", "sync", "pull"}) }
	remoteName, err := parseContextName(flags, args)
	if err != nil {
		return err
	}
	var s3Location *contextsync.S3Location
	if strings.HasPrefix(remoteName, "s3://") {
		location, err := contextsync.ParseS3Location(remoteName)
		if err != nil {
			return err
		}
		s3Location = &location
		if *name == "" {
			*name = filepath.Base(location.Prefix)
			if *name == "." || *name == "/" || *name == "" {
				return errors.New("--name is required when pulling from an S3 bucket root")
			}
		}
	} else if *name == "" {
		*name = remoteName
	}
	store := localContextStore()
	if _, err := store.Get(*name); err == nil {
		return fmt.Errorf("local context %s: %w", *name, localcontext.ErrAlreadyExists)
	} else if !errors.Is(err, localcontext.ErrNotFound) {
		return err
	}
	if s3Location != nil {
		temporary, err := os.MkdirTemp("", "contextctl-sync-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(temporary)
		bundlePath := filepath.Join(temporary, *name+".context")
		transport, err := contextsync.NewS3(context.Background(), *s3Region, *s3Endpoint)
		if err != nil {
			return err
		}
		transport.SetProgress(newTransferProgress(os.Stderr).Update)
		if err := transport.Pull(context.Background(), *s3Location, bundlePath); err != nil {
			return err
		}
		manifest, err := store.Import(bundlePath, *name)
		if err != nil {
			return err
		}
		fmt.Printf("Synced %s to local context %s\n", s3Location, manifest.Name)
		printLocalContext(manifest, false)
		return nil
	}
	remote, err := c.GetContext(context.Background(), *namespace, remoteName)
	if err != nil {
		return fmt.Errorf("get remote context %s: %w", remoteName, err)
	}
	if remote.Attachment.Kind != "pvc" || remote.Attachment.ClaimName == "" {
		return fmt.Errorf("remote context %s is not backed by a PVC", remoteName)
	}

	temporary, err := os.MkdirTemp("", "contextctl-sync-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	bundlePath := filepath.Join(temporary, remoteName+".context")
	kubectl, err := exec.LookPath("kubectl")
	if err != nil {
		return errors.New("kubectl is required for Kubernetes context sync")
	}
	transport := contextsync.NewKubectl(kubectl, *image)
	transport.SetProgress(newTransferProgress(os.Stderr).Update)
	if err := transport.Pull(context.Background(), remote.Namespace, remote.Attachment.ClaimName, bundlePath); err != nil {
		return err
	}
	manifest, err := store.Import(bundlePath, *name)
	if err != nil {
		return err
	}
	fmt.Printf("Synced pvc/%s in namespace %s to local context %s\n", remote.Attachment.ClaimName, remote.Namespace, manifest.Name)
	printLocalContext(manifest, false)
	return nil
}

func hookCommand(args []string, input io.Reader) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Print(`contextctl hook is used internally by agent harness integrations.

To capture state automatically, run:
  contextctl ctx attach NAME --harness claude|codex|opencode|pi
`)
		return nil
	}
	switch args[0] {
	case "claude-capture", "claude-session-end":
		return claudeHookCommand(args[1:], input)
	case "codex-capture":
		return codexHookCommand(args[1:], input)
	case "opencode-capture":
		return openCodeHookCommand(args[1:])
	case "pi-capture":
		return piHookCommand(args[1:])
	default:
		return errors.New("unknown hook command; hooks are installed by 'contextctl ctx attach'")
	}

}

func claudeHookCommand(args []string, input io.Reader) error {
	flags := flag.NewFlagSet("hook claude-capture", flag.ContinueOnError)
	contextName := flags.String("context", "", "context name")
	project := flags.String("project", "", "project directory")
	contextHome := flags.String("context-home", "", "local context directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *contextName == "" || *project == "" {
		return errors.New("Claude hook requires --context and --project")
	}
	store := localContextStore()
	if *contextHome != "" {
		store = localcontext.New(*contextHome)
	}
	if _, err := store.CaptureClaudeHook(*contextName, *project, claudeConfigHome(), input); err != nil {
		return err
	}
	_ = notifyBackup(store, *contextName)
	return nil
}

func codexHookCommand(args []string, input io.Reader) error {
	flags := flag.NewFlagSet("hook codex-capture", flag.ContinueOnError)
	contextName := flags.String("context", "", "context name")
	project := flags.String("project", "", "project directory")
	contextHome := flags.String("context-home", "", "local context directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *contextName == "" || *project == "" {
		return errors.New("Codex hook requires --context and --project")
	}
	store := localContextStore()
	if *contextHome != "" {
		store = localcontext.New(*contextHome)
	}
	if _, _, err := store.CaptureCodexHook(*contextName, *project, input); err != nil {
		return err
	}
	_ = notifyBackup(store, *contextName)
	return nil
}

func openCodeHookCommand(args []string) error {
	flags := flag.NewFlagSet("hook opencode-capture", flag.ContinueOnError)
	contextName := flags.String("context", "", "context name")
	project := flags.String("project", "", "project directory")
	contextHome := flags.String("context-home", "", "local context directory")
	opencodeExecutable := flags.String("opencode", "opencode", "OpenCode executable")
	sessionID := flags.String("session", "", "OpenCode session ID")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *contextName == "" || *project == "" || *sessionID == "" {
		return errors.New("OpenCode hook requires --context, --project, and --session")
	}
	command := exec.Command(*opencodeExecutable, "export", *sessionID, "--pure")
	output, err := command.Output()
	if err != nil {
		return fmt.Errorf("export OpenCode session: %w", err)
	}
	store := localContextStore()
	if *contextHome != "" {
		store = localcontext.New(*contextHome)
	}
	if _, _, err = store.CaptureSessionExport(*contextName, "opencode", *project, *sessionID, strings.NewReader(string(output))); err != nil {
		return err
	}
	_ = notifyBackup(store, *contextName)
	return nil
}

func piHookCommand(args []string) error {
	flags := flag.NewFlagSet("hook pi-capture", flag.ContinueOnError)
	contextName := flags.String("context", "", "context name")
	project := flags.String("project", "", "project directory")
	contextHome := flags.String("context-home", "", "local context directory")
	sessionFile := flags.String("session-file", "", "Pi session file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *contextName == "" || *project == "" || *sessionFile == "" {
		return errors.New("Pi hook requires --context, --project, and --session-file")
	}
	store := localContextStore()
	if *contextHome != "" {
		store = localcontext.New(*contextHome)
	}
	if _, _, err := store.CaptureSessionFile(*contextName, "pi", *project, "", *sessionFile); err != nil {
		return err
	}
	_ = notifyBackup(store, *contextName)
	return nil
}

func displayHarness(value string) string {
	switch value {
	case "claude":
		return "Claude"
	case "codex":
		return "Codex"
	case "opencode":
		return "OpenCode"
	case "pi":
		return "Pi"
	default:
		return value
	}
}

func localContextStore() *localcontext.Store {
	return localcontext.New(localContextHome())
}

func localContextHome() string {
	if configured := os.Getenv("CS_CONTEXT_HOME"); configured != "" {
		return configured
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".contexts"
	}
	return filepath.Join(home, ".contexts")
}

func claudeConfigHome() string {
	if configured := os.Getenv("CLAUDE_CONFIG_DIR"); configured != "" {
		return configured
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".claude"
	}
	return filepath.Join(home, ".claude")
}

func printLocalContext(value localcontext.Manifest, jsonOutput bool) {
	if jsonOutput {
		encoded, _ := json.MarshalIndent(value, "", "  ")
		fmt.Println(string(encoded))
		return
	}
	fmt.Printf("%s  ready · %s · filesystem\n", value.Name, value.Type)
	fmt.Printf("└── path  %s\n", value.Path)
	printLocalDerivation(value)
	printLocalHarnesses(value)
}

func printLocalDerivation(value localcontext.Manifest) {
	if value.CurrentRevision != "" {
		fmt.Printf("    └── revision  %s\n", shortRevision(value.CurrentRevision))
	}
	if value.Type == "memory" {
		fmt.Printf("    └── memory  %s\n", memoryPath(value))
	}
	if value.Type == "knowledge" {
		fmt.Printf("    └── knowledge  %s\n", knowledgePath(value))
	}
	if value.Derivation != nil {
		if value.Derivation.SourceContext != "" {
			fmt.Printf("    └── source  %s@%s · %s\n", value.Derivation.SourceContext, shortRevision(value.Derivation.SourceRevision), value.Derivation.Generator)
		}
		for _, source := range value.Derivation.Sources {
			fmt.Printf("    └── source  %s@%s · %s\n", source.Context, shortRevision(source.Revision), value.Derivation.Generator)
		}
	}
}

func printLocalHarnesses(value localcontext.Manifest) {
	for _, harness := range []string{"claude", "codex", "opencode", "pi"} {
		if capture, ok := value.Captures[harness]; ok {
			fmt.Printf("    └── %s  %d sessions · %s\n", displayHarness(harness), capture.Sessions, formatBytes(capture.Bytes))
		}
		if attachment, ok := value.Attachments[harness]; ok {
			fmt.Printf("    └── attached to %s  %s\n", displayHarness(harness), attachment.Project)
		}
	}
}

func printContextInventory(localItems []localcontext.Manifest, kubernetesItems []contextresource.Resource, warning string, jsonOutput bool) {
	if jsonOutput {
		encoded, _ := json.MarshalIndent(struct {
			Filesystem []localcontext.Manifest    `json:"filesystem"`
			PVC        []contextresource.Resource `json:"pvc"`
			Warning    string                     `json:"warning,omitempty"`
		}{Filesystem: localItems, PVC: kubernetesItems, Warning: warning}, "", "  ")
		fmt.Println(string(encoded))
		return
	}

	fmt.Println("\nCONTEXTS")
	fmt.Printf("\nFILESYSTEM (%d)\n", len(localItems))
	if len(localItems) == 0 {
		fmt.Println("None")
	}
	for index, item := range localItems {
		if index > 0 {
			fmt.Println()
		}
		fmt.Printf("%s  Ready · %s\n", item.Name, item.Type)
		fmt.Printf("└── path  %s\n", item.Path)
		printLocalDerivation(item)
		printLocalHarnesses(item)
	}

	if warning != "" {
		fmt.Println("\nPVC (unavailable)")
		fmt.Println(warning)
		return
	}
	fmt.Printf("\nPVC (%d)\n", len(kubernetesItems))
	if len(kubernetesItems) == 0 {
		fmt.Println("None")
		return
	}
	for index, item := range kubernetesItems {
		if index > 0 {
			fmt.Println()
		}
		fmt.Printf("%s  %s · %s\n", item.Name, displayStatus(item.Status), item.Type)
		fmt.Printf("└── %s/%s  %s %s · %s · namespace %s\n",
			strings.ToLower(item.Attachment.Kind), item.Attachment.ClaimName,
			item.Storage.Size, shortMode(item.Storage.AccessMode),
			storageClassName(item.Storage.StorageClass), item.Namespace)
		if item.CurrentRevision != "" {
			fmt.Printf("    └── revision  %s\n", shortRevision(item.CurrentRevision))
		}
	}
}

func printLocalContexts(items []localcontext.Manifest, jsonOutput bool) {
	if jsonOutput {
		encoded, _ := json.MarshalIndent(items, "", "  ")
		fmt.Println(string(encoded))
		return
	}
	fmt.Printf("\nFILESYSTEM CONTEXTS (%d)\n", len(items))
	if len(items) == 0 {
		fmt.Println("None")
		return
	}
	for _, item := range items {
		printLocalContext(item, false)
	}
}

func formatBytes(value int64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%d B", value)
	}
	divisor, exponent := int64(unit), 0
	for quotient := value / unit; quotient >= unit; quotient /= unit {
		divisor *= unit
		exponent++
	}
	return fmt.Sprintf("%.1f %ciB", float64(value)/float64(divisor), "KMGTPE"[exponent])
}

type transferProgress struct {
	mu          sync.Mutex
	output      io.Writer
	interactive bool
	last        time.Time
}

func newTransferProgress(output io.Writer) *transferProgress {
	progress := &transferProgress{output: output}
	if file, ok := output.(*os.File); ok {
		if info, err := file.Stat(); err == nil {
			progress.interactive = info.Mode()&os.ModeCharDevice != 0
		}
	}
	return progress
}

func (p *transferProgress) Update(value contextsync.Progress) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !value.Done && (!p.interactive || (!p.last.IsZero() && time.Since(p.last) < 100*time.Millisecond)) {
		return
	}
	p.last = time.Now()
	percent := 0
	if value.Total == 0 || value.Transferred >= value.Total {
		percent = 100
	} else {
		percent = int(value.Transferred * 100 / value.Total)
	}
	rate := int64(0)
	if value.Elapsed > 0 {
		rate = int64(float64(value.Transferred) / value.Elapsed.Seconds())
	}
	label := "Uploading"
	if value.Direction == "download" {
		label = "Downloading"
	}
	line := fmt.Sprintf("%-11s %3d%%  %s / %s  %s/s  %s",
		label, percent, formatBytes(value.Transferred), formatBytes(value.Total), formatBytes(rate),
		formatTransferElapsed(value.Elapsed))
	if p.interactive {
		fmt.Fprintf(p.output, "\r%-78s", line)
		if value.Done {
			fmt.Fprintln(p.output)
		}
		return
	}
	if value.Done {
		fmt.Fprintln(p.output, line)
	}
}

func formatTransferElapsed(value time.Duration) string {
	totalSeconds := int64(value.Round(time.Second) / time.Second)
	return fmt.Sprintf("%02d:%02d", totalSeconds/60, totalSeconds%60)
}

func removeContext(c *client.Client, args []string) error {
	flags := flag.NewFlagSet("context delete", flag.ContinueOnError)
	namespace := flags.String("namespace", envOr("CS_NAMESPACE", "serverless-harness"), "Kubernetes namespace")
	force := flags.Bool("force", false, "delete despite active or declared consumers")
	flags.Usage = func() { showHelp([]string{"context", "delete"}) }
	name, err := parseContextName(flags, args)
	if err != nil {
		return err
	}
	var deleteErr error
	if *force {
		deleteErr = c.ForceDeleteContext(context.Background(), *namespace, name)
	} else {
		deleteErr = c.DeleteContext(context.Background(), *namespace, name)
	}
	if deleteErr != nil {
		return deleteErr
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

func parseSingleArgument(flags *flag.FlagSet, args []string, label string) (string, error) {
	if len(args) > 1 && !strings.HasPrefix(args[0], "-") {
		args = append(append([]string{}, args[1:]...), args[0])
	}
	if err := flags.Parse(args); err != nil {
		return "", err
	}
	if flags.NArg() != 1 {
		flags.Usage()
		return "", fmt.Errorf("exactly one %s is required", label)
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
		if len(item.EffectiveAccess) > 0 {
			mode := "read-only"
			if hasPermission(item.EffectiveAccess, contextresource.PermissionWrite) {
				mode = "read-write"
			}
			fmt.Fprintf(w, "    └── access  %s · %d consumers\n", mode, len(item.Consumers))
		}
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
	if len(value.EffectiveAccess) > 0 {
		fmt.Printf("  Access:             %s\n", joinPermissions(value.EffectiveAccess))
		fmt.Printf("  Consumers:          %d\n", len(value.Consumers))
	}
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
		topology = "existing read-write"
		if value.Workspace.ReadOnly != nil && *value.Workspace.ReadOnly {
			topology = "existing read-only"
		}
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
  capture NAME         Capture a harness's native project state
  restore NAME         Restore captured state into a project
  attach NAME          Capture harness state automatically
  detach NAME          Stop automatic capture for a project
  export NAME          Write a portable .context bundle
  import FILE          Import a portable .context bundle
  sync push|pull       Transfer state between local and remote storage
  backup COMMAND       Back up local state automatically
  derive memory        Generate durable memory from captured state
  derive knowledge     Build searchable knowledge from state or memory
  search NAME QUERY    Search a local knowledge context
  revisions NAME       Show local revision history
  lineage NAME         Show derivation sources and availability
  access NAME          Show effective access and consumers
  grant NAME           Grant a subject access to a PVC context
  grants NAME          List access grants
  revoke NAME          Revoke a subject's PVC context access
  consumers NAME       Show declared and active PVC consumers
  audit NAME           Show access and attachment events
  delete NAME          Delete a named context (alias: rm)

Run "contextctl help context COMMAND" for command options.
`)
			return
		}
		switch args[1] {
		case "create":
			fmt.Print(`Usage: contextctl context create NAME [options]

Options:
  --backend BACKEND     pvc (default) or filesystem
  --namespace NAME      Kubernetes namespace (default CS_NAMESPACE or serverless-harness)
  --type TYPE           Context type (default workspace)
  --size SIZE           Storage size (default 1Gi)
  --storage-class NAME  Kubernetes storage class (default CS_STORAGE_CLASS)
  --access-mode MODE    ReadWriteOnce or ReadWriteMany (default ReadWriteOnce)
  --json                Print JSON

Types:
  workspace             Files used while an agent works
  state                 Native harness sessions, memory files, and metadata
  memory                Portable facts and summaries retained across sessions
  knowledge             Reference and retrieval data
  artifacts             Outputs produced by an agent

Local state example:
  contextctl ctx create demo --type state --backend filesystem
  contextctl ctx attach demo --harness claude
`)
		case "list":
			fmt.Print(`Usage: contextctl context list [options]

List filesystem and Kubernetes contexts together by default.

Options:
  --backend BACKEND     all (default), pvc, or filesystem
  --namespace NAME      Kubernetes namespace
  --json                Print JSON
`)
		case "get":
			fmt.Print("Usage: contextctl context get NAME [--backend pvc|filesystem] [--namespace NAME] [--json]\n")
		case "revisions":
			fmt.Print("Usage: contextctl context revisions NAME [--backend pvc|filesystem] [--namespace NAME] [--json]\n")
		case "lineage":
			fmt.Print("Usage: contextctl context lineage NAME [--json]\n")
		case "access":
			fmt.Print("Usage: contextctl context access NAME [--namespace NAME] [--json]\n")
		case "grant":
			fmt.Print("Usage: contextctl context grant NAME --subject kind:name --permissions read,attach [--namespace NAME]\n")
		case "grants":
			fmt.Print("Usage: contextctl context grants NAME [--namespace NAME] [--json]\n")
		case "revoke":
			fmt.Print("Usage: contextctl context revoke NAME --subject kind:name [--namespace NAME]\n")
		case "consumers":
			fmt.Print("Usage: contextctl context consumers NAME [--namespace NAME] [--json]\n")
		case "audit":
			fmt.Print("Usage: contextctl context audit NAME [--namespace NAME] [--json]\n")
		case "capture":
			fmt.Print(`Usage: contextctl context capture NAME [options]

Capture native Claude state for the current project into a filesystem context.

Options:
  --project PATH         Override the current project directory
  --harness NAME         Agent harness (default claude)
  --json                 Print JSON
`)
		case "attach":
			fmt.Print(`Usage: contextctl context attach NAME [options]

Attach a filesystem state context to the current project. State is captured automatically
after completed responses; launch the harness normally.

Options:
  --project PATH         Override the current project directory
  --harness NAME         claude, codex, opencode, or pi (default claude)
  --json                 Print JSON
`)
		case "detach":
			fmt.Print(`Usage: contextctl context detach NAME [options]

Stop automatic capture and remove only Context Service's harness integration.

Options:
  --project PATH         Override the attached project directory
  --harness NAME         claude, codex, opencode, or pi (default claude)
  --json                 Print JSON
`)
		case "restore":
			fmt.Print(`Usage: contextctl context restore NAME [options]

Restore captured Claude state into the current project, which must have no existing state.

Options:
  --project PATH         Override the current destination project
  --harness NAME         Agent harness (default claude)
  --json                 Print JSON
`)
		case "export":
			fmt.Print(`Usage: contextctl context export NAME [options]

Write a portable, checksummed .context bundle. Machine-local harness attachments are excluded.

Options:
  --output FILE          Output file (default NAME.context)
  --json                 Print JSON
`)
		case "import":
			fmt.Print(`Usage: contextctl context import FILE [options]

Verify and import a portable .context bundle into the local context store.

Options:
  --name NAME            Override the context name stored in the bundle
  --json                 Print JSON
`)
		case "sync":
			if len(args) == 2 {
				fmt.Print(`Usage: contextctl context sync push|pull SOURCE [options]

Transfer a portable context bundle between the local store and a Context Service PVC or S3.
`)
				return
			}
			switch args[2] {
			case "push":
				fmt.Print(`Usage: contextctl context sync push LOCAL_NAME [options]

Options:
  --remote-name NAME     Remote context name (default LOCAL_NAME)
  --to S3_URL            S3 destination (s3://bucket/prefix)
  --namespace NAME       Kubernetes namespace
  --helper-image IMAGE   Temporary transfer Pod image (default busybox:1.36)
  --s3-endpoint URL      S3-compatible endpoint (default CS_S3_ENDPOINT)
  --s3-region REGION     S3 region (default CS_S3_REGION, AWS_REGION, or us-east-1)
`)
			case "pull":
				fmt.Print(`Usage: contextctl context sync pull REMOTE_NAME|S3_URL [options]

Options:
  --name NAME            Local context name (default REMOTE_NAME)
  --namespace NAME       Kubernetes namespace
  --helper-image IMAGE   Temporary transfer Pod image (default busybox:1.36)
  --s3-endpoint URL      S3-compatible endpoint (default CS_S3_ENDPOINT)
  --s3-region REGION     S3 region (default CS_S3_REGION, AWS_REGION, or us-east-1)
`)
			default:
				fmt.Printf("Unknown sync direction %q.\n", args[2])
			}
		case "backup":
			if len(args) == 2 {
				fmt.Print(`Usage: contextctl context backup COMMAND NAME [options]

Commands:
  start NAME           Start automatic one-way backup
  stop NAME            Stop automatic backup
  status NAME          Show backup state and the latest result
  run NAME             Run the backup worker in the foreground
`)
				return
			}
			switch args[2] {
			case "start":
				fmt.Print(`Usage: contextctl context backup start NAME --to TARGET [options]

Targets:
  s3://bucket/prefix
  pvc://namespace/context-name

Options:
  --interval DURATION   Periodic backup interval (default 5m)
  --debounce DURATION   Delay after harness capture events (default 2s)
`)
			case "stop":
				fmt.Print("Usage: contextctl context backup stop NAME\n")
			case "status":
				fmt.Print("Usage: contextctl context backup status NAME [--json]\n")
			case "run":
				fmt.Print(`Usage: contextctl context backup run NAME

Run the configured backup worker in the foreground. This is useful for containers,
service managers, and troubleshooting.
`)
			default:
				fmt.Printf("Unknown backup command %q.\n", args[2])
			}
		case "derive":
			if len(args) == 2 {
				fmt.Print(`Usage: contextctl context derive memory|knowledge [options]

Generate memory from captured state or searchable knowledge from selected contexts.
`)
				return
			}
			switch args[2] {
			case "memory":
				fmt.Print(`Usage: contextctl context derive memory SOURCE [options]

Options:
  --name NAME           Memory context name (default SOURCE-memory)
  --agent NAME          claude or codex (default claude)
  --model NAME          Optional model override
  --max-input BYTES     Maximum captured text bytes (default 2097152)
`)
			case "knowledge":
				fmt.Print(`Usage: contextctl context derive knowledge --from NAME [--from NAME...] [options]

Options:
  --name NAME           Knowledge context name (default FIRST_SOURCE-knowledge)
  --from NAME           Source state or memory context (repeatable)
  --agent NAME          claude or codex (default claude)
  --model NAME          Optional model override
  --max-input BYTES     Maximum text bytes per source (default 2097152)
`)
			default:
				fmt.Printf("Unknown derived context %q.\n", args[2])
			}
		case "search":
			fmt.Print(`Usage: contextctl context search NAME QUERY [options]

Search a local knowledge context and show source attribution.

Options:
  --limit NUMBER        Maximum results (default 5)
  --json                Print JSON
`)
		case "delete", "rm":
			fmt.Print("Usage: contextctl context delete NAME [--namespace NAME] [--force]\n")
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
