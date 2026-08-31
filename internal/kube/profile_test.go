package kube

import (
	"context"
	"errors"
	"testing"

	"github.com/rossoctl/context-service/internal/pool"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
)

func TestResolveSandboxProfile(t *testing.T) {
	template := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "extensions.agents.x-k8s.io/v1beta1",
		"kind":       "SandboxTemplate",
		"metadata": map[string]any{
			"name": "developer", "namespace": "serverless-harness",
		},
		"spec": map[string]any{
			"networkPolicyManagement": "Managed",
			"podTemplate": map[string]any{"spec": map[string]any{
				"containers": []any{map[string]any{"name": "agent", "image": "example/developer:latest"}},
			}},
		},
	}}
	manager := &Manager{
		config: Config{Namespace: "serverless-harness"},
		dynamic: fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
			sandboxTemplateResource: "SandboxTemplateList",
		}, template),
	}

	profile, err := manager.resolveSandboxProfile(context.Background(), "developer")
	if err != nil {
		t.Fatal(err)
	}
	if _, found := profile["networkPolicyManagement"]; found {
		t.Fatal("template-only policy leaked into Sandbox blueprint")
	}
	podTemplate, ok := profile["podTemplate"].(map[string]any)
	if !ok || podTemplate["spec"] == nil {
		t.Fatalf("profile podTemplate was not preserved: %#v", profile)
	}
}

func TestResolveSandboxProfileRejectsMissingProfile(t *testing.T) {
	manager := &Manager{
		config: Config{Namespace: "serverless-harness"},
		dynamic: fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
			sandboxTemplateResource: "SandboxTemplateList",
		}),
	}
	_, err := manager.resolveSandboxProfile(context.Background(), "missing")
	if !errors.Is(err, pool.ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}
