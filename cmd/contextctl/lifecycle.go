package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/rossoctl/context-service/internal/client"
	"github.com/rossoctl/context-service/internal/contextresource"
	"github.com/rossoctl/context-service/internal/localcontext"
)

func snapshotCommand(c *client.Client, args []string) error {
	if len(args) == 0 {
		showHelp([]string{"context", "snapshot"})
		return errors.New("a snapshot command is required")
	}
	switch args[0] {
	case "create":
		return createSnapshot(c, args[1:])
	case "list":
		return listSnapshots(c, args[1:])
	case "clone":
		return cloneSnapshot(c, args[1:])
	case "restore":
		return restoreSnapshot(c, args[1:])
	case "retention":
		return setSnapshotRetention(c, args[1:])
	case "gc":
		return garbageCollectSnapshots(c, args[1:])
	case "capabilities":
		return snapshotCapabilities(c, args[1:])
	default:
		return fmt.Errorf("unknown snapshot command %q", args[0])
	}
}

func createSnapshot(c *client.Client, args []string) error {
	flags := flag.NewFlagSet("context snapshot create", flag.ContinueOnError)
	backend := flags.String("backend", "auto", "storage backend: auto, pvc, or filesystem")
	namespace := flags.String("namespace", envOr("CS_NAMESPACE", "serverless-harness"), "Kubernetes namespace")
	snapshotClass := flags.String("snapshot-class", "", "CSI VolumeSnapshotClass")
	protected := flags.Bool("protect", false, "protect this snapshot from garbage collection")
	jsonOutput := flags.Bool("json", false, "print JSON")
	flags.Usage = func() { showHelp([]string{"context", "snapshot", "create"}) }
	positional, err := parseLifecycleArgs(flags, args, 2)
	if err != nil {
		return err
	}
	if len(positional) != 2 {
		flags.Usage()
		return errors.New("context and snapshot names are required")
	}
	local, err := useLocalLifecycleBackend(positional[0], *backend)
	if err != nil {
		return err
	}
	if local {
		item, err := localContextStore().CreateSnapshot(positional[0], positional[1], *protected, nil)
		if err != nil {
			return err
		}
		printSnapshot(item, *jsonOutput)
		return nil
	}
	item, err := c.CreateContextSnapshot(context.Background(), *namespace, positional[0], contextresource.SnapshotRequest{Name: positional[1], SnapshotClass: *snapshotClass, Protected: *protected})
	if err != nil {
		return err
	}
	deadline := time.Now().Add(2 * time.Minute)
	for item.Status == "creating" && time.Now().Before(deadline) {
		time.Sleep(time.Second)
		item, err = c.GetContextSnapshot(context.Background(), *namespace, positional[0], positional[1])
		if err != nil {
			return err
		}
	}
	printRemoteSnapshot(item, *jsonOutput)
	return nil
}

func listSnapshots(c *client.Client, args []string) error {
	flags := flag.NewFlagSet("context snapshot list", flag.ContinueOnError)
	backend := flags.String("backend", "auto", "storage backend: auto, pvc, or filesystem")
	namespace := flags.String("namespace", envOr("CS_NAMESPACE", "serverless-harness"), "Kubernetes namespace")
	jsonOutput := flags.Bool("json", false, "print JSON")
	flags.Usage = func() { showHelp([]string{"context", "snapshot", "list"}) }
	name, err := parseContextName(flags, args)
	if err != nil {
		return err
	}
	local, err := useLocalLifecycleBackend(name, *backend)
	if err != nil {
		return err
	}
	if !local {
		items, err := c.ListContextSnapshots(context.Background(), *namespace, name)
		if err != nil {
			return err
		}
		if *jsonOutput {
			encoded, _ := json.MarshalIndent(items, "", "  ")
			fmt.Println(string(encoded))
			return nil
		}
		fmt.Printf("SNAPSHOTS (%d)\n", len(items))
		for _, item := range items {
			printRemoteSnapshot(item, false)
		}
		if len(items) == 0 {
			fmt.Println("None")
		}
		return nil
	}
	items, retention, err := localContextStore().ListSnapshots(name)
	if err != nil {
		return err
	}
	if *jsonOutput {
		encoded, _ := json.MarshalIndent(struct {
			Items     []localcontext.Snapshot      `json:"items"`
			Retention localcontext.RetentionPolicy `json:"retention"`
		}{items, retention}, "", "  ")
		fmt.Println(string(encoded))
		return nil
	}
	fmt.Printf("SNAPSHOTS (%d)\n", len(items))
	if len(items) == 0 {
		fmt.Println("None")
	}
	for _, item := range items {
		protection := ""
		if item.Protected {
			protection = " · protected"
		}
		fmt.Printf("%s  %s · %s%s\n", item.Name, shortRevision(item.Revision), item.CreatedAt.Local().Format("2006-01-02 15:04:05"), protection)
	}
	if retention.KeepLast > 0 || retention.MaxAge > 0 {
		fmt.Printf("Retention  keep %d · max age %s\n", retention.KeepLast, retention.MaxAge)
	}
	return nil
}

func cloneSnapshot(c *client.Client, args []string) error {
	flags := flag.NewFlagSet("context snapshot clone", flag.ContinueOnError)
	backend := flags.String("backend", "auto", "storage backend: auto, pvc, or filesystem")
	namespace := flags.String("namespace", envOr("CS_NAMESPACE", "serverless-harness"), "Kubernetes namespace")
	jsonOutput := flags.Bool("json", false, "print JSON")
	flags.Usage = func() { showHelp([]string{"context", "snapshot", "clone"}) }
	positional, err := parseLifecycleArgs(flags, args, 2)
	if err != nil {
		return err
	}
	if len(positional) != 2 {
		flags.Usage()
		return errors.New("source@snapshot and destination names are required")
	}
	source, snapshot, found := strings.Cut(positional[0], "@")
	if !found || source == "" || snapshot == "" {
		return errors.New("source must be CONTEXT@SNAPSHOT")
	}
	local, err := useLocalLifecycleBackend(source, *backend)
	if err != nil {
		return err
	}
	if local {
		manifest, err := localContextStore().CloneSnapshot(source, snapshot, positional[1])
		if err != nil {
			return err
		}
		printLocalContext(manifest, *jsonOutput)
		return nil
	}
	result, err := c.CloneContextSnapshot(context.Background(), *namespace, source, contextresource.CloneRequest{Name: positional[1], Snapshot: snapshot})
	if err != nil {
		return err
	}
	printContext(result, *jsonOutput)
	return nil
}

func restoreSnapshot(c *client.Client, args []string) error {
	flags := flag.NewFlagSet("context snapshot restore", flag.ContinueOnError)
	backend := flags.String("backend", "auto", "storage backend: auto, pvc, or filesystem")
	namespace := flags.String("namespace", envOr("CS_NAMESPACE", "serverless-harness"), "Kubernetes namespace")
	jsonOutput := flags.Bool("json", false, "print JSON")
	flags.Usage = func() { showHelp([]string{"context", "snapshot", "restore"}) }
	positional, err := parseLifecycleArgs(flags, args, 2)
	if err != nil {
		return err
	}
	if len(positional) != 2 {
		flags.Usage()
		return errors.New("context and snapshot names are required")
	}
	local, err := useLocalLifecycleBackend(positional[0], *backend)
	if err != nil {
		return err
	}
	if !local {
		result, err := c.RestoreContextSnapshot(context.Background(), *namespace, positional[0], contextresource.RestoreRequest{Snapshot: positional[1]})
		if err != nil {
			return err
		}
		printContext(result, *jsonOutput)
		return nil
	}
	manifest, safety, err := localContextStore().RestoreSnapshot(positional[0], positional[1])
	if err != nil {
		return err
	}
	if safety != nil && !*jsonOutput {
		fmt.Printf("Preserved current state as protected snapshot %s\n", safety.Name)
	}
	printLocalContext(manifest, *jsonOutput)
	return nil
}

func setSnapshotRetention(c *client.Client, args []string) error {
	flags := flag.NewFlagSet("context snapshot retention", flag.ContinueOnError)
	backend := flags.String("backend", "auto", "storage backend: auto, pvc, or filesystem")
	namespace := flags.String("namespace", envOr("CS_NAMESPACE", "serverless-harness"), "Kubernetes namespace")
	keepLast := flags.Int("keep-last", 0, "number of newest snapshots to retain")
	maxAge := flags.Duration("max-age", 0, "retain snapshots newer than this duration")
	flags.Usage = func() { showHelp([]string{"context", "snapshot", "retention"}) }
	name, err := parseContextName(flags, args)
	if err != nil {
		return err
	}
	local, err := useLocalLifecycleBackend(name, *backend)
	if err != nil {
		return err
	}
	if !local {
		maxAgeValue := ""
		if *maxAge > 0 {
			maxAgeValue = maxAge.String()
		}
		_, err := c.SetContextRetention(context.Background(), *namespace, name, contextresource.RetentionRequest{KeepLast: *keepLast, MaxAge: maxAgeValue})
		if err != nil {
			return err
		}
		fmt.Printf("Retention for %s: keep %d · max age %s\n", name, *keepLast, *maxAge)
		return nil
	}
	policy, err := localContextStore().SetRetention(name, localcontext.RetentionPolicy{KeepLast: *keepLast, MaxAge: *maxAge})
	if err != nil {
		return err
	}
	fmt.Printf("Retention for %s: keep %d · max age %s\n", name, policy.KeepLast, policy.MaxAge)
	return nil
}

func garbageCollectSnapshots(c *client.Client, args []string) error {
	flags := flag.NewFlagSet("context snapshot gc", flag.ContinueOnError)
	backend := flags.String("backend", "auto", "storage backend: auto, pvc, or filesystem")
	namespace := flags.String("namespace", envOr("CS_NAMESPACE", "serverless-harness"), "Kubernetes namespace")
	dryRun := flags.Bool("dry-run", false, "show what would be deleted")
	jsonOutput := flags.Bool("json", false, "print JSON")
	flags.Usage = func() { showHelp([]string{"context", "snapshot", "gc"}) }
	name, err := parseContextName(flags, args)
	if err != nil {
		return err
	}
	local, err := useLocalLifecycleBackend(name, *backend)
	if err != nil {
		return err
	}
	if !local {
		result, err := c.GarbageCollectContextSnapshots(context.Background(), *namespace, name, *dryRun)
		if err != nil {
			return err
		}
		return printRemoteGC(result, *jsonOutput)
	}
	result, err := localContextStore().GarbageCollect(name, *dryRun)
	if err != nil {
		return err
	}
	if *jsonOutput {
		encoded, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(encoded))
		return nil
	}
	verb := "Deleted"
	if result.DryRun {
		verb = "Would delete"
	}
	fmt.Printf("%s %d snapshot(s); kept %d\n", verb, len(result.Deleted), len(result.Kept))
	for _, item := range result.Deleted {
		fmt.Printf("- %s  %s\n", item.Name, shortRevision(item.Revision))
	}
	return nil
}

func snapshotCapabilities(c *client.Client, args []string) error {
	flags := flag.NewFlagSet("context snapshot capabilities", flag.ContinueOnError)
	backend := flags.String("backend", "pvc", "storage backend: pvc or filesystem")
	namespace := flags.String("namespace", envOr("CS_NAMESPACE", "serverless-harness"), "Kubernetes namespace")
	jsonOutput := flags.Bool("json", false, "print JSON")
	name, err := parseContextName(flags, args)
	if err != nil {
		return err
	}
	if *backend == "filesystem" || *backend == "local" {
		result := contextresource.LifecycleCapabilities{Snapshots: true, Clones: true, Restore: true, Driver: "filesystem"}
		if *jsonOutput {
			encoded, _ := json.MarshalIndent(result, "", "  ")
			fmt.Println(string(encoded))
		} else {
			fmt.Println("Snapshots true · clones true · in-place restore true")
			fmt.Println("Driver filesystem")
		}
		return nil
	}
	if *backend != "pvc" {
		return fmt.Errorf("unsupported context backend %q; use pvc or filesystem", *backend)
	}
	result, err := c.ContextLifecycleCapabilities(context.Background(), *namespace, name)
	if err != nil {
		return err
	}
	if *jsonOutput {
		encoded, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(encoded))
		return nil
	}
	fmt.Printf("Snapshots %t · clones %t · in-place restore %t\n", result.Snapshots, result.Clones, result.Restore)
	if result.Driver != "" {
		fmt.Printf("Driver %s · class %s\n", result.Driver, result.Class)
	}
	if result.Reason != "" {
		fmt.Println(result.Reason)
	}
	return nil
}

func useLocalLifecycleBackend(name, backend string) (bool, error) {
	switch backend {
	case "filesystem", "local":
		return true, nil
	case "pvc":
		return false, nil
	case "auto":
		_, err := localContextStore().Get(name)
		if err == nil {
			return true, nil
		}
		if errors.Is(err, localcontext.ErrNotFound) {
			return false, nil
		}
		return false, err
	default:
		return false, fmt.Errorf("unsupported context backend %q; use auto, pvc, or filesystem", backend)
	}
}

func printRemoteSnapshot(item contextresource.Snapshot, jsonOutput bool) {
	if jsonOutput {
		encoded, _ := json.MarshalIndent(item, "", "  ")
		fmt.Println(string(encoded))
		return
	}
	protection := ""
	if item.Protected {
		protection = " · protected"
	}
	fmt.Printf("%s  %s · %s%s\n", item.Name, item.Status, shortRevision(item.Revision), protection)
}

func printRemoteGC(result contextresource.GarbageCollection, jsonOutput bool) error {
	if jsonOutput {
		encoded, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(encoded))
		return nil
	}
	verb := "Deleted"
	if result.DryRun {
		verb = "Would delete"
	}
	fmt.Printf("%s %d snapshot(s); kept %d\n", verb, len(result.Deleted), len(result.Kept))
	for _, item := range result.Deleted {
		fmt.Printf("- %s  %s\n", item.Name, shortRevision(item.Revision))
	}
	return nil
}

func parseLifecycleArgs(flags *flag.FlagSet, args []string, count int) ([]string, error) {
	if len(args) >= count && !strings.HasPrefix(args[0], "-") {
		positionals := append([]string(nil), args[:count]...)
		remaining := append([]string(nil), args[count:]...)
		if err := flags.Parse(remaining); err != nil {
			return nil, err
		}
		positionals = append(positionals, flags.Args()...)
		return positionals, nil
	}
	if err := flags.Parse(args); err != nil {
		return nil, err
	}
	return flags.Args(), nil
}

func printSnapshot(item localcontext.Snapshot, jsonOutput bool) {
	if jsonOutput {
		encoded, _ := json.MarshalIndent(item, "", "  ")
		fmt.Println(string(encoded))
		return
	}
	protection := ""
	if item.Protected {
		protection = " · protected"
	}
	fmt.Printf("%s  ready · %s · %s%s\n", item.Name, shortRevision(item.Revision), formatBytes(item.Bytes), protection)
}
