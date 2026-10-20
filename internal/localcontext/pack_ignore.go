package localcontext

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
)

// ignoreScope holds the gitignore rules that apply inside one directory of
// the pack walk. Paths are matched relative to repoRoot, the work tree that
// owns the directory; a nested repository starts a new scope, as it does in
// git.
type ignoreScope struct {
	repoRoot string
	patterns []gitignore.Pattern
}

func (s *ignoreScope) excluded(filePath string, isDir bool) (bool, error) {
	if s == nil || len(s.patterns) == 0 {
		return false, nil
	}
	relative, err := filepath.Rel(s.repoRoot, filePath)
	if err != nil {
		return false, err
	}
	return gitignore.NewMatcher(s.patterns).Match(strings.Split(filepath.ToSlash(relative), "/"), isDir), nil
}

// enter returns the scope for directory, a child of the directory that owns
// s. A directory holding .git starts a new repository scope; otherwise its
// .gitignore, if any, is appended to the inherited rules.
func (s *ignoreScope) enter(directory string) (*ignoreScope, error) {
	if s == nil {
		// Outside a git work tree no rules apply, even to nested
		// repositories.
		return nil, nil
	}
	if hasGitEntry(directory) {
		return newRepoScope(directory)
	}
	patterns, err := readGitignore(filepath.Join(directory, ".gitignore"), s.domain(directory))
	if err != nil {
		return nil, err
	}
	if len(patterns) == 0 {
		return s, nil
	}
	return &ignoreScope{
		repoRoot: s.repoRoot,
		patterns: append(append([]gitignore.Pattern(nil), s.patterns...), patterns...),
	}, nil
}

func (s *ignoreScope) domain(directory string) []string {
	relative, err := filepath.Rel(s.repoRoot, directory)
	if err != nil || relative == "." {
		return nil
	}
	return strings.Split(filepath.ToSlash(relative), "/")
}

// newRepoScope loads .git/info/exclude and the root .gitignore of the work
// tree at repoRoot. The global core.excludesFile is deliberately not read:
// it is machine-dependent and would make revisions differ between machines.
func newRepoScope(repoRoot string) (*ignoreScope, error) {
	scope := &ignoreScope{repoRoot: repoRoot}
	if exclude := infoExcludePath(repoRoot); exclude != "" {
		patterns, err := readGitignore(exclude, nil)
		if err != nil {
			return nil, err
		}
		scope.patterns = patterns
	}
	patterns, err := readGitignore(filepath.Join(repoRoot, ".gitignore"), nil)
	if err != nil {
		return nil, err
	}
	scope.patterns = append(scope.patterns, patterns...)
	return scope, nil
}

// packRootScope returns the rules that apply inside root, or nil when root
// is not in a git work tree. When root is a subdirectory of a repository,
// the .gitignore files between the repository root and root apply too. A
// pack root that the repository itself ignores is rejected, so that packing
// it does not silently produce an empty bundle.
func packRootScope(root string) (*ignoreScope, error) {
	repoRoot := ""
	for directory := root; ; {
		if hasGitEntry(directory) {
			repoRoot = directory
			break
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return nil, nil
		}
		directory = parent
	}
	scope, err := newRepoScope(repoRoot)
	if err != nil {
		return nil, err
	}
	relative, err := filepath.Rel(repoRoot, root)
	if err != nil || relative == "." {
		return scope, err
	}
	directory := repoRoot
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		directory = filepath.Join(directory, component)
		excluded, err := scope.excluded(directory, true)
		if err != nil {
			return nil, err
		}
		if excluded {
			return nil, fmt.Errorf("%s is ignored by the .gitignore rules of the repository at %s; use --no-ignore to pack it", root, repoRoot)
		}
		if scope, err = scope.enter(directory); err != nil {
			return nil, err
		}
	}
	return scope, nil
}

func hasGitEntry(directory string) bool {
	_, err := os.Lstat(filepath.Join(directory, ".git"))
	return err == nil
}

// infoExcludePath returns the info/exclude file of the repository at
// repoRoot. A .git file (a worktree or submodule) points at the git
// directory, and a worktree's git directory names the shared common
// directory that holds info/exclude.
func infoExcludePath(repoRoot string) string {
	gitPath := filepath.Join(repoRoot, ".git")
	info, err := os.Lstat(gitPath)
	if err != nil {
		return ""
	}
	gitDir := gitPath
	if !info.IsDir() {
		content, err := os.ReadFile(gitPath)
		if err != nil {
			return ""
		}
		line, _, _ := strings.Cut(string(content), "\n")
		target, ok := strings.CutPrefix(strings.TrimSpace(line), "gitdir:")
		if !ok {
			return ""
		}
		gitDir = resolveFrom(repoRoot, strings.TrimSpace(target))
	}
	if common, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		gitDir = resolveFrom(gitDir, strings.TrimSpace(string(common)))
	}
	return filepath.Join(gitDir, "info", "exclude")
}

func resolveFrom(base, target string) string {
	if filepath.IsAbs(target) {
		return filepath.Clean(target)
	}
	return filepath.Join(base, filepath.FromSlash(target))
}

// readGitignore parses one ignore file. Blank lines and lines starting with
// # are skipped; a missing file has no rules.
func readGitignore(filePath string, domain []string) ([]gitignore.Pattern, error) {
	info, err := os.Lstat(filePath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		// Like git, do not follow a symbolic link or read a directory
		// named .gitignore.
		return nil, nil
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	var patterns []gitignore.Pattern
	scanner := bufio.NewScanner(bytes.NewReader(content))
	scanner.Buffer(make([]byte, 0, 64*1024), len(content)+1)
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, gitignore.ParsePattern(line, domain))
	}
	return patterns, scanner.Err()
}
