package kube

import (
	"context"
	"fmt"

	"github.com/rossoctl/context-service/internal/pool"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// resolveSandboxProfile returns the Sandbox-compatible blueprint stored in a
// platform-managed SandboxTemplate. An empty profile selects the built-in
// sandbox configuration and does not require the extensions API.
func (m *Manager) resolveSandboxProfile(ctx context.Context, name string) (map[string]any, error) {
	if name == "" {
		return nil, nil
	}
	template, err := m.dynamic.Resource(sandboxTemplateResource).Namespace(m.config.Namespace).Get(
		ctx, name, metav1.GetOptions{},
	)
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("%w: sandbox profile %q not found", pool.ErrInvalid, name)
	}
	if err != nil {
		return nil, fmt.Errorf("get sandbox profile %q: %w", name, err)
	}
	spec, found, err := unstructured.NestedMap(template.Object, "spec")
	if err != nil || !found {
		return nil, fmt.Errorf("%w: sandbox profile %q has no spec", pool.ErrInvalid, name)
	}

	// These settings belong to SandboxTemplate itself, not to the Sandbox
	// blueprint copied into a direct allocation.
	delete(spec, "networkPolicy")
	delete(spec, "networkPolicyManagement")
	delete(spec, "envVarsInjectionPolicy")
	delete(spec, "volumeClaimTemplatesPolicy")
	return spec, nil
}

func buildSandboxWithProfile(config Config, request pool.CreateRequest, index int, profile map[string]any) (*unstructured.Unstructured, error) {
	if profile == nil {
		return buildDefaultSandbox(config, request, index), nil
	}

	spec := runtime.DeepCopyJSONValue(profile).(map[string]any)
	podTemplate, found, err := unstructured.NestedMap(spec, "podTemplate")
	if err != nil || !found {
		return nil, fmt.Errorf("%w: sandbox profile %q has no podTemplate", pool.ErrInvalid, request.SandboxProfile)
	}
	podSpec, found, err := unstructured.NestedMap(podTemplate, "spec")
	if err != nil || !found {
		return nil, fmt.Errorf("%w: sandbox profile %q has no podTemplate.spec", pool.ErrInvalid, request.SandboxProfile)
	}
	containers, found, err := unstructured.NestedSlice(podSpec, "containers")
	if err != nil || !found || len(containers) == 0 {
		return nil, fmt.Errorf("%w: sandbox profile %q must define a container", pool.ErrInvalid, request.SandboxProfile)
	}
	container, ok := containers[0].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: sandbox profile %q has an invalid first container", pool.ErrInvalid, request.SandboxProfile)
	}
	reservedWorkspace := hasNamedItem(podSpec["volumes"], workspaceName)
	for _, raw := range containers {
		candidate, ok := raw.(map[string]any)
		if ok && hasNamedItem(candidate["volumeMounts"], workspaceName) {
			reservedWorkspace = true
		}
	}
	if reservedWorkspace {
		return nil, fmt.Errorf("%w: sandbox profile %q uses reserved volume name %q", pool.ErrInvalid, request.SandboxProfile, workspaceName)
	}
	if len(asSlice(spec["volumeClaimTemplates"])) > 0 {
		return nil, fmt.Errorf("%w: sandbox profile %q must not define volumeClaimTemplates; request storage through workspace", pool.ErrInvalid, request.SandboxProfile)
	}

	labels := map[string]any{poolLabel: request.Name, managedLabel: managedBy}
	metadata, _, _ := unstructured.NestedMap(podTemplate, "metadata")
	if metadata == nil {
		metadata = map[string]any{}
	}
	profileLabels, _, _ := unstructured.NestedMap(metadata, "labels")
	if profileLabels == nil {
		profileLabels = map[string]any{}
	}
	for key, value := range labels {
		profileLabels[key] = value
	}
	metadata["labels"] = profileLabels
	podTemplate["metadata"] = metadata

	readOnly := request.Workspace.ReadOnly != nil && *request.Workspace.ReadOnly
	claimName := pvcName(request, index)
	container["volumeMounts"] = append(asSlice(container["volumeMounts"]), map[string]any{
		"name": workspaceName, "mountPath": workspaceMount, "readOnly": readOnly,
	})
	containers[0] = container
	podSpec["containers"] = containers
	podSpec["volumes"] = append(asSlice(podSpec["volumes"]), map[string]any{
		"name":                  workspaceName,
		"persistentVolumeClaim": map[string]any{"claimName": claimName, "readOnly": readOnly},
	})
	if config.SandboxServiceAccount != "" {
		if _, found := podSpec["serviceAccountName"]; !found {
			podSpec["serviceAccountName"] = config.SandboxServiceAccount
		}
	}
	podTemplate["spec"] = podSpec
	spec["podTemplate"] = podTemplate

	annotations := sandboxAnnotations(request, claimName, readOnly)
	return sandboxResourceObject(config.Namespace, request, index, labels, annotations, spec), nil
}

func hasNamedItem(value any, name string) bool {
	for _, raw := range asSlice(value) {
		item, ok := raw.(map[string]any)
		if ok && item["name"] == name {
			return true
		}
	}
	return false
}

func asSlice(value any) []any {
	if value == nil {
		return nil
	}
	items, _ := value.([]any)
	return items
}
