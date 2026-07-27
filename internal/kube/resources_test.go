package kube

import (
	"testing"

	"github.com/rossoctl/context-service/internal/pool"
)

func TestBuildSharedPoolResources(t *testing.T) {
	request := pool.CreateRequest{
		Name: "bugstone-1", Replicas: 3,
		Workspace: pool.Workspace{Size: "5Gi", AccessMode: "ReadWriteMany", StorageClass: "ibm-scale-csi"},
	}
	pvc := buildPVC("serverless-harness", request)
	if pvc.Name != "bugstone-1-workspace" || pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != "ibm-scale-csi" {
		t.Fatalf("unexpected PVC: %#v", pvc)
	}

	sandbox := buildSandbox(Config{Namespace: "serverless-harness", SandboxImage: "sandbox:test"}, request, 2)
	if sandbox.GetName() != "bugstone-1-2" {
		t.Fatalf("sandbox name = %q", sandbox.GetName())
	}
	claim, found, err := unstructuredNestedString(sandbox.Object, "spec", "podTemplate", "spec", "volumes", "0", "persistentVolumeClaim", "claimName")
	if err != nil || !found || claim != "bugstone-1-workspace" {
		t.Fatalf("workspace claim = %q, found=%v, err=%v", claim, found, err)
	}
}

// This tiny helper navigates maps and the single list used by the generated Sandbox.
func unstructuredNestedString(object map[string]any, fields ...string) (string, bool, error) {
	var current any = object
	for _, field := range fields {
		switch value := current.(type) {
		case map[string]any:
			current = value[field]
		case []any:
			if field != "0" || len(value) == 0 {
				return "", false, nil
			}
			current = value[0]
		default:
			return "", false, nil
		}
	}
	result, ok := current.(string)
	return result, ok, nil
}
