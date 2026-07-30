package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rossoctl/context-service/internal/client"
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
	if err := create(c, []string{"fast-run", "--warm-pool", "research-agents", "-n", "3"}); err != nil {
		t.Fatal(err)
	}
	if received.WarmPoolRef != "research-agents" || received.Replicas != 3 || received.Workspace != (pool.Workspace{}) {
		t.Fatalf("unexpected request: %+v", received)
	}
}

func TestCreateFromWarmPoolRejectsWorkspaceFlags(t *testing.T) {
	c := client.New("http://unused", "", nil)
	err := create(c, []string{"fast-run", "--warm-pool", "research-agents", "--shared"})
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("unexpected error: %v", err)
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
