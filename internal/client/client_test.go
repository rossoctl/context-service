package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rossoctl/context-service/internal/pool"
)

func TestCreate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/sandbox-pools" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"name":"demo","status":"provisioning","replicas":2,"readyReplicas":0,"sandboxSelector":"context.rossoctl.io/pool=demo","workspace":{"size":"1Gi","accessMode":"ReadWriteMany"}}`))
	}))
	defer server.Close()

	result, err := New(server.URL, "", nil).Create(context.Background(), pool.CreateRequest{Name: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Name != "demo" || result.Replicas != 2 {
		t.Fatalf("unexpected pool: %+v", result)
	}
}

func TestAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not_found","message":"sandbox pool not found"}`))
	}))
	defer server.Close()

	_, err := New(server.URL, "", nil).Get(context.Background(), "missing")
	if err == nil || err.Error() != "sandbox pool not found" {
		t.Fatalf("unexpected error: %v", err)
	}
}
