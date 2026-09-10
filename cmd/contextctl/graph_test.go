package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rossoctl/context-service/internal/contextbackup"
	"github.com/rossoctl/context-service/internal/contextresource"
	"github.com/rossoctl/context-service/internal/localcontext"
)

func TestContextGraphSnapshot(t *testing.T) {
	state := localcontext.Manifest{
		Name: "project-state", Type: "state", Backend: "filesystem", Path: "/contexts/project-state",
		CurrentRevision: "state-new-12345678",
		Revisions: []localcontext.PublishedRevision{
			{ID: "state-old-12345678"}, {ID: "state-new-12345678"},
		},
		Attachments: map[string]localcontext.Attachment{
			"claude": {Harness: "claude", Project: "/work/project"},
		},
	}
	memory := localcontext.Manifest{
		Name: "project-memory", Type: "memory", Backend: "filesystem", Path: "/contexts/project-memory",
		CurrentRevision: "memory-12345678",
		Revisions: []localcontext.PublishedRevision{{
			ID: "memory-12345678", Sources: []localcontext.SourceReference{{
				Context: "project-state", Type: "state", Revision: "state-old-12345678",
			}},
		}},
	}
	knowledge := localcontext.Manifest{
		Name: "project-knowledge", Type: "knowledge", Backend: "filesystem", Path: "/contexts/project-knowledge",
		CurrentRevision: "knowledge-12345678",
		Revisions: []localcontext.PublishedRevision{{
			ID: "knowledge-12345678", Sources: []localcontext.SourceReference{{
				Context: "project-memory", Type: "memory", Revision: "memory-12345678",
			}},
		}},
	}
	pvc := contextresource.Resource{
		Name: "cloud-state", Namespace: "team1", Type: "state", Status: "ready",
		Storage:         contextresource.Storage{Backend: "pvc"},
		Attachment:      contextresource.Attachment{Kind: "pvc", ClaimName: "context-cloud-state"},
		CurrentRevision: "state-new-12345678",
		Revisions:       []contextresource.Revision{{ID: "state-new-12345678"}},
	}
	view := buildGraph("team1", []localcontext.Manifest{knowledge, state, memory}, []contextresource.Resource{pvc},
		map[string][]contextresource.Consumer{
			"team1/cloud-state": {{Kind: "pod", Name: "agent-0", AccessMode: "ReadOnlyMany", Active: true}},
		},
		map[string]graphBackup{
			"project-state": {
				Config: contextbackup.NewConfig("pvc://team1/cloud-state", time.Minute, time.Second),
				Status: contextbackup.Status{LastSuccess: time.Unix(1, 0), SourceRevision: "state-new-12345678"},
			},
		}, nil)

	var output bytes.Buffer
	writeGraph(&output, view)
	want := `CONTEXT GRAPH (3)
project-knowledge  knowledge · filesystem · Ready · knowledge-12
├── location  /contexts/project-knowledge
└── source  project-memory@memory-12345 · memory · filesystem · ready

project-memory  memory · filesystem · Ready · memory-12345
├── location  /contexts/project-memory
├── source  project-state@state-old-12 · state · filesystem · stale
└── derives  project-knowledge@knowledge-12 · knowledge · filesystem · ready

project-state  state · filesystem · Ready · state-new-12
├── location  /contexts/project-state
├── derives  project-memory@memory-12345 · memory · filesystem · stale
├── consumer  harness/Claude · /work/project · attached
├── consumer  pod/agent-0 · ReadOnlyMany · active
└── copy  pvc/context-cloud-state · namespace team1 · state-new-12 · ready
`
	if output.String() != want {
		t.Fatalf("graph snapshot mismatch\n--- got ---\n%s--- want ---\n%s", output.String(), want)
	}
}

func TestGraphCopyStates(t *testing.T) {
	current := "revision-current"
	if got := backupState(current, contextbackup.Status{Running: true}); got != "syncing" {
		t.Fatalf("running copy state = %q", got)
	}
	if got := backupState(current, contextbackup.Status{LastError: "upload failed"}); got != "failed" {
		t.Fatalf("failed copy state = %q", got)
	}
	if got := backupState(current, contextbackup.Status{}); got != "missing" {
		t.Fatalf("new copy state = %q", got)
	}
	if got := backupState(current, contextbackup.Status{LastSuccess: time.Now(), SourceRevision: "old"}); got != "stale" {
		t.Fatalf("old copy state = %q", got)
	}
	if got := backupState(current, contextbackup.Status{LastSuccess: time.Now(), SourceRevision: current}); got != "ready" {
		t.Fatalf("current copy state = %q", got)
	}
}

func TestContextGraphJSONDoesNotExposeInternalRevisionIndex(t *testing.T) {
	view := graphView{Contexts: []graphNode{{Name: "demo", Type: "state", Backend: "filesystem", revisionIDs: []string{"secret-internal-index"}}}}
	encoded := mustJSON(t, view)
	if strings.Contains(encoded, "secret-internal-index") {
		t.Fatalf("internal revision index leaked in JSON: %s", encoded)
	}
}

func TestContextGraphCommandFilesystemOnly(t *testing.T) {
	t.Setenv("CS_CONTEXT_HOME", filepath.Join(t.TempDir(), "contexts"))
	if err := run([]string{"ctx", "create", "demo", "--type", "state", "--backend", "filesystem"}); err != nil {
		t.Fatal(err)
	}
	var graphErr error
	output := captureStdout(t, func() {
		graphErr = run([]string{"ctx", "graph", "--backend", "filesystem"})
	})
	if graphErr != nil {
		t.Fatal(graphErr)
	}
	for _, expected := range []string{"CONTEXT GRAPH (1)", "demo  state · filesystem · Ready", "location"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("graph output missing %q:\n%s", expected, output)
		}
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
