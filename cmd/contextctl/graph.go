package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/rossoctl/context-service/internal/client"
	"github.com/rossoctl/context-service/internal/contextbackup"
	"github.com/rossoctl/context-service/internal/contextresource"
	"github.com/rossoctl/context-service/internal/localcontext"
)

type graphView struct {
	Namespace string      `json:"namespace"`
	Contexts  []graphNode `json:"contexts"`
	Warnings  []string    `json:"warnings,omitempty"`
}

type graphNode struct {
	Name            string          `json:"name"`
	Type            string          `json:"type"`
	Backend         string          `json:"backend"`
	Namespace       string          `json:"namespace,omitempty"`
	Status          string          `json:"status"`
	Location        string          `json:"location"`
	CurrentRevision string          `json:"currentRevision,omitempty"`
	Sources         []graphRelation `json:"sources,omitempty"`
	Derivatives     []graphRelation `json:"derivatives,omitempty"`
	Consumers       []graphConsumer `json:"consumers,omitempty"`
	Copies          []graphCopy     `json:"copies,omitempty"`
	revisionIDs     []string
}

type graphRelation struct {
	Name     string `json:"name"`
	Type     string `json:"type,omitempty"`
	Backend  string `json:"backend,omitempty"`
	Revision string `json:"revision,omitempty"`
	State    string `json:"state"`
}

type graphConsumer struct {
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Project    string `json:"project,omitempty"`
	AccessMode string `json:"accessMode,omitempty"`
	Active     bool   `json:"active"`
	Backend    string `json:"backend"`
}

type graphCopy struct {
	Backend         string `json:"backend"`
	Namespace       string `json:"namespace,omitempty"`
	Name            string `json:"name,omitempty"`
	Location        string `json:"location"`
	State           string `json:"state"`
	Status          string `json:"status,omitempty"`
	CurrentRevision string `json:"currentRevision,omitempty"`
}

type graphBackup struct {
	Config contextbackup.Config
	Status contextbackup.Status
}

func graphContext(c *client.Client, args []string) error {
	flags := flag.NewFlagSet("context graph", flag.ContinueOnError)
	backend := flags.String("backend", "all", "storage backend: all, pvc, or filesystem")
	namespace := flags.String("namespace", envOr("CS_NAMESPACE", "serverless-harness"), "Kubernetes namespace")
	jsonOutput := flags.Bool("json", false, "print JSON")
	flags.Usage = func() { showHelp([]string{"context", "graph"}) }
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		flags.Usage()
		return errors.New("context graph does not accept arguments")
	}
	if *backend != "all" && *backend != "pvc" && *backend != "filesystem" && *backend != "local" {
		return fmt.Errorf("unsupported context backend %q; use all, pvc, or filesystem", *backend)
	}

	var localItems []localcontext.Manifest
	var pvcItems []contextresource.Resource
	consumers := map[string][]contextresource.Consumer{}
	backups := map[string]graphBackup{}
	warnings := []string{}
	if *backend != "pvc" {
		items, err := localContextStore().List()
		if err != nil {
			return err
		}
		localItems = items
		for _, item := range items {
			config, configErr := contextbackup.LoadConfig(item.Path)
			if configErr != nil {
				if !errors.Is(configErr, os.ErrNotExist) {
					warnings = append(warnings, fmt.Sprintf("%s backup unavailable: %v", item.Name, configErr))
				}
				continue
			}
			status, statusErr := contextbackup.LoadStatus(item.Path)
			if statusErr != nil && !errors.Is(statusErr, os.ErrNotExist) {
				warnings = append(warnings, fmt.Sprintf("%s backup status unavailable: %v", item.Name, statusErr))
			}
			status.Running = processRunning(status.PID)
			backups[item.Name] = graphBackup{Config: config, Status: status}
		}
	}
	if *backend != "filesystem" && *backend != "local" {
		items, err := c.ListContexts(context.Background(), *namespace)
		if err != nil {
			if *backend == "pvc" {
				return err
			}
			warnings = append(warnings, "PVC contexts unavailable: "+err.Error())
		} else {
			pvcItems = items
			for _, item := range items {
				values, consumerErr := c.ListContextConsumers(context.Background(), item.Namespace, item.Name)
				if consumerErr != nil {
					warnings = append(warnings, fmt.Sprintf("%s/%s consumers unavailable: %v", item.Namespace, item.Name, consumerErr))
					continue
				}
				consumers[pvcKey(item.Namespace, item.Name)] = values
			}
		}
	}

	view := buildGraph(*namespace, localItems, pvcItems, consumers, backups, warnings)
	if *jsonOutput {
		encoded, _ := json.MarshalIndent(view, "", "  ")
		fmt.Println(string(encoded))
		return nil
	}
	writeGraph(os.Stdout, view)
	return nil
}

func buildGraph(namespace string, localItems []localcontext.Manifest, pvcItems []contextresource.Resource, consumers map[string][]contextresource.Consumer, backups map[string]graphBackup, warnings []string) graphView {
	sort.Slice(localItems, func(i, j int) bool { return localItems[i].Name < localItems[j].Name })
	sort.Slice(pvcItems, func(i, j int) bool {
		return pvcKey(pvcItems[i].Namespace, pvcItems[i].Name) < pvcKey(pvcItems[j].Namespace, pvcItems[j].Name)
	})

	nodes := make([]graphNode, 0, len(localItems)+len(pvcItems))
	claimedPVC := map[string]bool{}
	for _, item := range localItems {
		node := localGraphNode(item)
		if backup, ok := backups[item.Name]; ok {
			copy := backupGraphCopy(item, backup, pvcItems)
			node.Copies = append(node.Copies, copy)
			if copy.Backend == "pvc" && copy.Name != "" {
				key := pvcKey(copy.Namespace, copy.Name)
				claimedPVC[key] = true
				node.Consumers = append(node.Consumers, pvcGraphConsumers(consumers[key])...)
			}
		}
		for _, pvc := range pvcItems {
			key := pvcKey(pvc.Namespace, pvc.Name)
			if claimedPVC[key] || !isContextCopy(item, pvc) {
				continue
			}
			claimedPVC[key] = true
			node.Copies = append(node.Copies, pvcGraphCopy(item, pvc))
			node.Consumers = append(node.Consumers, pvcGraphConsumers(consumers[key])...)
		}
		nodes = append(nodes, node)
	}
	for _, item := range pvcItems {
		if claimedPVC[pvcKey(item.Namespace, item.Name)] {
			continue
		}
		nodes = append(nodes, pvcGraphNode(item, consumers[pvcKey(item.Namespace, item.Name)]))
	}

	for index := range nodes {
		for sourceIndex := range nodes[index].Sources {
			resolveGraphRelation(&nodes[index].Sources[sourceIndex], nodes)
		}
	}
	for index := range nodes {
		for _, source := range nodes[index].Sources {
			target := findGraphNode(nodes, source.Name, source.Revision)
			if target < 0 {
				continue
			}
			nodes[target].Derivatives = append(nodes[target].Derivatives, graphRelation{
				Name: nodes[index].Name, Type: nodes[index].Type, Backend: nodes[index].Backend,
				Revision: nodes[index].CurrentRevision, State: source.State,
			})
		}
	}
	for index := range nodes {
		sortGraphNode(&nodes[index])
	}
	sort.Slice(nodes, func(i, j int) bool {
		left := nodes[i].Name + "\x00" + nodes[i].Backend + "\x00" + nodes[i].Namespace
		right := nodes[j].Name + "\x00" + nodes[j].Backend + "\x00" + nodes[j].Namespace
		return left < right
	})
	sort.Strings(warnings)
	return graphView{Namespace: namespace, Contexts: nodes, Warnings: warnings}
}

func localGraphNode(item localcontext.Manifest) graphNode {
	node := graphNode{
		Name: item.Name, Type: item.Type, Backend: "filesystem", Status: "ready",
		Location: item.Path, CurrentRevision: item.CurrentRevision,
	}
	for harness, attachment := range item.Attachments {
		node.Consumers = append(node.Consumers, graphConsumer{
			Kind: "harness", Name: displayHarness(harness), Project: attachment.Project,
			Active: true, Backend: "filesystem",
		})
	}
	for _, source := range manifestSources(item) {
		node.Sources = append(node.Sources, graphRelation{Name: source.Context, Type: source.Type, Revision: source.Revision, State: "missing"})
	}
	for _, revision := range item.Revisions {
		node.revisionIDs = append(node.revisionIDs, revision.ID)
	}
	return node
}

func pvcGraphNode(item contextresource.Resource, consumers []contextresource.Consumer) graphNode {
	node := graphNode{
		Name: item.Name, Type: item.Type, Backend: "pvc", Namespace: item.Namespace,
		Status: item.Status, Location: "pvc/" + item.Attachment.ClaimName, CurrentRevision: item.CurrentRevision,
		Consumers: pvcGraphConsumers(consumers),
	}
	for _, source := range resourceSources(item) {
		node.Sources = append(node.Sources, graphRelation{Name: source.Context, Type: source.Type, Revision: source.Revision, State: "missing"})
	}
	for _, revision := range item.Revisions {
		node.revisionIDs = append(node.revisionIDs, revision.ID)
	}
	return node
}

func pvcGraphConsumers(items []contextresource.Consumer) []graphConsumer {
	result := make([]graphConsumer, 0, len(items))
	for _, item := range items {
		result = append(result, graphConsumer{
			Kind: item.Kind, Name: item.Name, AccessMode: item.AccessMode,
			Active: item.Active, Backend: "pvc",
		})
	}
	return result
}

func manifestSources(item localcontext.Manifest) []localcontext.SourceReference {
	if item.CurrentRevision != "" {
		for index := len(item.Revisions) - 1; index >= 0; index-- {
			if item.Revisions[index].ID == item.CurrentRevision {
				return item.Revisions[index].Sources
			}
		}
	}
	if item.Derivation == nil {
		return nil
	}
	if len(item.Derivation.Sources) > 0 {
		return item.Derivation.Sources
	}
	if item.Derivation.SourceContext != "" {
		return []localcontext.SourceReference{{
			Context: item.Derivation.SourceContext, Type: item.Derivation.SourceType,
			Revision: item.Derivation.SourceRevision,
		}}
	}
	return nil
}

func resourceSources(item contextresource.Resource) []contextresource.SourceReference {
	for index := len(item.Revisions) - 1; index >= 0; index-- {
		if item.Revisions[index].ID == item.CurrentRevision {
			return item.Revisions[index].Sources
		}
	}
	return nil
}

func isContextCopy(local localcontext.Manifest, pvc contextresource.Resource) bool {
	if local.Name == pvc.Name {
		return true
	}
	return local.CurrentRevision != "" && local.CurrentRevision == pvc.CurrentRevision
}

func pvcGraphCopy(local localcontext.Manifest, pvc contextresource.Resource) graphCopy {
	state := relationshipState(local.CurrentRevision, pvc.CurrentRevision, pvc.Status == "ready")
	return graphCopy{
		Backend: "pvc", Namespace: pvc.Namespace, Name: pvc.Name,
		Location: "pvc/" + pvc.Attachment.ClaimName, State: state,
		Status: pvc.Status, CurrentRevision: pvc.CurrentRevision,
	}
}

func backupGraphCopy(local localcontext.Manifest, backup graphBackup, pvcItems []contextresource.Resource) graphCopy {
	target := backup.Config.Target
	copy := graphCopy{Backend: "s3", Location: target, State: backupState(local.CurrentRevision, backup.Status)}
	parsed, err := url.Parse(target)
	if err != nil || parsed.Scheme != "pvc" {
		return copy
	}
	copy.Backend = "pvc"
	copy.Namespace = parsed.Host
	copy.Name = strings.TrimPrefix(parsed.Path, "/")
	for _, item := range pvcItems {
		if item.Namespace == copy.Namespace && item.Name == copy.Name {
			copy.Location = "pvc/" + item.Attachment.ClaimName
			copy.Status = item.Status
			copy.CurrentRevision = item.CurrentRevision
			if copy.State != "failed" && copy.State != "syncing" {
				copy.State = relationshipState(local.CurrentRevision, item.CurrentRevision, item.Status == "ready")
			}
			return copy
		}
	}
	copy.State = "missing"
	return copy
}

func backupState(current string, status contextbackup.Status) string {
	if status.LastError != "" {
		return "failed"
	}
	if status.Running && status.SourceRevision != current {
		return "syncing"
	}
	if status.LastSuccess.IsZero() || status.SourceRevision == "" {
		return "missing"
	}
	if status.SourceRevision != current {
		return "stale"
	}
	return "ready"
}

func relationshipState(want, have string, available bool) string {
	if !available {
		return "missing"
	}
	if want == "" {
		return "ready"
	}
	if have == "" {
		return "missing"
	}
	if want != "" && want != have {
		return "stale"
	}
	return "ready"
}

func resolveGraphRelation(relation *graphRelation, nodes []graphNode) {
	index := findGraphNode(nodes, relation.Name, relation.Revision)
	if index < 0 {
		relation.State = "missing"
		return
	}
	target := nodes[index]
	relation.Backend = target.Backend
	if relation.Type == "" {
		relation.Type = target.Type
	}
	if relation.Revision == "" || relation.Revision == target.CurrentRevision {
		relation.State = "ready"
		return
	}
	relation.State = "missing"
	for _, revision := range target.revisionIDs {
		if revision == relation.Revision {
			relation.State = "stale"
			break
		}
	}
}

func findGraphNode(nodes []graphNode, name, revision string) int {
	nameMatch := -1
	for index := range nodes {
		if nodes[index].Name != name {
			continue
		}
		if nameMatch < 0 {
			nameMatch = index
		}
		if revision != "" && nodes[index].CurrentRevision == revision {
			return index
		}
	}
	return nameMatch
}

func sortGraphNode(node *graphNode) {
	relationLess := func(left, right graphRelation) bool {
		return left.Name+"\x00"+left.Backend+"\x00"+left.Revision < right.Name+"\x00"+right.Backend+"\x00"+right.Revision
	}
	sort.Slice(node.Sources, func(i, j int) bool { return relationLess(node.Sources[i], node.Sources[j]) })
	sort.Slice(node.Derivatives, func(i, j int) bool { return relationLess(node.Derivatives[i], node.Derivatives[j]) })
	sort.Slice(node.Consumers, func(i, j int) bool {
		return node.Consumers[i].Backend+"\x00"+node.Consumers[i].Kind+"\x00"+node.Consumers[i].Name < node.Consumers[j].Backend+"\x00"+node.Consumers[j].Kind+"\x00"+node.Consumers[j].Name
	})
	sort.Slice(node.Copies, func(i, j int) bool { return node.Copies[i].Location < node.Copies[j].Location })
}

func writeGraph(w io.Writer, view graphView) {
	fmt.Fprintf(w, "CONTEXT GRAPH (%d)\n", len(view.Contexts))
	if len(view.Contexts) == 0 {
		fmt.Fprintln(w, "None")
	}
	for index, node := range view.Contexts {
		if index > 0 {
			fmt.Fprintln(w)
		}
		header := fmt.Sprintf("%s  %s · %s · %s", node.Name, node.Type, node.Backend, displayStatus(node.Status))
		if node.Backend == "pvc" && node.Namespace != "" {
			header += " · namespace " + node.Namespace
		}
		if node.CurrentRevision != "" {
			header += " · " + shortRevision(node.CurrentRevision)
		}
		fmt.Fprintln(w, header)
		lines := []string{"location  " + node.Location}
		for _, source := range node.Sources {
			lines = append(lines, graphRelationLine("source", source))
		}
		for _, derivative := range node.Derivatives {
			lines = append(lines, graphRelationLine("derives", derivative))
		}
		for _, consumer := range node.Consumers {
			state := "declared"
			if consumer.Backend == "filesystem" {
				state = "attached"
			} else if consumer.Active {
				state = "active"
			}
			detail := consumer.Kind + "/" + consumer.Name
			if consumer.Project != "" {
				detail += " · " + consumer.Project
			}
			if consumer.AccessMode != "" {
				detail += " · " + consumer.AccessMode
			}
			lines = append(lines, "consumer  "+detail+" · "+state)
		}
		for _, copy := range node.Copies {
			detail := copy.Location
			if copy.Backend == "pvc" && copy.Namespace != "" {
				detail += " · namespace " + copy.Namespace
			}
			if copy.CurrentRevision != "" {
				detail += " · " + shortRevision(copy.CurrentRevision)
			}
			lines = append(lines, "copy  "+detail+" · "+copy.State)
		}
		for lineIndex, line := range lines {
			branch := "├──"
			if lineIndex == len(lines)-1 {
				branch = "└──"
			}
			fmt.Fprintf(w, "%s %s\n", branch, line)
		}
	}
	for _, warning := range view.Warnings {
		fmt.Fprintf(w, "\nWarning: %s\n", warning)
	}
}

func graphRelationLine(label string, relation graphRelation) string {
	value := label + "  " + relation.Name
	if relation.Revision != "" {
		value += "@" + shortRevision(relation.Revision)
	}
	if relation.Type != "" {
		value += " · " + relation.Type
	}
	if relation.Backend != "" {
		value += " · " + relation.Backend
	}
	return value + " · " + relation.State
}

func pvcKey(namespace, name string) string { return namespace + "/" + name }
