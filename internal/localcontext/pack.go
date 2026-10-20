package localcontext

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/rossoctl/context-service/internal/contextresource"
)

// PackLimits bounds a packed bundle. Zero fields use the server's upload
// limits, so a bundle that packs also passes the upload checks.
type PackLimits struct {
	MaxEntries      int
	MaxFileBytes    int64
	MaxTotalBytes   int64
	MaxArchiveBytes int64
}

// PackOptions configures Pack.
type PackOptions struct {
	// Type is the context type; it defaults to and must be "workspace".
	Type string
	// Name is the context name; it defaults to the directory's base name.
	Name string
	// IncludeGit keeps .git entries, which are skipped by default.
	IncludeGit bool
	// IncludeClaude keeps .claude entries, which are skipped by default so
	// agent-side bookkeeping (settings, skills, per-run notes) does not
	// round-trip into the packed workspace.
	IncludeClaude bool
	// NoIgnore packs files that the repository's .gitignore files and
	// .git/info/exclude ignore. By default those rules are honored when the
	// directory is in a git work tree.
	NoIgnore bool
	Limits   PackLimits
}

// PackSkipped counts the entries a pack left out. A skipped directory
// counts once; its contents are not walked.
type PackSkipped struct {
	Git     int `json:"git"`
	Claude  int `json:"claude"`
	Ignored int `json:"ignored"`
}

// PackResult describes a packed bundle. Files, Bytes and Revision describe
// the workspace content and match what the server reports after upload.
type PackResult struct {
	Path     string `json:"path"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Files    int    `json:"files"`
	Bytes    int64  `json:"bytes"`
	Revision string `json:"revision"`
	// Skipped reports what the default filters left out.
	Skipped PackSkipped `json:"skipped"`
}

// Pack writes a .context bundle whose workspace/ root holds the files of an
// ordinary directory. Symbolic links and other non-regular files are
// rejected, owner-executable files keep their executable bit, and the upload
// limits are enforced before the bundle is written.
func Pack(sourceDir, outputPath string, options PackOptions) (PackResult, error) {
	contextType := options.Type
	if contextType == "" {
		contextType = "workspace"
	}
	if contextType != "workspace" {
		return PackResult{}, fmt.Errorf("cannot pack a directory as type %q: only workspace is supported; use ctx export for captured contexts", contextType)
	}
	if outputPath == "" {
		return PackResult{}, errors.New("an output file is required")
	}
	sourceDir, err := filepath.Abs(sourceDir)
	if err != nil {
		return PackResult{}, err
	}
	name := options.Name
	if name == "" {
		name = filepath.Base(sourceDir)
	}
	if err := validateName(name); err != nil {
		return PackResult{}, fmt.Errorf("%w; set a valid name explicitly", err)
	}
	root, err := filepath.EvalSymlinks(sourceDir)
	if err != nil {
		return PackResult{}, err
	}
	if info, err := os.Stat(root); err != nil {
		return PackResult{}, err
	} else if !info.IsDir() {
		return PackResult{}, fmt.Errorf("not a directory: %s", sourceDir)
	}
	limits := options.Limits.withDefaults()
	outputPath, err = newBundlePath(outputPath)
	if err != nil {
		return PackResult{}, err
	}

	// scopes holds the ignore rules of each directory walked so far; a nil
	// scope applies no rules.
	scopes := map[string]*ignoreScope{}
	if !options.NoIgnore {
		if scopes[root], err = packRootScope(root); err != nil {
			return PackResult{}, err
		}
	}
	var skipped PackSkipped
	skip := func(entry fs.DirEntry, count *int) error {
		*count++
		if entry.IsDir() {
			return fs.SkipDir
		}
		return nil
	}

	var files []bundleFile
	var entries []revisionEntry
	var totalBytes int64
	err = filepath.WalkDir(root, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if filePath == root {
			return nil
		}
		// Filters run before any limit is counted, so skipped bulk cannot
		// exceed the upload limits.
		switch entry.Name() {
		case ".git":
			if !options.IncludeGit {
				return skip(entry, &skipped.Git)
			}
		case ".claude":
			if !options.IncludeClaude {
				return skip(entry, &skipped.Claude)
			}
		}
		scope := scopes[filepath.Dir(filePath)]
		if entry.Name() != ".git" {
			excluded, err := scope.excluded(filePath, entry.IsDir())
			if err != nil {
				return err
			}
			if excluded {
				return skip(entry, &skipped.Ignored)
			}
		}
		if entry.IsDir() {
			if options.NoIgnore || entry.Name() == ".git" {
				// git applies no ignore rules inside its own directory.
				return nil
			}
			child, err := scope.enter(filePath)
			if err != nil {
				return err
			}
			scopes[filePath] = child
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("cannot pack non-regular file (symbolic links and special files are not supported): %s", filePath)
		}
		if info.Size() > limits.MaxFileBytes {
			return fmt.Errorf("file %s is %d bytes; the upload limit is %d bytes per file", filePath, info.Size(), limits.MaxFileBytes)
		}
		if totalBytes > limits.MaxTotalBytes-info.Size() {
			return fmt.Errorf("directory content exceeds the %d-byte expanded upload limit", limits.MaxTotalBytes)
		}
		totalBytes += info.Size()
		// Each file is at least one tar entry; the exact count, with
		// directories and metadata, is checked once the walk completes.
		if len(files) >= limits.MaxEntries {
			return fmt.Errorf("directory has more than %d entries, the upload limit", limits.MaxEntries)
		}
		relative, err := filepath.Rel(root, filePath)
		if err != nil {
			return err
		}
		file, err := newBundleFile("workspace/"+filepath.ToSlash(relative), filePath, info)
		if err != nil {
			return err
		}
		files = append(files, file)
		entries = append(entries, revisionEntry{
			path: file.name, checksum: file.checksum, size: file.size, executable: file.executable,
		})
		return nil
	})
	if err != nil {
		return PackResult{}, err
	}
	// manifest.json and checksums.json are tar entries too.
	if count := len(bundleDirectories(files)) + len(files) + 2; count > limits.MaxEntries {
		return PackResult{}, fmt.Errorf("bundle would have %d entries; the upload limit is %d", count, limits.MaxEntries)
	}

	manifest := Manifest{
		Version: manifestVersion, Name: name, Type: contextType, Backend: "filesystem",
		CreatedAt: time.Now().UTC(),
	}
	if _, _, err := writeContextBundle(outputPath, manifest, files, limits.MaxArchiveBytes); err != nil {
		return PackResult{}, err
	}
	revision := revisionDigest(contextType, entries)
	return PackResult{
		Path: outputPath, Name: name, Type: contextType,
		Files: revision.Files, Bytes: revision.Bytes, Revision: revision.Digest,
		Skipped: skipped,
	}, nil
}

func (l PackLimits) withDefaults() PackLimits {
	if l.MaxEntries <= 0 {
		l.MaxEntries = contextresource.UploadMaxEntries
	}
	if l.MaxFileBytes <= 0 {
		l.MaxFileBytes = contextresource.UploadMaxFileBytes
	}
	if l.MaxTotalBytes <= 0 {
		l.MaxTotalBytes = contextresource.UploadMaxExpandedBytes
	}
	if l.MaxArchiveBytes <= 0 {
		l.MaxArchiveBytes = contextresource.UploadMaxArchiveBytes
	}
	return l
}
