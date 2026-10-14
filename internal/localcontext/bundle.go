package localcontext

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
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	bundleFormatVersion = 1
	maxBundleFileSize   = 32 << 30
	maxBundleTotalSize  = 64 << 30
	maxBundleEntries    = 100_000
	portableFileMode    = 0o644
	portableDirMode     = 0o755
	localFileMode       = 0o600
	localDirMode        = 0o700
)

// portableRoots are the context directories carried by revisions, bundles and
// imports. Missing roots are skipped, so adding a root leaves the revisions of
// existing contexts unchanged. A server must know a root before it accepts a
// bundle that contains it: older servers reject workspace/ entries.
var portableRoots = []string{"harnesses", "memory", "knowledge", "artifacts", "workspace"}

// PortableMode is the only mode a bundle stores for an entry: 0755 for a
// directory or an owner-executable regular file, 0644 for any other file.
// Setuid, setgid, sticky and group or world write bits are never carried.
func PortableMode(info fs.FileInfo) os.FileMode {
	if info.IsDir() || isExecutable(info.Mode()) {
		return portableDirMode
	}
	return portableFileMode
}

func isExecutable(mode os.FileMode) bool {
	return mode.IsRegular() && mode.Perm()&0o100 != 0
}

// executableMode adds execute permission wherever fileMode grants read, so
// 0644 becomes 0755 and the local 0600 becomes 0700.
func executableMode(fileMode os.FileMode) os.FileMode {
	return fileMode | (fileMode&0o444)>>2
}

type Bundle struct {
	FormatVersion int    `json:"formatVersion"`
	Name          string `json:"name"`
	Type          string `json:"type"`
	File          string `json:"file"`
	Files         int    `json:"files"`
	Bytes         int64  `json:"bytes"`
}

type Revision struct {
	Digest string
	Files  int
	Bytes  int64
}

type ImportLimits struct {
	MaxFileSize  int64
	MaxTotalSize int64
	MaxEntries   int
}

var defaultImportLimits = ImportLimits{
	MaxFileSize: maxBundleFileSize, MaxTotalSize: maxBundleTotalSize, MaxEntries: maxBundleEntries,
}

type revisionEntry struct {
	path       string
	checksum   string
	size       int64
	executable bool
}

// Revision identifies the portable content of a context. Capture timestamps
// and machine-local attachments are deliberately excluded, so unchanged
// harness files produce the same revision.
func (s *Store) Revision(name string) (Revision, error) {
	manifest, err := s.Get(name)
	if err != nil {
		return Revision{}, err
	}
	unlock, err := acquireCaptureLock(s.contextDir(name), 30*time.Second)
	if err != nil {
		return Revision{}, err
	}
	defer unlock()

	return s.revisionUnlocked(manifest)
}

func (s *Store) revisionUnlocked(manifest Manifest) (Revision, error) {
	var entries []revisionEntry
	contextRoot := s.contextDir(manifest.Name)
	for _, rootName := range portableRoots {
		root := filepath.Join(contextRoot, rootName)
		err := filepath.WalkDir(root, func(filePath string, item fs.DirEntry, walkErr error) error {
			if errors.Is(walkErr, os.ErrNotExist) && filePath == root {
				return fs.SkipDir
			}
			if walkErr != nil {
				return walkErr
			}
			if item.IsDir() {
				return nil
			}
			info, err := item.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("cannot hash non-regular context file: %s", filePath)
			}
			relative, err := filepath.Rel(contextRoot, filePath)
			if err != nil {
				return err
			}
			digest, err := checksumFile(filePath)
			if err != nil {
				return err
			}
			entries = append(entries, revisionEntry{
				path: filepath.ToSlash(relative), checksum: digest, size: info.Size(),
				executable: isExecutable(info.Mode()),
			})
			return nil
		})
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return Revision{}, err
		}
	}
	return revisionDigest(manifest.Type, entries), nil
}

// revisionDigest hashes the type and the sorted file entries. An executable
// file appends an "exec" field after its size. Every entry path starts with a
// portable root and contains a slash, so the field cannot be mistaken for the
// next entry, and a tree without executable files hashes exactly as it did
// before modes were carried.
func revisionDigest(contextType string, entries []revisionEntry) Revision {
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "type\x00%s\x00", contextType)
	var bytes int64
	for _, item := range entries {
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00%d\x00", item.path, item.checksum, item.size)
		if item.executable {
			_, _ = io.WriteString(hash, "exec\x00")
		}
		bytes += item.size
	}
	return Revision{Digest: hex.EncodeToString(hash.Sum(nil)), Files: len(entries), Bytes: bytes}
}

type bundleChecksums struct {
	FormatVersion int               `json:"formatVersion"`
	Algorithm     string            `json:"algorithm"`
	Files         map[string]string `json:"files"`
}

type bundleFile struct {
	name       string
	sourcePath string
	data       []byte
	size       int64
	checksum   string
	executable bool
}

// Export writes a portable, gzip-compressed .context bundle. Attachments are
// deliberately omitted because hooks and project paths are machine-local.
// Native harness state and derived context content are included.
func (s *Store) Export(name, outputPath string) (Bundle, error) {
	manifest, err := s.Get(name)
	if err != nil {
		return Bundle{}, err
	}
	unlock, err := acquireCaptureLock(s.contextDir(name), 30*time.Second)
	if err != nil {
		return Bundle{}, err
	}
	defer unlock()
	return s.exportUnlocked(manifest, outputPath)
}

// exportUnlocked writes a bundle while the caller holds the context capture
// lock. Keeping this separate lets lifecycle operations snapshot exactly the
// revision they publish without dropping and reacquiring the lock.
func (s *Store) exportUnlocked(manifest Manifest, outputPath string) (Bundle, error) {
	name := manifest.Name
	if outputPath == "" {
		outputPath = name + ".context"
	}
	outputPath, err := newBundlePath(outputPath)
	if err != nil {
		return Bundle{}, err
	}

	var files []bundleFile
	for _, rootName := range portableRoots {
		root := filepath.Join(s.contextDir(name), rootName)
		if err := filepath.WalkDir(root, func(filePath string, entry fs.DirEntry, walkErr error) error {
			if errors.Is(walkErr, os.ErrNotExist) && filePath == root {
				return fs.SkipDir
			}
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("cannot export non-regular file: %s", filePath)
			}
			relative, err := filepath.Rel(s.contextDir(name), filePath)
			if err != nil {
				return err
			}
			file, err := newBundleFile(filepath.ToSlash(relative), filePath, info)
			if err != nil {
				return err
			}
			files = append(files, file)
			return nil
		}); err != nil && !errors.Is(err, os.ErrNotExist) {
			return Bundle{}, err
		}
	}

	manifest.Attachments = nil
	manifest.Path = ""
	count, totalBytes, err := writeContextBundle(outputPath, manifest, files, 0)
	if err != nil {
		return Bundle{}, err
	}
	return Bundle{
		FormatVersion: bundleFormatVersion, Name: name, Type: manifest.Type,
		File: outputPath, Files: count, Bytes: totalBytes,
	}, nil
}

// newBundlePath resolves outputPath and refuses to replace anything there,
// including a dangling symlink.
func newBundlePath(outputPath string) (string, error) {
	outputPath, err := filepath.Abs(outputPath)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(outputPath); err == nil {
		return "", fmt.Errorf("bundle already exists: %s", outputPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return outputPath, nil
}

func newBundleFile(name, sourcePath string, info fs.FileInfo) (bundleFile, error) {
	checksum, err := checksumFile(sourcePath)
	if err != nil {
		return bundleFile{}, err
	}
	return bundleFile{
		name: name, sourcePath: sourcePath, size: info.Size(), checksum: checksum,
		executable: isExecutable(info.Mode()),
	}, nil
}

// writeContextBundle adds manifest.json and checksums.json to the content
// files and writes the bundle. It returns the number of files counted in a
// Bundle (content plus manifest) and their total size. A positive
// maxArchiveBytes caps the compressed output.
func writeContextBundle(outputPath string, manifest Manifest, content []bundleFile, maxArchiveBytes int64) (int, int64, error) {
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return 0, 0, err
	}
	manifestData = append(manifestData, '\n')
	files := make([]bundleFile, 0, len(content)+2)
	files = append(files, bundleFile{
		name: "manifest.json", data: manifestData,
		size: int64(len(manifestData)), checksum: checksumBytes(manifestData),
	})
	files = append(files, content...)
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })

	checksums := bundleChecksums{
		FormatVersion: bundleFormatVersion,
		Algorithm:     "sha256",
		Files:         make(map[string]string, len(files)),
	}
	var totalBytes int64
	for _, file := range files {
		checksums.Files[file.name] = file.checksum
		totalBytes += file.size
	}
	checksumData, err := json.MarshalIndent(checksums, "", "  ")
	if err != nil {
		return 0, 0, err
	}
	checksumData = append(checksumData, '\n')
	count := len(files)
	files = append(files, bundleFile{
		name: "checksums.json", data: checksumData, size: int64(len(checksumData)),
	})

	if err := writeBundle(outputPath, files, maxArchiveBytes); err != nil {
		return 0, 0, err
	}
	return count, totalBytes, nil
}

// Import verifies and atomically installs a portable .context bundle. The
// optional name overrides the source context name.
func (s *Store) Import(bundlePath, name string) (Manifest, error) {
	bundlePath, err := filepath.Abs(bundlePath)
	if err != nil {
		return Manifest{}, err
	}
	input, err := os.Open(bundlePath)
	if err != nil {
		return Manifest{}, err
	}
	defer input.Close()
	return s.importReader(input, name, localDirMode, localFileMode, defaultImportLimits)
}

// ImportPortableReaderWithLimits verifies and atomically installs a portable
// bundle read from input under the given limits. Installed content uses
// portable modes, so a non-root consumer can read it.
func (s *Store) ImportPortableReaderWithLimits(input io.Reader, name string, limits ImportLimits) (Manifest, error) {
	if limits.MaxFileSize <= 0 || limits.MaxTotalSize <= 0 || limits.MaxEntries <= 0 {
		return Manifest{}, errors.New("portable import limits must be positive")
	}
	return s.importReader(input, name, portableDirMode, portableFileMode, limits)
}

func (s *Store) importReader(input io.Reader, name string, dirMode, fileMode os.FileMode, limits ImportLimits) (Manifest, error) {
	gzipReader, err := gzip.NewReader(input)
	if err != nil {
		return Manifest{}, fmt.Errorf("read context bundle: %w", err)
	}
	defer gzipReader.Close()

	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return Manifest{}, err
	}
	staging, err := os.MkdirTemp(s.root, ".context-import-")
	if err != nil {
		return Manifest{}, err
	}
	installed := false
	defer func() {
		if !installed {
			_ = os.RemoveAll(staging)
		}
	}()

	computed := map[string]string{}
	executables := map[string]bool{}
	seen := map[string]bool{}
	var totalSize int64
	entries := 0
	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Manifest{}, fmt.Errorf("read context bundle: %w", err)
		}
		entries++
		if entries > limits.MaxEntries {
			return Manifest{}, errors.New("context bundle contains too many entries")
		}
		archivePath, err := safeBundlePath(header.Name)
		if err != nil {
			return Manifest{}, err
		}
		if seen[archivePath] {
			return Manifest{}, fmt.Errorf("duplicate bundle entry: %s", archivePath)
		}
		seen[archivePath] = true
		destination := filepath.Join(staging, filepath.FromSlash(archivePath))
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(destination, dirMode); err != nil {
				return Manifest{}, err
			}
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 || header.Size > limits.MaxFileSize {
				return Manifest{}, fmt.Errorf("bundle entry %s has invalid size %d", archivePath, header.Size)
			}
			if totalSize > limits.MaxTotalSize-header.Size {
				return Manifest{}, errors.New("context bundle is too large")
			}
			totalSize += header.Size
			if err := os.MkdirAll(filepath.Dir(destination), dirMode); err != nil {
				return Manifest{}, err
			}
			output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, fileMode)
			if err != nil {
				return Manifest{}, err
			}
			hash := sha256.New()
			written, copyErr := io.Copy(io.MultiWriter(output, hash), io.LimitReader(tarReader, header.Size+1))
			closeErr := output.Close()
			if copyErr != nil {
				return Manifest{}, copyErr
			}
			if closeErr != nil {
				return Manifest{}, closeErr
			}
			if written != header.Size {
				return Manifest{}, fmt.Errorf("bundle entry %s is truncated", archivePath)
			}
			if archivePath != "checksums.json" {
				computed[archivePath] = hex.EncodeToString(hash.Sum(nil))
			}
			if archivePath != "checksums.json" && archivePath != "manifest.json" && header.Mode&0o100 != 0 {
				executables[destination] = true
			}
		default:
			return Manifest{}, fmt.Errorf("unsupported bundle entry type for %s", archivePath)
		}
	}

	if err := verifyBundle(staging, computed); err != nil {
		return Manifest{}, err
	}
	manifestData, err := os.ReadFile(filepath.Join(staging, "manifest.json"))
	if err != nil {
		return Manifest{}, errors.New("context bundle is missing manifest.json")
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("read context bundle manifest: %w", err)
	}
	if manifest.Version != manifestVersion || manifest.Backend != "filesystem" {
		return Manifest{}, errors.New("unsupported context bundle manifest")
	}
	if strings.TrimSpace(manifest.Type) == "" {
		return Manifest{}, errors.New("context bundle manifest has no type")
	}
	if name == "" {
		name = manifest.Name
	}
	if err := validateName(name); err != nil {
		return Manifest{}, err
	}
	manifest.Name = name
	manifest.Attachments = map[string]Attachment{}
	manifest.Path = ""

	destination := s.contextDir(name)
	if _, err := os.Lstat(destination); err == nil {
		return Manifest{}, ErrAlreadyExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return Manifest{}, err
	}
	if err := s.writeManifestAt(staging, manifest); err != nil {
		return Manifest{}, err
	}
	if err := os.Remove(filepath.Join(staging, "checksums.json")); err != nil {
		return Manifest{}, err
	}
	if err := applyImportModes(staging, dirMode, fileMode, executables); err != nil {
		return Manifest{}, err
	}
	if err := os.Rename(staging, destination); err != nil {
		return Manifest{}, err
	}
	installed = true
	manifest.Path = destination
	return manifest, nil
}

// applyImportModes sets every directory to dirMode and every file to
// fileMode, or to its executable form for a file the bundle marked
// owner-executable. Other mode bits in the bundle are ignored.
func applyImportModes(root string, dirMode, fileMode os.FileMode, executables map[string]bool) error {
	return filepath.WalkDir(root, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		mode := fileMode
		if entry.IsDir() {
			mode = dirMode
		} else if executables[filePath] {
			mode = executableMode(fileMode)
		}
		return os.Chmod(filePath, mode)
	})
}

// archiveLimitWriter fails once more than limit bytes are written, so an
// oversized bundle stops early instead of being written in full.
type archiveLimitWriter struct {
	w         io.Writer
	limit     int64
	remaining int64
}

func (w *archiveLimitWriter) Write(data []byte) (int, error) {
	if int64(len(data)) > w.remaining {
		return 0, fmt.Errorf("context bundle exceeds the %d-byte compressed upload limit", w.limit)
	}
	n, err := w.w.Write(data)
	w.remaining -= int64(n)
	return n, err
}

func writeBundle(outputPath string, files []bundleFile, maxArchiveBytes int64) (resultErr error) {
	temporary, err := os.CreateTemp(filepath.Dir(outputPath), ".context-export-")
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
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	var archive io.Writer = temporary
	if maxArchiveBytes > 0 {
		archive = &archiveLimitWriter{w: temporary, limit: maxArchiveBytes, remaining: maxArchiveBytes}
	}
	gzipWriter := gzip.NewWriter(archive)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, directory := range bundleDirectories(files) {
		header := &tar.Header{Name: directory, Mode: portableDirMode, Typeflag: tar.TypeDir}
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
	}
	for _, file := range files {
		mode := int64(portableFileMode)
		if file.executable {
			mode = portableDirMode
		}
		header := &tar.Header{Name: file.name, Mode: mode, Size: file.size, Typeflag: tar.TypeReg}
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		if file.data != nil {
			if _, err := tarWriter.Write(file.data); err != nil {
				return err
			}
			continue
		}
		input, err := os.Open(file.sourcePath)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tarWriter, input)
		closeErr := input.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if err := tarWriter.Close(); err != nil {
		return err
	}
	if err := gzipWriter.Close(); err != nil {
		return err
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

func bundleDirectories(files []bundleFile) []string {
	directories := map[string]struct{}{}
	for _, file := range files {
		for directory := path.Dir(file.name); directory != "."; directory = path.Dir(directory) {
			directories[directory] = struct{}{}
		}
	}
	result := make([]string, 0, len(directories))
	for directory := range directories {
		result = append(result, directory)
	}
	sort.Slice(result, func(i, j int) bool {
		leftDepth := strings.Count(result[i], "/")
		rightDepth := strings.Count(result[j], "/")
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return result[i] < result[j]
	})
	return result
}

func verifyBundle(staging string, computed map[string]string) error {
	data, err := os.ReadFile(filepath.Join(staging, "checksums.json"))
	if err != nil {
		return errors.New("context bundle is missing checksums.json")
	}
	var checksums bundleChecksums
	if err := json.Unmarshal(data, &checksums); err != nil {
		return fmt.Errorf("read context bundle checksums: %w", err)
	}
	if checksums.FormatVersion != bundleFormatVersion || checksums.Algorithm != "sha256" {
		return errors.New("unsupported context bundle format")
	}
	if len(checksums.Files) != len(computed) {
		return errors.New("context bundle file inventory does not match checksums")
	}
	for name, actual := range computed {
		expected, ok := checksums.Files[name]
		if !ok || actual != expected {
			return fmt.Errorf("context bundle checksum mismatch: %s", name)
		}
	}
	return nil
}

func safeBundlePath(value string) (string, error) {
	cleaned := path.Clean(value)
	if value == "" || value != cleaned || strings.Contains(value, `\`) || strings.HasPrefix(value, "/") || value == "." || value == ".." || strings.HasPrefix(value, "../") {
		return "", fmt.Errorf("unsafe context bundle path: %q", value)
	}
	allowed := value == "manifest.json" || value == "checksums.json"
	for _, root := range portableRoots {
		if value == root || strings.HasPrefix(value, root+"/") {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", fmt.Errorf("unexpected context bundle path: %q", value)
	}
	return value, nil
}

func checksumBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func checksumFile(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (s *Store) writeManifestAt(directory string, manifest Manifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(directory, "manifest.json"), data, 0o600)
}
