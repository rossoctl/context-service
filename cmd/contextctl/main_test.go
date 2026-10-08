package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rossoctl/context-service/internal/client"
	"github.com/rossoctl/context-service/internal/contextbackup"
	"github.com/rossoctl/context-service/internal/contextresource"
	"github.com/rossoctl/context-service/internal/contextsync"
	"github.com/rossoctl/context-service/internal/localcontext"
)

func TestHelpDefinesCoreConcepts(t *testing.T) {
	for _, expected := range []string{
		"Context               Persistent agent data, such as a workspace, memory, or artifacts",
		"Storage class         Kubernetes storage available to contexts",
	} {
		if !strings.Contains(help, expected) {
			t.Errorf("help missing %q:\n%s", expected, help)
		}
	}
}

func TestGlobalHelpFlags(t *testing.T) {
	for _, argument := range []string{"--help", "-h"} {
		output := captureStdout(t, func() {
			if err := run([]string{argument}); err != nil {
				t.Fatal(err)
			}
		})
		if output != help {
			t.Fatalf("%s output did not match global help", argument)
		}
	}
}

func TestVersion(t *testing.T) {
	previous := version
	version = "v1.2.3"
	t.Cleanup(func() { version = previous })

	output := captureStdout(t, func() {
		if err := run([]string{"--version"}); err != nil {
			t.Fatal(err)
		}
	})
	if output != "contextctl v1.2.3\n" {
		t.Fatalf("version output = %q", output)
	}
}

func TestTransferProgressNonInteractivePrintsFinalStats(t *testing.T) {
	var output bytes.Buffer
	progress := newTransferProgress(&output)
	progress.Update(contextsync.Progress{
		Direction: "upload", Transferred: 512, Total: 1024, Elapsed: time.Second,
	})
	if output.Len() != 0 {
		t.Fatalf("non-interactive progress printed an intermediate update: %q", output.String())
	}
	progress.Update(contextsync.Progress{
		Direction: "upload", Transferred: 1024, Total: 1024, Elapsed: 2 * time.Second, Done: true,
	})
	for _, expected := range []string{"Uploading", "100%", "1.0 KiB / 1.0 KiB", "512 B/s", "00:02"} {
		if !strings.Contains(output.String(), expected) {
			t.Errorf("progress output missing %q: %q", expected, output.String())
		}
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

func TestLocalClaudeStateWorkflow(t *testing.T) {
	root := t.TempDir()
	contextHome := filepath.Join(root, "contexts")
	claudeHome := filepath.Join(root, "claude")
	sourceProject := filepath.Join(root, "source")
	destinationProject := filepath.Join(root, "destination")
	for _, dir := range []string{sourceProject, destinationProject} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	sourceProject, _ = filepath.EvalSymlinks(sourceProject)
	destinationProject, _ = filepath.EvalSymlinks(destinationProject)
	t.Setenv("CS_CONTEXT_HOME", contextHome)
	t.Setenv("CLAUDE_CONFIG_DIR", claudeHome)

	if err := run([]string{"ctx", "create", "demo", "--type", "state", "--backend", "filesystem"}); err != nil {
		t.Fatal(err)
	}
	sourceHistory := filepath.Join(claudeHome, "projects", strings.ReplaceAll(sourceProject, string(filepath.Separator), "-"))
	if err := os.MkdirAll(filepath.Join(sourceHistory, "memory"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceHistory, "session.jsonl"), []byte(`{"type":"user"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceHistory, "memory", "MEMORY.md"), []byte("remember\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := run([]string{"ctx", "capture", "demo", "--project", sourceProject}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"ctx", "restore", "demo", "--project", destinationProject}); err != nil {
		t.Fatal(err)
	}
	destinationHistory := filepath.Join(claudeHome, "projects", strings.ReplaceAll(destinationProject, string(filepath.Separator), "-"))
	if _, err := os.Stat(filepath.Join(destinationHistory, "session.jsonl")); err != nil {
		t.Fatalf("restored transcript: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destinationHistory, "memory", "MEMORY.md")); err != nil {
		t.Fatalf("restored memory: %v", err)
	}
}

func TestLocalClaudeAttachAndSessionEndHook(t *testing.T) {
	root := t.TempDir()
	contextHome := filepath.Join(root, "contexts")
	claudeHome := filepath.Join(root, "claude")
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CS_CONTEXT_HOME", contextHome)
	t.Setenv("CLAUDE_CONFIG_DIR", claudeHome)
	if err := run([]string{"ctx", "create", "demo", "--type", "state", "--backend", "filesystem"}); err != nil {
		t.Fatal(err)
	}
	manifest, err := localcontext.New(contextHome).Get("demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := contextbackup.SaveConfig(manifest.Path, contextbackup.NewConfig("s3://contexts/demo", time.Minute, time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"ctx", "attach", "demo", "--project", project}); err != nil {
		t.Fatal(err)
	}
	history := filepath.Join(claudeHome, "projects", strings.ReplaceAll(project, string(filepath.Separator), "-"))
	if err := os.MkdirAll(history, 0o700); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(history, "session.jsonl")
	if err := os.WriteFile(transcript, []byte(`{"type":"user","cwd":"`+project+`"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := `{"session_id":"abc123","transcript_path":"` + transcript + `","cwd":"` + project + `","hook_event_name":"SessionEnd","reason":"other"}`
	if err := hookCommand([]string{"claude-capture", "--context", "demo", "--project", project}, strings.NewReader(input)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(contextHome, "demo", "harnesses", "claude", "project", "session.jsonl")); err != nil {
		t.Fatalf("automatic capture: %v", err)
	}
	if triggered, err := contextbackup.TriggerTime(manifest.Path); err != nil || triggered.IsZero() {
		t.Fatalf("backup trigger: time=%v err=%v", triggered, err)
	}
	if err := run([]string{"ctx", "detach", "demo"}); err != nil {
		t.Fatal(err)
	}
}

func TestContextExportImport(t *testing.T) {
	root := t.TempDir()
	sourceHome := filepath.Join(root, "source-contexts")
	destinationHome := filepath.Join(root, "destination-contexts")
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CS_CONTEXT_HOME", sourceHome)
	if err := run([]string{"ctx", "create", "demo", "--type", "state", "--backend", "filesystem"}); err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(session, []byte(`{"message":"portable"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := localcontext.New(sourceHome).CaptureSessionFile("demo", "codex", project, "session-1", session); err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(root, "demo.context")
	if err := run([]string{"ctx", "export", "demo", "--output", bundlePath}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CS_CONTEXT_HOME", destinationHome)
	if err := run([]string{"ctx", "import", bundlePath, "--name", "copy"}); err != nil {
		t.Fatal(err)
	}
	files, err := localcontext.New(destinationHome).SessionFiles("copy", "codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("imported session count = %d, want 1", len(files))
	}
}

func TestContextRevisionsListsCapturedHistory(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "contexts")
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CS_CONTEXT_HOME", home)
	store := localcontext.New(home)
	if _, err := store.Create("demo", "state"); err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(session, []byte("captured\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CaptureSessionFile("demo", "codex", project, "session-1", session); err != nil {
		t.Fatal(err)
	}
	var revisionsErr error
	output := captureStdout(t, func() {
		revisionsErr = revisionsContext(nil, []string{"demo"})
	})
	if revisionsErr != nil {
		t.Fatal(revisionsErr)
	}
	for _, expected := range []string{"REVISIONS (1)", "capture", "codex", "current"} {
		if !strings.Contains(output, expected) {
			t.Errorf("revision output missing %q:\n%s", expected, output)
		}
	}
}

func TestContextSyncPushRejectsTypeMismatchBeforeTransfer(t *testing.T) {
	t.Setenv("CS_CONTEXT_HOME", filepath.Join(t.TempDir(), "contexts"))
	if err := run([]string{"ctx", "create", "local-state", "--type", "state", "--backend", "filesystem"}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/namespaces/serverless-harness/contexts/remote-workspace" {
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"remote-workspace","namespace":"serverless-harness","type":"workspace","status":"ready","storage":{"backend":"pvc","size":"1Gi","accessMode":"ReadWriteOnce","storageClass":"standard"},"attachment":{"kind":"pvc","claimName":"context-remote-workspace"}}`))
	}))
	defer server.Close()

	err := pushContext(client.New(server.URL, "", server.Client()), []string{"local-state", "--remote-name", "remote-workspace"})
	if err == nil || !strings.Contains(err.Error(), "context type mismatch") {
		t.Fatalf("push error = %v, want type mismatch", err)
	}
}

func TestContextListIncludesLocalAndKubernetesContexts(t *testing.T) {
	t.Setenv("CS_CONTEXT_HOME", filepath.Join(t.TempDir(), "contexts"))
	if err := run([]string{"ctx", "create", "local-demo", "--type", "state", "--backend", "filesystem"}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/namespaces/serverless-harness/contexts" {
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"name":"cluster-demo","namespace":"serverless-harness","type":"workspace","status":"ready","storage":{"backend":"pvc","size":"1Gi","accessMode":"ReadWriteOnce","storageClass":"standard"},"attachment":{"kind":"pvc","claimName":"context-cluster-demo"}}]}`))
	}))
	defer server.Close()

	var listErr error
	output := captureStdout(t, func() {
		listErr = listContexts(client.New(server.URL, "", server.Client()), nil)
	})
	if listErr != nil {
		t.Fatal(listErr)
	}
	for _, expected := range []string{
		"CONTEXTS",
		"FILESYSTEM (1)",
		"local-demo  Ready · state",
		"PVC (1)",
		"cluster-demo  Ready · workspace",
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("list output missing %q:\n%s", expected, output)
		}
	}
}

func TestContextListShowsEmptyPVCBackend(t *testing.T) {
	t.Setenv("CS_CONTEXT_HOME", filepath.Join(t.TempDir(), "contexts"))
	if err := run([]string{"ctx", "create", "local-demo", "--type", "state", "--backend", "filesystem"}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer server.Close()

	var listErr error
	output := captureStdout(t, func() {
		listErr = listContexts(client.New(server.URL, "", server.Client()), nil)
	})
	if listErr != nil {
		t.Fatal(listErr)
	}
	for _, expected := range []string{"FILESYSTEM (1)", "PVC (0)", "None"} {
		if !strings.Contains(output, expected) {
			t.Errorf("list output missing %q:\n%s", expected, output)
		}
	}
}

func captureStdout(t *testing.T, action func()) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = writer
	action()
	_ = writer.Close()
	os.Stdout = original
	output, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil {
		t.Fatal(err)
	}
	return string(output)
}

func TestContextCommandRequiresSubcommand(t *testing.T) {
	err := contextCommand(client.New("http://unused", "", nil), nil)
	if err == nil || !strings.Contains(err.Error(), "context command is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResourceAliases(t *testing.T) {
	tests := []struct {
		alias string
		want  string
	}{
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

func TestSandboxPoolCommandIsRemoved(t *testing.T) {
	for _, command := range []string{"sandbox-pool", "sb"} {
		err := run([]string{command, "list"})
		if err == nil || !strings.Contains(err.Error(), "unknown command") {
			t.Fatalf("%s: unexpected error: %v", command, err)
		}
	}
}

func TestTreeViews(t *testing.T) {
	var output bytes.Buffer
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

func TestLoadStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
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
	if view.Namespace != "team1" || len(view.Contexts) != 1 || len(view.StorageClasses) != 1 {
		t.Fatalf("unexpected status: %+v", view)
	}
}
