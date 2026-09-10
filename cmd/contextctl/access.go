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

func contextAccess(c *client.Client, args []string) error {
	flags := flag.NewFlagSet("context access", flag.ContinueOnError)
	namespace := flags.String("namespace", envOr("CS_NAMESPACE", "serverless-harness"), "Kubernetes namespace")
	jsonOutput := flags.Bool("json", false, "print JSON")
	flags.Usage = func() { showHelp([]string{"context", "access"}) }
	name, err := parseContextName(flags, args)
	if err != nil {
		return err
	}
	resource, err := c.GetContext(context.Background(), *namespace, name)
	if err != nil {
		return err
	}
	consumers, err := c.ListContextConsumers(context.Background(), *namespace, name)
	if err != nil {
		return err
	}
	if *jsonOutput {
		encoded, _ := json.MarshalIndent(struct {
			Permissions []contextresource.Permission `json:"permissions"`
			Consumers   []contextresource.Consumer   `json:"consumers"`
		}{resource.EffectiveAccess, consumers}, "", "  ")
		fmt.Println(string(encoded))
		return nil
	}
	mode := "read-only"
	if hasPermission(resource.EffectiveAccess, contextresource.PermissionWrite) {
		mode = "read-write"
	}
	fmt.Printf("%s  %s\n", name, mode)
	fmt.Printf("Permissions: %s\n", joinPermissions(resource.EffectiveAccess))
	fmt.Printf("Consumers:   %d\n", len(consumers))
	return nil
}

func contextGrant(c *client.Client, args []string) error {
	flags := flag.NewFlagSet("context grant", flag.ContinueOnError)
	namespace := flags.String("namespace", envOr("CS_NAMESPACE", "serverless-harness"), "Kubernetes namespace")
	subjectValue := flags.String("subject", "", "subject kind:name")
	permissionValue := flags.String("permissions", "read", "comma-separated permissions")
	flags.Usage = func() { showHelp([]string{"context", "grant"}) }
	name, err := parseContextName(flags, args)
	if err != nil {
		return err
	}
	subject, err := parseSubject(*subjectValue)
	if err != nil {
		return err
	}
	permissions, err := parsePermissions(*permissionValue)
	if err != nil {
		return err
	}
	if _, err := c.SetContextGrant(context.Background(), *namespace, name, contextresource.Grant{Subject: subject, Permissions: permissions}); err != nil {
		return err
	}
	fmt.Printf("Granted %s to %s:%s on %s\n", joinPermissions(permissions), subject.Kind, subject.Name, name)
	return nil
}

func contextGrants(c *client.Client, args []string) error {
	flags := flag.NewFlagSet("context grants", flag.ContinueOnError)
	namespace := flags.String("namespace", envOr("CS_NAMESPACE", "serverless-harness"), "Kubernetes namespace")
	jsonOutput := flags.Bool("json", false, "print JSON")
	flags.Usage = func() { showHelp([]string{"context", "grants"}) }
	name, err := parseContextName(flags, args)
	if err != nil {
		return err
	}
	items, err := c.ListContextGrants(context.Background(), *namespace, name)
	if err != nil {
		return err
	}
	if *jsonOutput {
		encoded, _ := json.MarshalIndent(items, "", "  ")
		fmt.Println(string(encoded))
		return nil
	}
	fmt.Printf("GRANTS (%d)\n", len(items))
	if len(items) == 0 {
		fmt.Println("None")
	}
	for _, item := range items {
		fmt.Printf("%s:%s  %s\n", item.Subject.Kind, item.Subject.Name, joinPermissions(item.Permissions))
	}
	return nil
}

func contextRevoke(c *client.Client, args []string) error {
	flags := flag.NewFlagSet("context revoke", flag.ContinueOnError)
	namespace := flags.String("namespace", envOr("CS_NAMESPACE", "serverless-harness"), "Kubernetes namespace")
	subjectValue := flags.String("subject", "", "subject kind:name")
	flags.Usage = func() { showHelp([]string{"context", "revoke"}) }
	name, err := parseContextName(flags, args)
	if err != nil {
		return err
	}
	subject, err := parseSubject(*subjectValue)
	if err != nil {
		return err
	}
	if _, err := c.RevokeContextGrant(context.Background(), *namespace, name, subject); err != nil {
		return err
	}
	fmt.Printf("Revoked %s:%s from %s\n", subject.Kind, subject.Name, name)
	return nil
}

func contextConsumers(c *client.Client, args []string) error {
	flags := flag.NewFlagSet("context consumers", flag.ContinueOnError)
	namespace := flags.String("namespace", envOr("CS_NAMESPACE", "serverless-harness"), "Kubernetes namespace")
	jsonOutput := flags.Bool("json", false, "print JSON")
	flags.Usage = func() { showHelp([]string{"context", "consumers"}) }
	name, err := parseContextName(flags, args)
	if err != nil {
		return err
	}
	items, err := c.ListContextConsumers(context.Background(), *namespace, name)
	if err != nil {
		return err
	}
	if *jsonOutput {
		encoded, _ := json.MarshalIndent(items, "", "  ")
		fmt.Println(string(encoded))
		return nil
	}
	fmt.Printf("CONSUMERS (%d)\n", len(items))
	if len(items) == 0 {
		fmt.Println("None")
	}
	for _, item := range items {
		state := "declared"
		if item.Active {
			state = "active"
		}
		fmt.Printf("%s/%s  %s · %s · %s:%s\n", item.Kind, item.Name, item.AccessMode, state, item.Subject.Kind, item.Subject.Name)
	}
	return nil
}

func contextAudit(c *client.Client, args []string) error {
	flags := flag.NewFlagSet("context audit", flag.ContinueOnError)
	namespace := flags.String("namespace", envOr("CS_NAMESPACE", "serverless-harness"), "Kubernetes namespace")
	jsonOutput := flags.Bool("json", false, "print JSON")
	flags.Usage = func() { showHelp([]string{"context", "audit"}) }
	name, err := parseContextName(flags, args)
	if err != nil {
		return err
	}
	items, err := c.ListContextAudit(context.Background(), *namespace, name)
	if err != nil {
		return err
	}
	if *jsonOutput {
		encoded, _ := json.MarshalIndent(items, "", "  ")
		fmt.Println(string(encoded))
		return nil
	}
	fmt.Printf("AUDIT (%d)\n", len(items))
	if len(items) == 0 {
		fmt.Println("None")
	}
	for _, item := range items {
		target := ""
		if item.Target != nil {
			target = " → " + item.Target.Kind + ":" + item.Target.Name
		}
		fmt.Printf("%s  %s · %s:%s%s\n", item.Time.Local().Format("2006-01-02 15:04:05"), item.Action, item.Subject.Kind, item.Subject.Name, target)
	}
	return nil
}

func parseSubject(value string) (contextresource.Subject, error) {
	kind, name, found := strings.Cut(strings.TrimSpace(value), ":")
	if !found || name == "" {
		return contextresource.Subject{}, errors.New("--subject must be kind:name")
	}
	switch kind {
	case "user", "agent", "workload", "service":
	default:
		return contextresource.Subject{}, errors.New("subject kind must be user, agent, workload, or service")
	}
	return contextresource.Subject{Kind: kind, Name: name}, nil
}

func parsePermissions(value string) ([]contextresource.Permission, error) {
	seen := map[contextresource.Permission]bool{}
	var result []contextresource.Permission
	for _, item := range strings.Split(value, ",") {
		permission := contextresource.Permission(strings.TrimSpace(item))
		switch permission {
		case contextresource.PermissionRead, contextresource.PermissionWrite, contextresource.PermissionAttach, contextresource.PermissionDerive, contextresource.PermissionAdminister:
		default:
			return nil, fmt.Errorf("unsupported permission %q", item)
		}
		if !seen[permission] {
			result = append(result, permission)
			seen[permission] = true
		}
	}
	if len(result) == 0 {
		return nil, errors.New("at least one permission is required")
	}
	for _, permission := range []contextresource.Permission{contextresource.PermissionWrite, contextresource.PermissionAttach, contextresource.PermissionDerive} {
		if seen[permission] && !seen[contextresource.PermissionRead] {
			return nil, fmt.Errorf("permission %q requires read", permission)
		}
	}
	return result, nil
}

func hasPermission(values []contextresource.Permission, expected contextresource.Permission) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
func joinPermissions(values []contextresource.Permission) string {
	parts := make([]string, len(values))
	for i, value := range values {
		parts[i] = string(value)
	}
	return strings.Join(parts, ",")
}
