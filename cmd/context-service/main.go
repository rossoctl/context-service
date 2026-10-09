package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rossoctl/context-service/internal/api"
	"github.com/rossoctl/context-service/internal/contextresource"
	"github.com/rossoctl/context-service/internal/kube"
	"github.com/rossoctl/context-service/internal/localcontext"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "upload-helper" {
		runUploadHelper()
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "upload-wait" {
		for {
			time.Sleep(time.Hour)
		}
	}
	if len(os.Args) > 1 && os.Args[1] == "export-helper" {
		runExportHelper()
		return
	}
	config, err := kube.LoadConfig()
	if err != nil {
		log.Fatal(err)
	}

	manager, err := kube.NewManager(config)
	if err != nil {
		log.Fatal(err)
	}

	addr := envOr("CS_LISTEN_ADDR", ":8080")
	server := &http.Server{
		Addr: addr,
		Handler: api.NewHandler(manager, api.Options{
			ControlPlaneToken: os.Getenv("CS_CONTROL_PLANE_TOKEN"),
		}),
		ReadHeaderTimeout: api.ReadHeaderTimeout,
	}

	log.Printf("context-service listening on %s (namespace=%s)", addr, config.Namespace)
	log.Fatal(server.ListenAndServe())
}

func runExportHelper() {
	if len(os.Args) != 5 {
		log.Fatal("usage: context-service export-helper ROOT REVISION MAX_ARCHIVE_BYTES")
	}
	maxBytes, err := strconv.ParseInt(os.Args[4], 10, 64)
	if err != nil || maxBytes <= 0 || maxBytes > exportMaxArchiveSize {
		log.Fatal("MAX_ARCHIVE_BYTES must be between 1 and 268435456")
	}
	if err := exportMaterializedRevision(os.Args[2], os.Args[3], maxBytes, os.Stdout); err != nil {
		log.Fatal(err)
	}
}

func runUploadHelper() {
	if len(os.Args) != 6 {
		log.Fatal("usage: context-service upload-helper ROOT UPLOAD_ID CONTEXT_TYPE MAX_EXPANDED_BYTES")
	}
	maxExpandedBytes, err := strconv.ParseInt(os.Args[5], 10, 64)
	if err != nil || maxExpandedBytes <= 0 || maxExpandedBytes > uploadMaxTotalSize {
		log.Fatal("MAX_EXPANDED_BYTES must be between 1 and 1073741824")
	}
	result, err := materializeUploadWithLimit(os.Args[2], os.Args[3], os.Args[4], maxExpandedBytes, os.Stdin)
	if err != nil {
		log.Fatal(err)
	}
	_ = json.NewEncoder(os.Stdout).Encode(result)
}

const (
	uploadMaxFileSize    = 256 << 20
	uploadMaxTotalSize   = 1 << 30
	uploadMaxEntries     = 10_000
	exportMaxArchiveSize = int64(256 << 20)
)

type boundedWriter struct {
	w         io.Writer
	remaining int64
}

func (w *boundedWriter) Write(data []byte) (int, error) {
	if int64(len(data)) > w.remaining {
		return 0, errors.New("context archive exceeds maximum size")
	}
	n, err := w.w.Write(data)
	w.remaining -= int64(n)
	return n, err
}

func exportMaterializedRevision(workspace, revision string, maxBytes int64, output io.Writer) error {
	if matched, _ := regexp.MatchString(`^[0-9a-f]{64}$`, revision); !matched {
		return errors.New("revision must be a lowercase SHA-256 digest")
	}
	root := filepath.Join(workspace, ".context-service", "materialized", revision)
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("materialized revision is unavailable")
	}
	limited := &boundedWriter{w: output, remaining: maxBytes}
	gzipWriter := gzip.NewWriter(limited)
	tarWriter := tar.NewWriter(gzipWriter)
	entries := 0
	checksums := map[string]string{}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.Mode().IsRegular() && !info.IsDir()) {
			return fmt.Errorf("unsupported context entry: %s", path)
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == "." || strings.HasPrefix(relative, "..") {
			return errors.New("invalid context entry path")
		}
		relative = filepath.ToSlash(relative)
		if relative == "checksums.json" {
			return nil
		}
		entries++
		if entries >= uploadMaxEntries {
			return errors.New("context archive has too many entries")
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = relative
		if info.IsDir() {
			header.Mode = 0o755
		} else {
			header.Mode = 0o644
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, copyErr := io.Copy(io.MultiWriter(tarWriter, hash), file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		checksums[relative] = hex.EncodeToString(hash.Sum(nil))
		return nil
	})
	if err != nil {
		return err
	}
	checksumData, err := json.MarshalIndent(struct {
		FormatVersion int               `json:"formatVersion"`
		Algorithm     string            `json:"algorithm"`
		Files         map[string]string `json:"files"`
	}{FormatVersion: 1, Algorithm: "sha256", Files: checksums}, "", "  ")
	if err != nil {
		return err
	}
	checksumData = append(checksumData, '\n')
	if err := tarWriter.WriteHeader(&tar.Header{Name: "checksums.json", Mode: 0o644, Size: int64(len(checksumData)), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	if _, err := tarWriter.Write(checksumData); err != nil {
		return err
	}
	if err := tarWriter.Close(); err != nil {
		return err
	}
	return gzipWriter.Close()
}

func materializeUpload(workspace, uploadID, contextType string, input io.Reader) (contextresource.UploadResult, error) {
	return materializeUploadWithLimit(workspace, uploadID, contextType, uploadMaxTotalSize, input)
}

func materializeUploadWithLimit(workspace, uploadID, contextType string, maxExpandedBytes int64, input io.Reader) (contextresource.UploadResult, error) {
	if maxExpandedBytes <= 0 || maxExpandedBytes > uploadMaxTotalSize {
		return contextresource.UploadResult{}, errors.New("invalid expanded upload limit")
	}
	root := filepath.Join(workspace, ".context-service", "materialized")
	if err := ensureMaterializationDirectory(workspace, filepath.Join(workspace, ".context-service"), root); err != nil {
		return contextresource.UploadResult{}, err
	}
	if err := cleanupUploadStaging(root); err != nil {
		return contextresource.UploadResult{}, err
	}
	store := localcontext.New(root)
	maxFileSize := int64(uploadMaxFileSize)
	if maxExpandedBytes < maxFileSize {
		maxFileSize = maxExpandedBytes
	}
	manifest, err := store.ImportPortableReaderWithLimits(input, "upload-"+uploadID, localcontext.ImportLimits{
		MaxFileSize: maxFileSize, MaxTotalSize: maxExpandedBytes, MaxEntries: uploadMaxEntries,
	})
	if err != nil {
		return contextresource.UploadResult{}, err
	}
	stagingPath := manifest.Path
	removeStaging := true
	defer func() {
		if removeStaging {
			_ = os.RemoveAll(stagingPath)
		}
	}()
	if manifest.Type != contextType {
		return contextresource.UploadResult{}, fmt.Errorf("bundle type %q does not match Context type %q", manifest.Type, contextType)
	}
	revision, err := store.Revision(manifest.Name)
	if err != nil {
		return contextresource.UploadResult{}, err
	}
	destination := filepath.Join(root, revision.Digest)
	if info, err := os.Lstat(destination); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return contextresource.UploadResult{}, errors.New("existing materialized revision is not a directory")
		}
		if err := verifyMaterializedRevision(store, root, revision); err != nil {
			return contextresource.UploadResult{}, err
		}
	} else if !os.IsNotExist(err) {
		return contextresource.UploadResult{}, err
	} else {
		manifest.Name = revision.Digest
		manifest.Path = ""
		data, err := json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			return contextresource.UploadResult{}, err
		}
		data = append(data, '\n')
		if err := os.WriteFile(filepath.Join(stagingPath, "manifest.json"), data, 0o644); err != nil {
			return contextresource.UploadResult{}, err
		}
		if err := os.Rename(stagingPath, destination); err != nil {
			if _, destinationErr := os.Stat(destination); destinationErr != nil {
				return contextresource.UploadResult{}, err
			}
			if verifyErr := verifyMaterializedRevision(store, root, revision); verifyErr != nil {
				return contextresource.UploadResult{}, verifyErr
			}
		} else {
			removeStaging = false
		}
	}
	return contextresource.UploadResult{
		Revision: revision.Digest, Files: revision.Files, Bytes: revision.Bytes,
		WorkspacePath: filepath.ToSlash(filepath.Join(".context-service", "materialized", revision.Digest)),
	}, nil
}

func cleanupUploadStaging(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".context-import-") {
			if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
				return fmt.Errorf("remove stale upload staging directory: %w", err)
			}
		}
	}
	return nil
}

func ensureMaterializationDirectory(workspace string, directories ...string) error {
	workspaceInfo, err := os.Lstat(workspace)
	if err != nil {
		return err
	}
	if workspaceInfo.Mode()&os.ModeSymlink != 0 || !workspaceInfo.IsDir() {
		return errors.New("workspace mount is not a directory")
	}
	for _, directory := range directories {
		info, err := os.Lstat(directory)
		if os.IsNotExist(err) {
			if err := os.Mkdir(directory, 0o755); err != nil && !os.IsExist(err) {
				return err
			}
			info, err = os.Lstat(directory)
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("materialization path is not a directory: %s", directory)
		}
		if err := os.Chmod(directory, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func verifyMaterializedRevision(store *localcontext.Store, root string, expected localcontext.Revision) error {
	actual, err := store.Revision(expected.Digest)
	if err != nil {
		return fmt.Errorf("verify existing materialized revision: %w", err)
	}
	if actual != expected {
		return errors.New("existing materialized revision does not match uploaded content")
	}
	materialized := filepath.Join(root, expected.Digest)
	return filepath.WalkDir(materialized, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("materialized revision contains a symbolic link: %s", path)
		}
		if entry.IsDir() {
			if info.Mode().Perm() != 0o755 {
				return fmt.Errorf("materialized directory has mode %#o", info.Mode().Perm())
			}
			return nil
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o644 {
			return fmt.Errorf("materialized file has invalid mode or type: %s", path)
		}
		return nil
	})
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
