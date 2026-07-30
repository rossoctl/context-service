package kube

import (
	"fmt"
	"testing"

	"github.com/rossoctl/context-service/internal/pool"
)

func TestBuildSharedPoolResources(t *testing.T) {
	request := pool.CreateRequest{
		Name: "bugstone-1", Replicas: 3,
		Workspace: pool.Workspace{Size: "5Gi", AccessMode: "ReadWriteMany", StorageClass: "ibm-scale-csi"},
	}
	pvc := buildPVC("serverless-harness", request, 0)
	if pvc.Name != "bugstone-1-workspace" || pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != "ibm-scale-csi" {
		t.Fatalf("unexpected PVC: %#v", pvc)
	}

	sandbox := buildSandbox(Config{Namespace: "serverless-harness", SandboxImage: "sandbox:test"}, request, 2)
	if sandbox.GetName() != "sandbox-bugstone-1-2" {
		t.Fatalf("sandbox name = %q", sandbox.GetName())
	}
	claim, found, err := unstructuredNestedString(sandbox.Object, "spec", "podTemplate", "spec", "volumes", "0", "persistentVolumeClaim", "claimName")
	if err != nil || !found || claim != "bugstone-1-workspace" {
		t.Fatalf("workspace claim = %q, found=%v, err=%v", claim, found, err)
	}
}

func TestBuildDedicatedPoolResources(t *testing.T) {
	request := pool.CreateRequest{
		Name: "review", Replicas: 3,
		Workspace: pool.Workspace{Size: "2Gi", AccessMode: "ReadWriteOnce", StorageClass: "ibm-scale-csi"},
	}
	for index := 0; index < request.Replicas; index++ {
		pvc := buildPVC("serverless-harness", request, index)
		expected := fmt.Sprintf("review-workspace-%d", index)
		if pvc.Name != expected {
			t.Fatalf("PVC %d name = %q, want %q", index, pvc.Name, expected)
		}
		sandbox := buildSandbox(Config{Namespace: "serverless-harness", SandboxImage: "sandbox:test"}, request, index)
		claim, found, err := unstructuredNestedString(sandbox.Object, "spec", "podTemplate", "spec", "volumes", "0", "persistentVolumeClaim", "claimName")
		if err != nil || !found || claim != expected {
			t.Fatalf("sandbox %d claim = %q, found=%v, err=%v", index, claim, found, err)
		}
	}
}

func TestBuildSandboxWithExistingReadOnlyClaim(t *testing.T) {
	readOnly := true
	request := pool.CreateRequest{
		Name: "readers", Replicas: 2,
		Workspace: pool.Workspace{ClaimName: "prepared-workspace", ReadOnly: &readOnly},
	}
	sandbox := buildSandbox(Config{Namespace: "serverless-harness", SandboxImage: "sandbox:test"}, request, 0)
	claim, found, err := unstructuredNestedString(sandbox.Object, "spec", "podTemplate", "spec", "volumes", "0", "persistentVolumeClaim", "claimName")
	if err != nil || !found || claim != "prepared-workspace" {
		t.Fatalf("workspace claim = %q, found=%v, err=%v", claim, found, err)
	}
	volumeReadOnly, found, err := unstructuredNestedBool(sandbox.Object, "spec", "podTemplate", "spec", "volumes", "0", "persistentVolumeClaim", "readOnly")
	if err != nil || !found || !volumeReadOnly {
		t.Fatalf("volume readOnly = %v, found=%v, err=%v", volumeReadOnly, found, err)
	}
	mountReadOnly, found, err := unstructuredNestedBool(sandbox.Object, "spec", "podTemplate", "spec", "containers", "0", "volumeMounts", "0", "readOnly")
	if err != nil || !found || !mountReadOnly {
		t.Fatalf("mount readOnly = %v, found=%v, err=%v", mountReadOnly, found, err)
	}
}

func TestBuildSandboxWithExistingReadWriteClaim(t *testing.T) {
	readOnly := false
	request := pool.CreateRequest{
		Name: "writer", Replicas: 1,
		Workspace: pool.Workspace{ClaimName: "prepared-workspace", ReadOnly: &readOnly},
	}
	sandbox := buildSandbox(Config{Namespace: "serverless-harness", SandboxImage: "sandbox:test"}, request, 0)
	volumeReadOnly, found, err := unstructuredNestedBool(sandbox.Object, "spec", "podTemplate", "spec", "volumes", "0", "persistentVolumeClaim", "readOnly")
	if err != nil || !found || volumeReadOnly {
		t.Fatalf("volume readOnly = %v, found=%v, err=%v", volumeReadOnly, found, err)
	}
	mountReadOnly, found, err := unstructuredNestedBool(sandbox.Object, "spec", "podTemplate", "spec", "containers", "0", "volumeMounts", "0", "readOnly")
	if err != nil || !found || mountReadOnly {
		t.Fatalf("mount readOnly = %v, found=%v, err=%v", mountReadOnly, found, err)
	}
}

func TestBuildSandboxClaim(t *testing.T) {
	request := pool.CreateRequest{Name: "fast-run", Replicas: 3, WarmPoolRef: "research-agents"}
	claim := buildSandboxClaim("serverless-harness", request, 2)
	if claim.GetName() != "claim-fast-run-2" || claim.GetKind() != "SandboxClaim" {
		t.Fatalf("unexpected claim identity: %s %s", claim.GetKind(), claim.GetName())
	}
	warmPool, found, err := unstructuredNestedString(claim.Object, "spec", "warmPoolRef", "name")
	if err != nil || !found || warmPool != "research-agents" {
		t.Fatalf("warm pool = %q, found=%v, err=%v", warmPool, found, err)
	}
	poolName, found, err := unstructuredNestedString(claim.Object, "spec", "additionalPodMetadata", "labels", poolLabel)
	if err != nil || !found || poolName != "fast-run" {
		t.Fatalf("pod pool label = %q, found=%v, err=%v", poolName, found, err)
	}
}

// This tiny helper navigates maps and the single list used by the generated Sandbox.
func unstructuredNestedString(object map[string]any, fields ...string) (string, bool, error) {
	value, found := unstructuredNestedValue(object, fields...)
	result, ok := value.(string)
	return result, found && ok, nil
}

func unstructuredNestedBool(object map[string]any, fields ...string) (bool, bool, error) {
	value, found := unstructuredNestedValue(object, fields...)
	result, ok := value.(bool)
	return result, found && ok, nil
}

func unstructuredNestedValue(object map[string]any, fields ...string) (any, bool) {
	var current any = object
	for _, field := range fields {
		switch value := current.(type) {
		case map[string]any:
			current = value[field]
		case []any:
			if field != "0" || len(value) == 0 {
				return nil, false
			}
			current = value[0]
		default:
			return nil, false
		}
	}
	return current, true
}
