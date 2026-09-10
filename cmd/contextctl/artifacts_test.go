package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArtifactCLIWorkflow(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CS_CONTEXT_HOME", filepath.Join(root, "contexts"))
	if err := run([]string{"ctx", "create", "workspace", "--type", "workspace", "--backend", "filesystem"}); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(root, "report.txt")
	if err := os.WriteFile(input, []byte("verified output"), 0o600); err != nil {
		t.Fatal(err)
	}
	var publishErr error
	published := captureStdout(t, func() {
		publishErr = run([]string{"ctx", "artifact", "publish", "results", input, "--from", "workspace", "--producer", "agent:demo"})
	})
	if publishErr != nil {
		t.Fatal(publishErr)
	}
	if !strings.Contains(published, "Published 1 artifact") || !strings.Contains(published, "report.txt") {
		t.Fatalf("publish output = %s", published)
	}
	output := filepath.Join(root, "download.txt")
	if err := run([]string{"ctx", "artifact", "get", "results", "report.txt", "--output", output}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(output)
	if string(data) != "verified output" {
		t.Fatalf("retrieved = %q", data)
	}
}
