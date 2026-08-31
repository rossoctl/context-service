package kube

import (
	"context"
	"testing"

	"github.com/rossoctl/context-service/internal/pool"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
)

func TestManagerListsDirectSandboxPools(t *testing.T) {
	config := Config{Namespace: "serverless-harness", SandboxImage: "example/sandbox:latest"}
	request := pool.CreateRequest{
		Name: "review", Replicas: 1,
		Workspace: pool.Workspace{Size: "1Gi", AccessMode: "ReadWriteOnce"},
	}
	pvc := buildPVC(config.Namespace, request, 0)
	sandbox := buildSandbox(config, request, 0)
	listKinds := map[schema.GroupVersionResource]string{
		sandboxClaimResource: "SandboxClaimList",
		sandboxResource:      "SandboxList",
	}
	manager := &Manager{
		config:  config,
		core:    kubernetesfake.NewSimpleClientset(pvc),
		dynamic: fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds),
	}
	if _, err := manager.dynamic.Resource(sandboxResource).Namespace(config.Namespace).Create(
		context.Background(), sandbox, metav1.CreateOptions{},
	); err != nil {
		t.Fatal(err)
	}

	items, err := manager.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Name != "review" || items[0].Replicas != 1 {
		t.Fatalf("unexpected pools: %+v", items)
	}
	if len(items[0].Resources) != 2 || items[0].Resources[0].Kind != "sandbox" || items[0].Resources[1].Kind != "pvc" {
		t.Fatalf("unexpected Kubernetes resources: %+v", items[0].Resources)
	}
}
