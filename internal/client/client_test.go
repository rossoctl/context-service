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

func TestListPools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/sandbox-pools" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"name":"demo","status":"ready","replicas":2,"readyReplicas":2,"sandboxSelector":"context.rossoctl.io/pool=demo","workspace":{"size":"1Gi","accessMode":"ReadWriteMany"}}]}`))
	}))
	defer server.Close()

	result, err := New(server.URL, "", nil).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 || result[0].Name != "demo" || result[0].ReadyReplicas != 2 {
		t.Fatalf("unexpected pools: %+v", result)
	}
}

func TestListStorageClasses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/storage-classes" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"name":"local-path","default":true,"provisioner":"rancher.io/local-path","volumeBindingMode":"WaitForFirstConsumer","reclaimPolicy":"Delete","allowVolumeExpansion":false}]}`))
	}))
	defer server.Close()

	result, err := New(server.URL, "", nil).ListStorageClasses(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 || result[0].Name != "local-path" || !result[0].Default {
		t.Fatalf("unexpected storage classes: %+v", result)
	}
}

func TestContextResourcePaths(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		body   string
		call   func(*Client) error
	}{
		{
			name: "list", method: http.MethodGet, path: "/v1/namespaces/team1/contexts",
			body: `{"items":[]}`,
			call: func(c *Client) error {
				_, err := c.ListContexts(context.Background(), "team1")
				return err
			},
		},
		{
			name: "get", method: http.MethodGet, path: "/v1/namespaces/team1/contexts/demo",
			body: `{"name":"demo","namespace":"team1","type":"workspace","status":"ready","storage":{"backend":"pvc","size":"1Gi","accessMode":"ReadWriteOnce"},"attachment":{"kind":"pvc","claimName":"context-demo"}}`,
			call: func(c *Client) error {
				_, err := c.GetContext(context.Background(), "team1", "demo")
				return err
			},
		},
		{
			name: "delete", method: http.MethodDelete, path: "/v1/namespaces/team1/contexts/demo",
			call: func(c *Client) error {
				return c.DeleteContext(context.Background(), "team1", "demo")
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != test.method || r.URL.Path != test.path {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				if test.body != "" {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(test.body))
					return
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()

			if err := test.call(New(server.URL, "", server.Client())); err != nil {
				t.Fatal(err)
			}
		})
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
