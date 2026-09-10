package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rossoctl/context-service/internal/client"
	"github.com/rossoctl/context-service/internal/contextbackup"
)

func TestBackupAttemptSkipsUnchangedRevisionAndRetriesFailure(t *testing.T) {
	home := filepath.Join(t.TempDir(), "contexts")
	t.Setenv("CS_CONTEXT_HOME", home)
	store := localContextStore()
	manifest, err := store.Create("demo", "state")
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(manifest.Path, "harnesses", "claude", "project")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(stateDir, "session.jsonl")
	if err := os.WriteFile(stateFile, []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	original := backupPush
	defer func() { backupPush = original }()
	pushes := 0
	backupPush = func(context.Context, *client.Client, string, string) error {
		pushes++
		return nil
	}
	config := contextbackup.NewConfig("s3://contexts/demo", time.Minute, time.Second)
	status := backupAttempt(context.Background(), nil, store, "demo", config, contextbackup.Status{})
	if pushes != 1 || status.SourceRevision == "" || status.LastSuccess.IsZero() {
		t.Fatalf("first backup: pushes=%d status=%+v", pushes, status)
	}
	status = backupAttempt(context.Background(), nil, store, "demo", config, status)
	if pushes != 1 {
		t.Fatalf("unchanged context was uploaded again: pushes=%d", pushes)
	}

	if err := os.WriteFile(stateFile, []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	backupPush = func(context.Context, *client.Client, string, string) error {
		pushes++
		return errors.New("temporary failure")
	}
	failed := backupAttempt(context.Background(), nil, store, "demo", config, status)
	if pushes != 2 || failed.ConsecutiveFailures != 1 || failed.LastError == "" || failed.SourceRevision != status.SourceRevision {
		t.Fatalf("failed backup: pushes=%d status=%+v", pushes, failed)
	}
	backupPush = func(context.Context, *client.Client, string, string) error {
		pushes++
		return nil
	}
	retried := backupAttempt(context.Background(), nil, store, "demo", config, failed)
	if pushes != 3 || retried.ConsecutiveFailures != 0 || retried.LastError != "" || retried.SourceRevision == status.SourceRevision {
		t.Fatalf("retried backup: pushes=%d status=%+v", pushes, retried)
	}
}

func TestBackupTargetValidationAndRetryBounds(t *testing.T) {
	for _, target := range []string{"s3://contexts/demo", "pvc://agents/cloud-demo"} {
		if err := validateBackupTarget(target); err != nil {
			t.Errorf("valid target %q: %v", target, err)
		}
	}
	for _, target := range []string{"", "contexts/demo", "pvc://agents", "pvc:///demo"} {
		if err := validateBackupTarget(target); err == nil {
			t.Errorf("accepted invalid target %q", target)
		}
	}
	if got := retryDelay(1, 10*time.Minute); got != 5*time.Second {
		t.Fatalf("first retry = %s", got)
	}
	if got := retryDelay(99, 10*time.Minute); got != time.Minute {
		t.Fatalf("bounded retry = %s", got)
	}
	if got := retryDelay(3, 7*time.Second); got != 7*time.Second {
		t.Fatalf("short interval retry = %s", got)
	}
}
