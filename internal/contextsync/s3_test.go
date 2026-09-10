package contextsync

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
)

type fakeS3Client struct {
	objects         map[string][]byte
	puts            []string
	failPut         string
	requireSeekable bool
}

func newFakeS3Client() *fakeS3Client {
	return &fakeS3Client{objects: map[string][]byte{}}
}

func (f *fakeS3Client) PutObject(_ context.Context, input *awss3.PutObjectInput, _ ...func(*awss3.Options)) (*awss3.PutObjectOutput, error) {
	key := aws.ToString(input.Key)
	if key == f.failPut {
		return nil, errors.New("injected put failure")
	}
	if f.requireSeekable {
		if _, ok := input.Body.(io.Seeker); !ok {
			return nil, errors.New("upload body is not seekable")
		}
	}
	content, err := io.ReadAll(input.Body)
	if err != nil {
		return nil, err
	}
	f.objects[key] = content
	f.puts = append(f.puts, key)
	return &awss3.PutObjectOutput{}, nil
}

func (f *fakeS3Client) GetObject(_ context.Context, input *awss3.GetObjectInput, _ ...func(*awss3.Options)) (*awss3.GetObjectOutput, error) {
	content, ok := f.objects[aws.ToString(input.Key)]
	if !ok {
		return nil, errors.New("object not found")
	}
	return &awss3.GetObjectOutput{
		Body: io.NopCloser(bytes.NewReader(content)), ContentLength: aws.Int64(int64(len(content))),
	}, nil
}

func TestParseS3Location(t *testing.T) {
	location, err := ParseS3Location("s3://contexts/team/demo/")
	if err != nil {
		t.Fatal(err)
	}
	if location.Bucket != "contexts" || location.Prefix != "team/demo" || location.String() != "s3://contexts/team/demo" {
		t.Fatalf("unexpected location: %+v", location)
	}
	for _, value := range []string{"", "contexts/demo", "s3://", "s3:///demo"} {
		if _, err := ParseS3Location(value); err == nil {
			t.Fatalf("accepted invalid S3 location %q", value)
		}
	}
}

func TestS3PushAndPull(t *testing.T) {
	content := []byte("portable context")
	bundlePath := filepath.Join(t.TempDir(), "demo.context")
	if err := os.WriteFile(bundlePath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	client := newFakeS3Client()
	client.requireSeekable = true
	transport := &S3{client: client}
	var progress []Progress
	transport.SetProgress(func(value Progress) { progress = append(progress, value) })
	location := S3Location{Bucket: "contexts", Prefix: "team/demo"}
	digest, err := transport.Push(context.Background(), location, bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(client.objects[location.currentKey()]); got != digest+"\n" {
		t.Fatalf("current pointer = %q, want %q", got, digest+"\n")
	}
	if got := client.objects[location.objectKey(digest)]; !bytes.Equal(got, content) {
		t.Fatalf("stored object = %q, want %q", got, content)
	}
	output := filepath.Join(t.TempDir(), "pulled.context")
	if err := transport.Pull(context.Background(), location, output); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("pulled object = %q, want %q", got, content)
	}
	if len(progress) == 0 || !progress[len(progress)-1].Done {
		t.Fatalf("missing completed progress: %+v", progress)
	}
}

func TestS3PushDoesNotPublishFailedObject(t *testing.T) {
	bundlePath := filepath.Join(t.TempDir(), "demo.context")
	if err := os.WriteFile(bundlePath, []byte("portable context"), 0o600); err != nil {
		t.Fatal(err)
	}
	location := S3Location{Bucket: "contexts", Prefix: "team/demo"}
	client := newFakeS3Client()
	client.objects[location.currentKey()] = []byte(strings.Repeat("a", 64) + "\n")
	client.failPut = location.currentKey()
	transport := &S3{client: client}
	if _, err := transport.Push(context.Background(), location, bundlePath); err == nil {
		t.Fatal("push succeeded when publishing current failed")
	}
	if got := string(client.objects[location.currentKey()]); got != strings.Repeat("a", 64)+"\n" {
		t.Fatalf("failed push replaced current pointer: %q", got)
	}
}

func TestS3PullRejectsCorruptionAndExistingOutput(t *testing.T) {
	location := S3Location{Bucket: "contexts", Prefix: "team/demo"}
	client := newFakeS3Client()
	digest := strings.Repeat("a", 64)
	client.objects[location.currentKey()] = []byte(digest + "\n")
	client.objects[location.objectKey(digest)] = []byte("corrupt")
	transport := &S3{client: client}
	directory := t.TempDir()
	output := filepath.Join(directory, "pulled.context")
	if err := transport.Pull(context.Background(), location, output); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("Pull error = %v, want checksum mismatch", err)
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed pull left output: %v", err)
	}
	if err := os.WriteFile(output, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := transport.Pull(context.Background(), location, output); err == nil {
		t.Fatal("pull overwrote an existing output")
	}
}
