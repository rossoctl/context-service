package contextsync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPushCreatesHelperTransfersVerifiesAndDeletes(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "demo.context")
	content := []byte("portable context")
	if err := os.WriteFile(bundle, content, 0o600); err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	var uploaded []byte
	var progress []Progress
	transport := NewKubectl("kubectl", "busybox:test")
	transport.SetProgress(func(value Progress) { progress = append(progress, value) })
	transport.run = func(_ context.Context, args []string, stdin io.Reader, stdout, _ io.Writer) error {
		calls = append(calls, append([]string{}, args...))
		if len(args) > 0 && args[0] == "create" && contains(args, "/proxy/upload") {
			uploaded, _ = io.ReadAll(stdin)
		} else if len(args) > 0 && args[0] == "create" {
			manifest, _ := io.ReadAll(stdin)
			for _, expected := range []string{`"namespace":"team1"`, `"claimName":"context-demo"`, `"image":"busybox:test"`, `"automountServiceAccountToken":false`, `"activeDeadlineSeconds":300`, `"containerPort":8080`, `"name":"UPLOAD_PATH"`} {
				if !bytes.Contains(manifest, []byte(expected)) {
					t.Errorf("helper manifest missing %s: %s", expected, manifest)
				}
			}
		}
		if contains(args, "sha256sum") {
			sum := sha256.Sum256(content)
			_, _ = io.WriteString(stdout, hex.EncodeToString(sum[:])+"  incoming.context\n")
		}
		return nil
	}
	if err := transport.Push(context.Background(), "team1", "context-demo", bundle); err != nil {
		t.Fatal(err)
	}
	assertCall(t, calls, "create")
	assertCall(t, calls, "wait")
	assertCall(t, calls, "exec")
	assertCall(t, calls, "delete")
	if !bytes.Equal(uploaded, content) {
		t.Fatalf("uploaded %q, want %q", uploaded, content)
	}
	if len(progress) == 0 || !progress[len(progress)-1].Done || progress[len(progress)-1].Transferred != int64(len(content)) {
		t.Fatalf("unexpected progress: %+v", progress)
	}
}

func TestPullDownloadsAtomically(t *testing.T) {
	content := []byte("portable context")
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	output := filepath.Join(t.TempDir(), "demo.context")
	transport := NewKubectl("kubectl", "busybox:test")
	transport.run = func(_ context.Context, args []string, _ io.Reader, stdout, _ io.Writer) error {
		if contains(args, remoteCurrent) {
			_, _ = io.WriteString(stdout, digest+"\n")
		} else if contains(args, "wc -c") {
			_, _ = fmt.Fprintf(stdout, "%d\n", len(content))
		} else if contains(args, remoteObjectPath(digest)) {
			_, _ = stdout.Write(content)
		}
		return nil
	}
	if err := transport.Pull(context.Background(), "team1", "context-demo", output); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("downloaded %q, want %q", got, content)
	}
	if err := transport.Pull(context.Background(), "team1", "context-demo", output); err == nil {
		t.Fatal("pull overwrote an existing file")
	}
}

func TestPullRefusesToReplaceDanglingSymlink(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "demo.context")
	if err := os.Symlink(filepath.Join(root, "missing"), output); err != nil {
		t.Fatal(err)
	}
	called := false
	transport := NewKubectl("kubectl", "busybox:test")
	transport.run = func(_ context.Context, _ []string, _ io.Reader, _, _ io.Writer) error {
		called = true
		return nil
	}
	if err := transport.Pull(context.Background(), "team1", "context-demo", output); err == nil {
		t.Fatal("pull replaced a dangling symlink")
	}
	if called {
		t.Fatal("pull contacted Kubernetes before rejecting the output path")
	}
}

func TestPullDoesNotOverwriteFileCreatedDuringTransfer(t *testing.T) {
	content := []byte("portable context")
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	output := filepath.Join(t.TempDir(), "demo.context")
	transport := NewKubectl("kubectl", "busybox:test")
	transport.run = func(_ context.Context, args []string, _ io.Reader, stdout, _ io.Writer) error {
		if contains(args, remoteCurrent) {
			_, _ = io.WriteString(stdout, digest+"\n")
		} else if contains(args, "wc -c") {
			_, _ = fmt.Fprintf(stdout, "%d\n", len(content))
		} else if contains(args, remoteObjectPath(digest)) {
			if err := os.WriteFile(output, []byte("keep"), 0o600); err != nil {
				return err
			}
			_, _ = stdout.Write(content)
		}
		return nil
	}
	if err := transport.Pull(context.Background(), "team1", "context-demo", output); err == nil {
		t.Fatal("pull overwrote a file created during transfer")
	}
	got, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "keep" {
		t.Fatalf("output = %q, want existing content", got)
	}
}

func TestPushRejectsChecksumMismatchAndRemovesIncomingFile(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "demo.context")
	if err := os.WriteFile(bundle, []byte("portable context"), 0o600); err != nil {
		t.Fatal(err)
	}
	cleaned := false
	transport := NewKubectl("kubectl", "busybox:test")
	transport.run = func(_ context.Context, args []string, stdin io.Reader, stdout, _ io.Writer) error {
		if len(args) > 0 && args[0] == "create" && contains(args, "/proxy/upload") {
			_, _ = io.Copy(io.Discard, stdin)
		} else if len(args) > 0 && args[0] == "create" {
			_, _ = io.Copy(io.Discard, stdin)
		}
		if contains(args, "sha256sum") {
			_, _ = io.WriteString(stdout, strings.Repeat("0", 64)+"  incoming.context\n")
		}
		if contains(args, "rm") {
			cleaned = true
		}
		return nil
	}
	err := transport.Push(context.Background(), "team1", "context-demo", bundle)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("Push error = %v, want checksum mismatch", err)
	}
	if !cleaned {
		t.Fatal("failed push did not remove its incoming file")
	}
}

func TestPullRejectsChecksumMismatchAndRemovesTemporaryFile(t *testing.T) {
	directory := t.TempDir()
	output := filepath.Join(directory, "demo.context")
	expected := sha256.Sum256([]byte("expected"))
	transport := NewKubectl("kubectl", "busybox:test")
	transport.run = func(_ context.Context, args []string, _ io.Reader, stdout, _ io.Writer) error {
		if contains(args, remoteCurrent) {
			_, _ = io.WriteString(stdout, hex.EncodeToString(expected[:])+"\n")
		} else if contains(args, "wc -c") {
			_, _ = io.WriteString(stdout, "7\n")
		} else if contains(args, remoteObjects) {
			_, _ = io.WriteString(stdout, "corrupt")
		}
		return nil
	}
	if err := transport.Pull(context.Background(), "team1", "context-demo", output); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("Pull error = %v, want checksum mismatch", err)
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed pull left output file: %v", err)
	}
	temporary, err := filepath.Glob(filepath.Join(directory, ".context-pull-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(temporary) != 0 {
		t.Fatalf("failed pull left temporary files: %v", temporary)
	}
}

func TestValidChecksum(t *testing.T) {
	if !validChecksum(strings.Repeat("a", 64)) {
		t.Fatal("valid SHA-256 rejected")
	}
	for _, value := range []string{"", "abc", strings.Repeat("z", 64), strings.Repeat("a", 65)} {
		if validChecksum(value) {
			t.Fatalf("invalid checksum accepted: %q", value)
		}
	}
}

func contains(args []string, value string) bool {
	for _, arg := range args {
		if arg == value || strings.Contains(arg, value) {
			return true
		}
	}
	return false
}

func assertCall(t *testing.T, calls [][]string, command string) {
	t.Helper()
	for _, call := range calls {
		if len(call) > 0 && call[0] == command {
			return
		}
	}
	t.Fatalf("missing kubectl %s call: %#v", command, calls)
}
