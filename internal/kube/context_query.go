package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/rossoctl/context-service/internal/contextquery"
	"github.com/rossoctl/context-service/internal/contextresource"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const queryIndexRevisionAnnotation = "context.rossoctl.io/query-index-revision"

func (m *Manager) PublishContextQueryIndex(ctx context.Context, namespace, name string, index contextresource.QueryIndex) error {
	resource, err := m.GetContext(ctx, namespace, name)
	if err != nil {
		return err
	}
	if resource.Type != "memory" && resource.Type != "knowledge" {
		return fmt.Errorf("%w: query indexes require memory or knowledge contexts", contextresource.ErrInvalid)
	}
	if index.Revision == "" || index.Revision != resource.CurrentRevision {
		return fmt.Errorf("%w: query index revision must match the current context revision", contextresource.ErrInvalid)
	}
	for i := range index.Records {
		record := &index.Records[i]
		if record.ID == "" || strings.TrimSpace(record.Text) == "" {
			return fmt.Errorf("%w: query records require id and text", contextresource.ErrInvalid)
		}
		if record.Context != name || record.Type != resource.Type || record.Revision != index.Revision {
			return fmt.Errorf("%w: query record identity does not match its context", contextresource.ErrInvalid)
		}
	}
	data, err := json.Marshal(index)
	if err != nil {
		return err
	}
	configMap := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name: queryIndexName(name), Namespace: namespace,
		Labels:      map[string]string{managedLabel: managedBy, contextLabel: name, contextTypeLabel: resource.Type},
		Annotations: map[string]string{queryIndexRevisionAnnotation: index.Revision},
	}, Data: map[string]string{"index.json": string(data)}}
	existing, err := m.core.CoreV1().ConfigMaps(namespace).Get(ctx, configMap.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = m.core.CoreV1().ConfigMaps(namespace).Create(ctx, configMap, metav1.CreateOptions{})
	} else if err == nil {
		configMap.ResourceVersion = existing.ResourceVersion
		_, err = m.core.CoreV1().ConfigMaps(namespace).Update(ctx, configMap, metav1.UpdateOptions{})
	}
	if err != nil {
		return fmt.Errorf("publish query index: %w", err)
	}
	return nil
}

func (m *Manager) QueryContexts(ctx context.Context, namespace string, subject contextresource.Subject, request contextresource.QueryRequest) (contextresource.QueryResponse, error) {
	var resources []contextresource.Resource
	if len(request.Contexts) == 0 {
		items, err := m.ListAccessibleContexts(ctx, namespace, subject)
		if err != nil {
			return contextresource.QueryResponse{}, err
		}
		resources = items
	} else {
		seen := map[string]bool{}
		for _, name := range request.Contexts {
			if seen[name] {
				continue
			}
			seen[name] = true
			item, err := m.AccessContext(ctx, namespace, name, subject, contextresource.PermissionRead)
			if err != nil {
				return contextresource.QueryResponse{}, err
			}
			resources = append(resources, item)
		}
	}
	sort.Slice(resources, func(i, j int) bool { return resources[i].Name < resources[j].Name })
	var records []contextresource.QueryRecord
	var unavailable []string
	for _, resource := range resources {
		if resource.Type != "memory" && resource.Type != "knowledge" {
			continue
		}
		configMap, err := m.core.CoreV1().ConfigMaps(namespace).Get(ctx, queryIndexName(resource.Name), metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			unavailable = append(unavailable, resource.Name)
			continue
		}
		if err != nil {
			return contextresource.QueryResponse{}, fmt.Errorf("read query index: %w", err)
		}
		var index contextresource.QueryIndex
		if json.Unmarshal([]byte(configMap.Data["index.json"]), &index) != nil || index.Revision != resource.CurrentRevision {
			unavailable = append(unavailable, resource.Name)
			continue
		}
		records = append(records, index.Records...)
	}
	response, err := contextquery.Search(records, request)
	if err != nil {
		return contextresource.QueryResponse{}, fmt.Errorf("%w: %v", contextresource.ErrInvalid, err)
	}
	response.Unavailable = unavailable
	return response, nil
}

func queryIndexName(contextName string) string {
	base := "context-" + contextName + "-query"
	if len(base) <= 63 {
		return base
	}
	return contextSnapshotName(contextName, "query")
}
