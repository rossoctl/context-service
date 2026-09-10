package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rossoctl/context-service/internal/client"
	"github.com/rossoctl/context-service/internal/contextresource"
)

func TestContextAccessShowsReadOnlyAndConsumers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/namespaces/team1/contexts/demo":
			_ = json.NewEncoder(w).Encode(contextresource.Resource{Name: "demo", EffectiveAccess: []contextresource.Permission{contextresource.PermissionRead, contextresource.PermissionAttach}})
		case "/v1/namespaces/team1/contexts/demo/consumers":
			_ = json.NewEncoder(w).Encode(contextresource.ConsumerList{Items: []contextresource.Consumer{{Kind: "pod", Name: "agent-0", Active: true}}})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	var commandErr error
	output := captureStdout(t, func() {
		commandErr = contextAccess(client.New(server.URL, "", server.Client()), []string{"demo", "--namespace", "team1"})
	})
	if commandErr != nil {
		t.Fatal(commandErr)
	}
	for _, expected := range []string{"demo  read-only", "Permissions: read,attach", "Consumers:   1"} {
		if !strings.Contains(output, expected) {
			t.Errorf("output missing %q: %s", expected, output)
		}
	}
}

func TestContextGrantSendsExplicitPermissions(t *testing.T) {
	var received contextresource.Grant
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/v1/namespaces/team1/contexts/demo/grants" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"demo","storage":{},"attachment":{}}`))
	}))
	defer server.Close()
	if err := contextGrant(client.New(server.URL, "", server.Client()), []string{"demo", "--namespace", "team1", "--subject", "agent:reader", "--permissions", "read,attach"}); err != nil {
		t.Fatal(err)
	}
	if received.Subject.Name != "reader" || len(received.Permissions) != 2 {
		t.Fatalf("grant = %+v", received)
	}
}
