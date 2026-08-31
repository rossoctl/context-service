package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rossoctl/context-service/internal/client"
	"github.com/rossoctl/context-service/internal/contextresource"
	"github.com/rossoctl/context-service/internal/pool"
)

func TestCreateFromWarmPool(t *testing.T) {
	var received pool.CreateRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(pool.Pool{
			Name: "fast-run", Status: "provisioning", Replicas: 3,
			WarmPoolRef: "research-agents", SandboxSelector: "context.rossoctl.io/pool=fast-run",
		})
	}))
	defer server.Close()

	c := client.New(server.URL, "", server.Client())
	if err := createSandboxPool(c, []string{"fast-run", "--warm-pool", "research-agents", "--replicas", "3"}); err != nil {
		t.Fatal(err)
	}
	if received.WarmPoolRef != "research-agents" || received.Replicas != 3 || received.Workspace != (pool.Workspace{}) {
		t.Fatalf("unexpected request: %+v", received)
	}
}

func TestCreateContextDefaults(t *testing.T) {
	t.Setenv("CS_NAMESPACE", "team1")
	var received contextresource.CreateRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/contexts" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(contextresource.Resource{
			Name: received.Name, Namespace: received.Namespace, Type: received.Type,
			Status: "provisioning", Storage: received.Storage,
			Attachment: contextresource.Attachment{Kind: "pvc", ClaimName: "context-demo"},
		})
	}))
	defer server.Close()

	c := client.New(server.URL, "", server.Client())
	if err := createContext(c, []string{"demo", "--storage-class", "local-path"}); err != nil {
		t.Fatal(err)
	}
	if received.Name != "demo" || received.Namespace != "team1" || received.Type != "workspace" {
		t.Fatalf("unexpected request: %+v", received)
	}
	if received.Storage.Backend != "pvc" || received.Storage.Size != "1Gi" ||
		received.Storage.AccessMode != "ReadWriteOnce" || received.Storage.StorageClass != "local-path" {
		t.Fatalf("unexpected storage request: %+v", received.Storage)
	}
}

func TestContextCommandRequiresSubcommand(t *testing.T) {
	err := contextCommand(client.New("http://unused", "", nil), nil)
	if err == nil || !strings.Contains(err.Error(), "context command is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCreateFromWarmPoolRejectsWorkspaceFlags(t *testing.T) {
	c := client.New("http://unused", "", nil)
	err := createSandboxPool(c, []string{"fast-run", "--warm-pool", "research-agents", "--shared"})
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSharedWorkspaceDoesNotChangeReplicaCount(t *testing.T) {
	var received pool.CreateRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(pool.Pool{
			Name: received.Name, Status: "provisioning", Replicas: received.Replicas,
			Workspace: received.Workspace,
		})
	}))
	defer server.Close()

	c := client.New(server.URL, "", server.Client())
	if err := createSandboxPool(c, []string{"demo", "--shared"}); err != nil {
		t.Fatal(err)
	}
	if received.Replicas != 1 {
		t.Fatalf("replicas = %d, want 1", received.Replicas)
	}
	if received.Workspace.AccessMode != "ReadWriteMany" {
		t.Fatalf("access mode = %q, want ReadWriteMany", received.Workspace.AccessMode)
	}
}

func TestRunRequiresResourceFirstForSandboxPool(t *testing.T) {
	err := run([]string{"create", "demo"})
	if err == nil || !strings.Contains(err.Error(), "use 'contextctl sandbox-pool create'") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSandboxPoolCommandRequiresSubcommand(t *testing.T) {
	err := sandboxPoolCommand(client.New("http://unused", "", nil), nil)
	if err == nil || !strings.Contains(err.Error(), "sandbox-pool command is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResourceAliases(t *testing.T) {
	tests := []struct {
		alias string
		want  string
	}{
		{alias: "sb", want: "sandbox-pool command is required"},
		{alias: "ctx", want: "context command is required"},
		{alias: "sc", want: "storage-class command is required"},
	}
	for _, test := range tests {
		t.Run(test.alias, func(t *testing.T) {
			err := run([]string{test.alias})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestListSandboxPools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/sandbox-pools" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer server.Close()

	c := client.New(server.URL, "", server.Client())
	if err := listSandboxPools(c, nil); err != nil {
		t.Fatal(err)
	}
}

func TestWarmPoolResponseJSONRoundTrip(t *testing.T) {
	value := pool.Pool{Name: "fast-run", WarmPoolRef: "research-agents"}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var decoded pool.Pool
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.WarmPoolRef != value.WarmPoolRef {
		t.Fatalf("warm pool ref = %q", decoded.WarmPoolRef)
	}
}
