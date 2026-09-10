package contextsync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
)

const contextContentType = "application/vnd.rossoctl.context"

type S3Location struct {
	Bucket string
	Prefix string
}

func ParseS3Location(value string) (S3Location, error) {
	if !strings.HasPrefix(value, "s3://") {
		return S3Location{}, fmt.Errorf("invalid S3 location %q: expected s3://bucket/prefix", value)
	}
	remainder := strings.TrimPrefix(value, "s3://")
	bucket, prefix, _ := strings.Cut(remainder, "/")
	if bucket == "" || bucket == "." || bucket == ".." {
		return S3Location{}, fmt.Errorf("invalid S3 location %q: bucket is required", value)
	}
	prefix = strings.Trim(prefix, "/")
	return S3Location{Bucket: bucket, Prefix: prefix}, nil
}

func (l S3Location) String() string {
	if l.Prefix == "" {
		return "s3://" + l.Bucket
	}
	return "s3://" + l.Bucket + "/" + l.Prefix
}

type s3Client interface {
	PutObject(context.Context, *awss3.PutObjectInput, ...func(*awss3.Options)) (*awss3.PutObjectOutput, error)
	GetObject(context.Context, *awss3.GetObjectInput, ...func(*awss3.Options)) (*awss3.GetObjectOutput, error)
}

type S3 struct {
	client   s3Client
	progress func(Progress)
}

func NewS3(ctx context.Context, region, endpoint string) (*S3, error) {
	if region == "" {
		region = "us-east-1"
	}
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("load S3 configuration: %w", err)
	}
	client := awss3.NewFromConfig(cfg, func(options *awss3.Options) {
		if endpoint != "" {
			options.BaseEndpoint = aws.String(endpoint)
			options.UsePathStyle = true
		}
	})
	return &S3{client: client}, nil
}

func (s *S3) SetProgress(progress func(Progress)) {
	s.progress = progress
}

func (s *S3) Push(ctx context.Context, location S3Location, bundlePath string) (string, error) {
	file, err := os.Open(bundlePath)
	if err != nil {
		return "", err
	}
	digest, err := checksum(file)
	if closeErr := file.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	info, err := os.Stat(bundlePath)
	if err != nil {
		return "", err
	}

	file, err = os.Open(bundlePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	started := time.Now()
	transferred := int64(0)
	s.reportProgress("upload", transferred, info.Size(), started, false)
	reader := &progressReadSeeker{reader: file, update: func(value int64) {
		transferred = value
		s.reportProgress("upload", transferred, info.Size(), started, false)
	}}
	objectKey := location.objectKey(digest)
	_, err = s.client.PutObject(ctx, &awss3.PutObjectInput{
		Bucket: aws.String(location.Bucket), Key: aws.String(objectKey), Body: reader,
		ContentLength: aws.Int64(info.Size()), ContentType: aws.String(contextContentType),
		Metadata: map[string]string{"sha256": digest},
	})
	s.reportProgress("upload", transferred, info.Size(), started, true)
	if err != nil {
		return "", fmt.Errorf("upload context bundle to %s: %w", location, err)
	}
	if transferred != info.Size() {
		return "", fmt.Errorf("upload context bundle: transferred %d bytes, expected %d", transferred, info.Size())
	}
	if err := s.verifyObject(ctx, location, objectKey, digest, info.Size()); err != nil {
		return "", err
	}
	pointer := []byte(digest + "\n")
	_, err = s.client.PutObject(ctx, &awss3.PutObjectInput{
		Bucket: aws.String(location.Bucket), Key: aws.String(location.currentKey()),
		Body: bytes.NewReader(pointer), ContentLength: aws.Int64(int64(len(pointer))),
		ContentType: aws.String("text/plain"),
	})
	if err != nil {
		return "", fmt.Errorf("publish current context revision to %s: %w", location, err)
	}
	return digest, nil
}

// progressReadSeeker preserves seek support required by the AWS signer while
// reporting the current read offset. The signer may read and rewind the body
// before sending it, so progress follows the current offset rather than
// accumulating bytes across reads.
type progressReadSeeker struct {
	reader io.ReadSeeker
	offset int64
	update func(int64)
}

func (r *progressReadSeeker) Read(buffer []byte) (int, error) {
	count, err := r.reader.Read(buffer)
	r.offset += int64(count)
	if count > 0 && r.update != nil {
		r.update(r.offset)
	}
	return count, err
}

func (r *progressReadSeeker) Seek(offset int64, whence int) (int64, error) {
	position, err := r.reader.Seek(offset, whence)
	if err != nil {
		return 0, err
	}
	r.offset = position
	if r.update != nil {
		r.update(position)
	}
	return position, nil
}

func (s *S3) Pull(ctx context.Context, location S3Location, outputPath string) (resultErr error) {
	if _, err := os.Lstat(outputPath); err == nil {
		return fmt.Errorf("bundle already exists: %s", outputPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	digest, err := s.currentDigest(ctx, location)
	if err != nil {
		return err
	}
	object, err := s.client.GetObject(ctx, &awss3.GetObjectInput{
		Bucket: aws.String(location.Bucket), Key: aws.String(location.objectKey(digest)),
	})
	if err != nil {
		return fmt.Errorf("download context bundle from %s: %w", location, err)
	}
	defer object.Body.Close()
	temporary, err := os.CreateTemp(filepath.Dir(outputPath), ".context-s3-pull-")
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
	total := aws.ToInt64(object.ContentLength)
	started := time.Now()
	transferred := int64(0)
	s.reportProgress("download", transferred, total, started, false)
	writer := &progressWriter{writer: temporary, update: func(value int64) {
		transferred = value
		s.reportProgress("download", transferred, total, started, false)
	}}
	_, copyErr := io.Copy(writer, object.Body)
	s.reportProgress("download", transferred, total, started, true)
	if copyErr != nil {
		return fmt.Errorf("download context bundle from %s: %w", location, copyErr)
	}
	if total >= 0 && transferred != total {
		return fmt.Errorf("download context bundle: transferred %d bytes, expected %d", transferred, total)
	}
	if _, err := temporary.Seek(0, io.SeekStart); err != nil {
		return err
	}
	actual, err := checksum(temporary)
	if err != nil {
		return err
	}
	if actual != digest {
		return fmt.Errorf("downloaded context bundle checksum mismatch: got %s, want %s", actual, digest)
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Link(temporaryPath, outputPath); err != nil {
		return err
	}
	_ = os.Remove(temporaryPath)
	return nil
}

func (s *S3) verifyObject(ctx context.Context, location S3Location, key, expected string, expectedSize int64) error {
	object, err := s.client.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String(location.Bucket), Key: aws.String(key)})
	if err != nil {
		return fmt.Errorf("verify uploaded context bundle in %s: %w", location, err)
	}
	defer object.Body.Close()
	hash, err := checksum(object.Body)
	if err != nil {
		return fmt.Errorf("verify uploaded context bundle in %s: %w", location, err)
	}
	if size := aws.ToInt64(object.ContentLength); size >= 0 && size != expectedSize {
		return fmt.Errorf("uploaded context bundle size mismatch: got %d, want %d", size, expectedSize)
	}
	if hash != expected {
		return fmt.Errorf("uploaded context bundle checksum mismatch: got %s, want %s", hash, expected)
	}
	return nil
}

func (s *S3) currentDigest(ctx context.Context, location S3Location) (string, error) {
	object, err := s.client.GetObject(ctx, &awss3.GetObjectInput{
		Bucket: aws.String(location.Bucket), Key: aws.String(location.currentKey()),
	})
	if err != nil {
		return "", fmt.Errorf("read current context revision from %s: %w", location, err)
	}
	defer object.Body.Close()
	value, err := io.ReadAll(io.LimitReader(object.Body, 66))
	if err != nil {
		return "", err
	}
	digest := strings.TrimSpace(string(value))
	if !validChecksum(digest) || len(value) > 65 {
		return "", fmt.Errorf("current context revision in %s is invalid", location)
	}
	return digest, nil
}

func (s *S3) reportProgress(direction string, transferred, total int64, started time.Time, done bool) {
	if s.progress == nil {
		return
	}
	s.progress(Progress{
		Direction: direction, Transferred: transferred, Total: total,
		Elapsed: time.Since(started), Done: done,
	})
}

func (l S3Location) rootKey() string {
	return path.Join(l.Prefix, ".context-service")
}

func (l S3Location) objectKey(digest string) string {
	return path.Join(l.rootKey(), "objects", digest+".context")
}

func (l S3Location) currentKey() string {
	return path.Join(l.rootKey(), "current")
}
