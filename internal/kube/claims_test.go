package kube

import (
	"context"
	"testing"

	"github.com/rossoctl/context-service/internal/pool"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
)

func TestManagerCreatesGetsAndDeletesWarmPoolClaims(t *testing.T) {
	warmPool := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "extensions.agents.x-k8s.io/v1beta1",
		"kind":       "SandboxWarmPool",
		"metadata": map[string]any{
			"name": "research-agents", "namespace": "serverless-harness",
		},
	}}
	listKinds := map[schema.GroupVersionResource]string{
		sandboxClaimResource:    "SandboxClaimList",
		sandboxWarmPoolResource: "SandboxWarmPoolList",
		sandboxResource:         "SandboxList",
	}
	dynamicClient := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, warmPool)
	manager := &Manager{
		config: Config{Namespace: "serverless-harness"},
		core:   kubernetesfake.NewSimpleClientset(), dynamic: dynamicClient,
	}

	created, err := manager.Create(context.Background(), pool.CreateRequest{
		Name: "fast-run", Replicas: 3, WarmPoolRef: "research-agents",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.WarmPoolRef != "research-agents" || created.Replicas != 3 || created.ReadyReplicas != 0 {
		t.Fatalf("unexpected pool: %+v", created)
	}
	claims, err := dynamicClient.Resource(sandboxClaimResource).Namespace("serverless-harness").List(
		context.Background(), metav1.ListOptions{LabelSelector: selectorFor("fast-run")},
	)
	if err != nil || len(claims.Items) != 3 {
		t.Fatalf("claims = %d, err = %v", len(claims.Items), err)
	}

	for index := range claims.Items {
		claims.Items[index].Object["status"] = map[string]any{
			"conditions": []any{map[string]any{"type": "Ready", "status": "True"}},
		}
		if _, err := dynamicClient.Resource(sandboxClaimResource).Namespace("serverless-harness").UpdateStatus(
			context.Background(), &claims.Items[index], metav1.UpdateOptions{},
		); err != nil {
			t.Fatal(err)
		}
	}
	ready, err := manager.Get(context.Background(), "fast-run")
	if err != nil {
		t.Fatal(err)
	}
	if ready.Status != "ready" || ready.ReadyReplicas != 3 {
		t.Fatalf("unexpected ready pool: %+v", ready)
	}

	if err := manager.Delete(context.Background(), "fast-run"); err != nil {
		t.Fatal(err)
	}
	claims, err = dynamicClient.Resource(sandboxClaimResource).Namespace("serverless-harness").List(
		context.Background(), metav1.ListOptions{LabelSelector: selectorFor("fast-run")},
	)
	if err != nil || len(claims.Items) != 0 {
		t.Fatalf("claims after delete = %d, err = %v", len(claims.Items), err)
	}
}

func TestManagerRejectsMissingWarmPool(t *testing.T) {
	listKinds := map[schema.GroupVersionResource]string{
		sandboxClaimResource:    "SandboxClaimList",
		sandboxWarmPoolResource: "SandboxWarmPoolList",
	}
	manager := &Manager{
		config: Config{Namespace: "serverless-harness"},
		core:   kubernetesfake.NewSimpleClientset(),
		dynamic: fake.NewSimpleDynamicClientWithCustomListKinds(
			runtime.NewScheme(), listKinds,
		),
	}
	_, err := manager.Create(context.Background(), pool.CreateRequest{
		Name: "fast-run", Replicas: 1, WarmPoolRef: "missing",
	})
	if err == nil {
		t.Fatal("expected missing warm pool error")
	}
}
