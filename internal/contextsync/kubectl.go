package contextsync

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	remoteRoot    = "/context/.context-service"
	remoteObjects = remoteRoot + "/objects"
	remoteCurrent = remoteRoot + "/current"
	uploadPort    = 8080
)

const uploadHandler = `#!/bin/sh
set -eu
umask 077

# Consume HTTP headers one byte at a time so no body bytes are buffered.
while :; do
  line=
  while IFS= read -r -n 1 char; do
    [ "$char" = "$(printf '\r')" ] && break
    line="${line}${char}"
  done
  IFS= read -r -n 1 char || true
  [ -z "$line" ] && break
done

: > "$UPLOAD_PATH"
while :; do
  size=
  while IFS= read -r -n 1 char; do
    [ "$char" = "$(printf '\r')" ] && break
    size="${size}${char}"
  done
  [ -z "$size" ] && break
  IFS= read -r -n 1 char || true
  size=${size%%;*}
  [ "$size" = 0 ] && break
  case "$size" in *[!0-9a-fA-F]*) exit 1;; esac
  count=$((0x$size))
  dd bs=1 count="$count" >> "$UPLOAD_PATH" 2>/dev/null
  IFS= read -r -n 1 char || true
  IFS= read -r -n 1 char || true
done

printf 'HTTP/1.1 200 OK\r\nContent-Length: 3\r\nConnection: close\r\n\r\nok\n'
`

type commandRunner func(context.Context, []string, io.Reader, io.Writer, io.Writer) error

type Progress struct {
	Direction   string
	Transferred int64
	Total       int64
	Elapsed     time.Duration
	Done        bool
}

type Transport interface {
	Push(context.Context, string, string, string) error
	Pull(context.Context, string, string, string) error
}

// Kubectl moves portable bundles to and from a PVC through a short-lived helper
// Pod. It is the first transport implementation, not the long-term service API.
type Kubectl struct {
	executable string
	image      string
	run        commandRunner
	progress   func(Progress)
}

func NewKubectl(executable, image string) *Kubectl {
	transport := &Kubectl{executable: executable, image: image}
	transport.run = transport.runCommand
	return transport
}

func (k *Kubectl) SetProgress(progress func(Progress)) {
	k.progress = progress
}

func (k *Kubectl) Push(ctx context.Context, namespace, claimName, bundlePath string) error {
	input, err := os.Open(bundlePath)
	if err != nil {
		return err
	}
	expected, err := checksum(input)
	closeErr := input.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	info, err := os.Stat(bundlePath)
	if err != nil {
		return err
	}

	return k.withHelper(ctx, namespace, claimName, func(podName string) error {
		incomingPath := remoteIncomingPath(podName)
		currentTempPath := remoteCurrent + "." + podName + ".tmp"
		command := "set -eu; mkdir -p " + remoteObjects
		var stderr bytes.Buffer
		if err := k.run(ctx, []string{"exec", "-n", namespace, podName, "--", "sh", "-c", command}, nil, io.Discard, &stderr); err != nil {
			return commandError("prepare remote context storage", err, stderr.String())
		}
		published := false
		defer func() {
			if !published {
				cleanupContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				_ = k.run(cleanupContext, []string{"exec", "-n", namespace, podName, "--", "rm", "-f", incomingPath, currentTempPath}, nil, io.Discard, io.Discard)
			}
		}()
		upload, err := os.Open(bundlePath)
		if err != nil {
			return err
		}
		defer upload.Close()
		started := time.Now()
		transferred := int64(0)
		k.reportProgress("upload", transferred, info.Size(), started, false)
		reader := &progressReader{reader: upload, update: func(value int64) {
			transferred = value
			k.reportProgress("upload", transferred, info.Size(), started, false)
		}}
		stderr.Reset()
		uploadURL := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s:%d/proxy/upload", namespace, podName, uploadPort)
		if err := k.run(ctx, []string{"create", "--raw=" + uploadURL, "-f", "-"}, reader, io.Discard, &stderr); err != nil {
			k.reportProgress("upload", transferred, info.Size(), started, true)
			return commandError("upload context bundle", err, stderr.String())
		}
		k.reportProgress("upload", transferred, info.Size(), started, true)
		if transferred != info.Size() {
			return fmt.Errorf("upload context bundle: transferred %d bytes, expected %d", transferred, info.Size())
		}
		var output bytes.Buffer
		stderr.Reset()
		if err := k.run(ctx, []string{"exec", "-n", namespace, podName, "--", "sha256sum", incomingPath}, nil, &output, &stderr); err != nil {
			return commandError("verify remote context bundle", err, stderr.String())
		}
		actual := strings.Fields(output.String())
		if len(actual) == 0 {
			return errors.New("remote context bundle checksum returned no digest")
		}
		if actual[0] != expected {
			return fmt.Errorf("remote context bundle checksum mismatch: got %s, want %s", actual[0], expected)
		}
		objectPath := remoteObjectPath(expected)
		command = "set -eu; mv " + incomingPath + " " + objectPath + "; " +
			"printf '%s\\n' " + expected + " > " + currentTempPath + "; mv " + currentTempPath + " " + remoteCurrent
		stderr.Reset()
		if err := k.run(ctx, []string{"exec", "-n", namespace, podName, "--", "sh", "-c", command}, nil, io.Discard, &stderr); err != nil {
			return commandError("publish remote context bundle", err, stderr.String())
		}
		published = true
		return nil
	})
}

func (k *Kubectl) Pull(ctx context.Context, namespace, claimName, outputPath string) (resultErr error) {
	if _, err := os.Lstat(outputPath); err == nil {
		return fmt.Errorf("bundle already exists: %s", outputPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(outputPath), ".context-pull-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		if resultErr != nil {
			_ = os.Remove(temporaryPath)
		}
	}()

	var expectedDigest string
	err = k.withHelper(ctx, namespace, claimName, func(podName string) error {
		var stderr bytes.Buffer
		var current bytes.Buffer
		if err := k.run(ctx, []string{"exec", "-n", namespace, podName, "--", "cat", remoteCurrent}, nil, &current, &stderr); err != nil {
			return commandError("read remote context revision", err, stderr.String())
		}
		digest := strings.TrimSpace(current.String())
		if !validChecksum(digest) {
			return errors.New("remote context revision is invalid")
		}
		expectedDigest = digest
		objectPath := remoteObjectPath(digest)
		var sizeOutput bytes.Buffer
		stderr.Reset()
		sizeCommand := "wc -c < " + objectPath
		if err := k.run(ctx, []string{"exec", "-n", namespace, podName, "--", "sh", "-c", sizeCommand}, nil, &sizeOutput, &stderr); err != nil {
			return commandError("read remote context bundle size", err, stderr.String())
		}
		var total int64
		if _, err := fmt.Fscan(&sizeOutput, &total); err != nil || total < 0 {
			return errors.New("remote context bundle returned an invalid size")
		}
		started := time.Now()
		transferred := int64(0)
		k.reportProgress("download", transferred, total, started, false)
		writer := &progressWriter{writer: temporary, update: func(value int64) {
			transferred = value
			k.reportProgress("download", transferred, total, started, false)
		}}
		stderr.Reset()
		if err := k.run(ctx, []string{"exec", "-n", namespace, podName, "--", "cat", objectPath}, nil, writer, &stderr); err != nil {
			k.reportProgress("download", transferred, total, started, true)
			return commandError("download context bundle", err, stderr.String())
		}
		k.reportProgress("download", transferred, total, started, true)
		if transferred != total {
			return fmt.Errorf("download context bundle: transferred %d bytes, expected %d", transferred, total)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if _, err := temporary.Seek(0, io.SeekStart); err != nil {
		return err
	}
	digest, err := checksum(temporary)
	if err != nil {
		return err
	}
	if _, err := temporary.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	if digest != expectedDigest {
		return errors.New("downloaded context bundle checksum mismatch")
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	// Link installs the completed file without replacing a path created after
	// the initial existence check.
	if err := os.Link(temporaryPath, outputPath); err != nil {
		return err
	}
	_ = os.Remove(temporaryPath)
	return nil
}

func (k *Kubectl) reportProgress(direction string, transferred, total int64, started time.Time, done bool) {
	if k.progress == nil {
		return
	}
	k.progress(Progress{
		Direction: direction, Transferred: transferred, Total: total,
		Elapsed: time.Since(started), Done: done,
	})
}

type progressReader struct {
	reader io.Reader
	read   int64
	update func(int64)
}

func (r *progressReader) Read(buffer []byte) (int, error) {
	n, err := r.reader.Read(buffer)
	r.read += int64(n)
	if n > 0 {
		r.update(r.read)
	}
	return n, err
}

type progressWriter struct {
	writer  io.Writer
	written int64
	update  func(int64)
}

func (w *progressWriter) Write(buffer []byte) (int, error) {
	n, err := w.writer.Write(buffer)
	w.written += int64(n)
	if n > 0 {
		w.update(w.written)
	}
	return n, err
}

func (k *Kubectl) withHelper(ctx context.Context, namespace, claimName string, action func(string) error) error {
	podName, err := helperName()
	if err != nil {
		return err
	}
	manifest, err := helperPodManifest(namespace, podName, claimName, k.image)
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	if err := k.run(ctx, []string{"create", "-f", "-"}, bytes.NewReader(manifest), io.Discard, &stderr); err != nil {
		return commandError("create sync helper", err, stderr.String())
	}
	defer func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = k.run(cleanupContext, []string{"delete", "pod", "-n", namespace, podName, "--ignore-not-found", "--wait=false"}, nil, io.Discard, io.Discard)
	}()
	stderr.Reset()
	if err := k.run(ctx, []string{"wait", "-n", namespace, "--for=condition=Ready", "pod/" + podName, "--timeout=90s"}, nil, io.Discard, &stderr); err != nil {
		return commandError("wait for sync helper", err, stderr.String())
	}
	return action(podName)
}

func (k *Kubectl) runCommand(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	command := exec.CommandContext(ctx, k.executable, args...)
	command.Stdin = stdin
	command.Stdout = stdout
	command.Stderr = stderr
	return command.Run()
}

func helperName() (string, error) {
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return "context-sync-" + hex.EncodeToString(random), nil
}

func helperPodManifest(namespace, podName, claimName, image string) ([]byte, error) {
	uploadScript := base64.StdEncoding.EncodeToString([]byte(uploadHandler))
	command := "set -eu; echo " + uploadScript + " | base64 -d > /tmp/context-upload; " +
		"chmod 700 /tmp/context-upload; " + fmt.Sprintf("exec nc -lk -p %d -e /tmp/context-upload", uploadPort)
	manifest := map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"name": podName, "namespace": namespace,
			"labels": map[string]string{"app.kubernetes.io/managed-by": "contextctl", "context.rossoctl.io/purpose": "sync"},
		},
		"spec": map[string]any{
			"restartPolicy":                 "Never",
			"automountServiceAccountToken":  false,
			"activeDeadlineSeconds":         300,
			"terminationGracePeriodSeconds": 1,
			"containers": []any{map[string]any{
				"name": "sync", "image": image,
				"command": []string{"sh", "-c", command},
				"env":     []any{map[string]any{"name": "UPLOAD_PATH", "value": remoteIncomingPath(podName)}},
				"ports":   []any{map[string]any{"name": "upload", "containerPort": uploadPort}},
				"readinessProbe": map[string]any{
					"exec": map[string]any{"command": []string{"test", "-x", "/tmp/context-upload"}},
				},
				"volumeMounts": []any{map[string]any{"name": "context", "mountPath": "/context"}},
			}},
			"volumes": []any{map[string]any{
				"name": "context", "persistentVolumeClaim": map[string]any{"claimName": claimName},
			}},
		},
	}
	return json.Marshal(manifest)
}

func checksum(reader io.Reader) (string, error) {
	hash := sha256.New()
	if _, err := io.Copy(hash, reader); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func remoteObjectPath(checksum string) string {
	return remoteObjects + "/" + checksum + ".context"
}

func remoteIncomingPath(podName string) string {
	return remoteRoot + "/incoming-" + podName + ".context"
}

func validChecksum(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func commandError(action string, err error, stderr string) error {
	stderr = strings.TrimSpace(stderr)
	if stderr == "" {
		return fmt.Errorf("%s: %w", action, err)
	}
	return fmt.Errorf("%s: %s: %w", action, stderr, err)
}
