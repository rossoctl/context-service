package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rossoctl/context-service/internal/client"
	"github.com/rossoctl/context-service/internal/contextresource"
	"github.com/rossoctl/context-service/internal/pool"
)

func TestHelpDefinesCoreConcepts(t *testing.T) {
	for _, expected := range []string{
		"Context               Persistent agent data, such as a workspace, memory, or artifacts",
		"Sandbox pool          One or more isolated agent environments with workspace context",
		"Sandbox profile       Platform-managed runtime settings for sandbox Pods",
		"Storage class         Kubernetes storage available to contexts and sandboxes",
	} {
		if !strings.Contains(help, expected) {
			t.Errorf("help missing %q:\n%s", expected, help)
		}
	}
}

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

func TestCreateWithSandboxProfile(t *testing.T) {
	var received pool.CreateRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(pool.Pool{
			Name: received.Name, Status: "provisioning", Replicas: received.Replicas,
			SandboxProfile: received.SandboxProfile, Workspace: received.Workspace,
		})
	}))
	defer server.Close()

	c := client.New(server.URL, "", server.Client())
	if err := createSandboxPool(c, []string{"developer", "--sandbox-profile", "python-tools"}); err != nil {
		t.Fatal(err)
	}
	if received.SandboxProfile != "python-tools" {
		t.Fatalf("sandbox profile = %q", received.SandboxProfile)
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

func TestTreeViews(t *testing.T) {
	var output bytes.Buffer
	writePools(&output, []pool.Pool{{
		Name: "review", Status: "ready", Replicas: 2, ReadyReplicas: 2,
		SandboxProfile: "developer",
		Workspace:      pool.Workspace{Size: "1Gi", AccessMode: "ReadWriteOnce"},
		Resources: []pool.KubernetesResource{
			{Kind: "sandbox", Name: "sandbox-review-0", Status: "Ready"},
			{Kind: "pod", Name: "sandbox-review-0", Status: "Running"},
			{Kind: "pvc", Name: "review-workspace-0", Status: "Bound"},
		},
	}})
	for _, expected := range []string{
		"SANDBOX POOLS (1)",
		"review  Ready · 2/2 · dedicated · 1Gi RWO · profile developer",
		"└── sandbox/sandbox-review-0  Ready",
		"    ├── pod/sandbox-review-0  Running",
		"    └── workspace → pvc/review-workspace-0  Bound",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Errorf("output missing %q:\n%s", expected, output.String())
		}
	}

	output.Reset()
	writeContexts(&output, []contextresource.Resource{{
		Name: "demo", Type: "workspace", Status: "provisioning",
		Storage:    contextresource.Storage{Size: "1Gi", AccessMode: "ReadWriteOnce", StorageClass: "local-path"},
		Attachment: contextresource.Attachment{Kind: "pvc", ClaimName: "context-demo"},
	}})
	for _, expected := range []string{
		"CONTEXTS (1)", "demo  Provisioning · workspace", "└── pvc/context-demo  1Gi RWO · local-path",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Errorf("output missing %q:\n%s", expected, output.String())
		}
	}
}

func TestTreeViewShowsSharedWorkspaceUnderEverySandbox(t *testing.T) {
	var output bytes.Buffer
	writePools(&output, []pool.Pool{{
		Name: "team", Status: "ready", Replicas: 2, ReadyReplicas: 2,
		Workspace: pool.Workspace{Size: "1Gi", AccessMode: "ReadWriteMany"},
		Resources: []pool.KubernetesResource{
			{Kind: "sandbox", Name: "sandbox-team-0", Status: "Ready"},
			{Kind: "sandbox", Name: "sandbox-team-1", Status: "Ready"},
			{Kind: "pod", Name: "sandbox-team-0", Status: "Running"},
			{Kind: "pod", Name: "sandbox-team-1", Status: "Running"},
			{Kind: "pvc", Name: "team-workspace", Status: "Bound"},
		},
	}})
	if count := strings.Count(output.String(), "workspace → pvc/team-workspace  Bound"); count != 2 {
		t.Fatalf("shared PVC attachment count = %d, want 2:\n%s", count, output.String())
	}
}

func TestLoadStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/sandbox-pools":
			_, _ = w.Write([]byte(`{"items":[{"name":"review","status":"ready","replicas":1,"readyReplicas":1,"workspace":{"size":"1Gi","accessMode":"ReadWriteOnce"}}]}`))
		case "/v1/namespaces/team1/contexts":
			_, _ = w.Write([]byte(`{"items":[{"name":"demo","namespace":"team1","type":"workspace","status":"ready","storage":{"backend":"pvc","size":"1Gi","accessMode":"ReadWriteOnce"},"attachment":{"kind":"pvc","claimName":"context-demo"}}]}`))
		case "/v1/storage-classes":
			_, _ = w.Write([]byte(`{"items":[{"name":"standard","default":true}]}`))
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	view, err := loadStatus(client.New(server.URL, "", server.Client()), "team1")
	if err != nil {
		t.Fatal(err)
	}
	if view.Namespace != "team1" || len(view.SandboxPools) != 1 || len(view.Contexts) != 1 || len(view.StorageClasses) != 1 {
		t.Fatalf("unexpected status: %+v", view)
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
