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
)

// ignoreScope holds the gitignore rules that apply inside one directory of
// the pack walk. Paths are matched relative to repoRoot, the work tree that
// owns the directory; a nested repository starts a new scope, as it does in
// git.
type ignoreScope struct {
	repoRoot string
	patterns []ignorePattern // in increasing priority
}

// excluded reports whether git ignores filePath. As in git, the last
// pattern that matches the path itself decides; the walk never descends
// into an excluded directory, so a file under one is never asked about.
func (s *ignoreScope) excluded(filePath string, isDir bool) (bool, error) {
	if s == nil || len(s.patterns) == 0 {
		return false, nil
	}
	relative, err := filepath.Rel(s.repoRoot, filePath)
	if err != nil {
		return false, err
	}
	relative = filepath.ToSlash(relative)
	for i := len(s.patterns) - 1; i >= 0; i-- {
		if s.patterns[i].matches(relative, isDir) {
			return !s.patterns[i].negate, nil
		}
	}
	return false, nil
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
		patterns: append(append([]ignorePattern(nil), s.patterns...), patterns...),
	}, nil
}

// domain returns directory relative to the repository root, the base that
// the patterns of its .gitignore are relative to.
func (s *ignoreScope) domain(directory string) string {
	relative, err := filepath.Rel(s.repoRoot, directory)
	if err != nil || relative == "." {
		return ""
	}
	return filepath.ToSlash(relative)
}

// newRepoScope loads .git/info/exclude and the root .gitignore of the work
// tree at repoRoot. The global core.excludesFile is deliberately not read:
// it is machine-dependent and would make revisions differ between machines.
func newRepoScope(repoRoot string) (*ignoreScope, error) {
	scope := &ignoreScope{repoRoot: repoRoot}
	if exclude := infoExcludePath(repoRoot); exclude != "" {
		patterns, err := readGitignore(exclude, "")
		if err != nil {
			return nil, err
		}
		scope.patterns = patterns
	}
	patterns, err := readGitignore(filepath.Join(repoRoot, ".gitignore"), "")
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

// readGitignore parses one ignore file whose patterns are relative to base,
// a directory relative to the repository root ("" for the root). As in git,
// blank lines and lines starting with # are skipped, a trailing CR and
// unescaped trailing spaces are removed, and a missing file has no rules.
func readGitignore(filePath, base string) ([]ignorePattern, error) {
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
	content = bytes.TrimPrefix(content, []byte("\xef\xbb\xbf"))
	var patterns []ignorePattern
	scanner := bufio.NewScanner(bytes.NewReader(content))
	scanner.Buffer(make([]byte, 0, 64*1024), len(content)+1)
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if pattern, ok := parseIgnorePattern(trimTrailingSpaces(line), base); ok {
			patterns = append(patterns, pattern)
		}
	}
	return patterns, scanner.Err()
}

// ignorePattern is one line of an ignore file, parsed as git's
// parse_path_pattern does.
type ignorePattern struct {
	glob     string // matched with wildmatch
	base     string // directory the pattern is relative to, "" for the root
	negate   bool   // leading !
	dirOnly  bool   // trailing /: matches only directories
	basename bool   // no other /: matches the last path component at any depth
}

func parseIgnorePattern(line, base string) (ignorePattern, bool) {
	pattern := ignorePattern{base: base}
	if strings.HasPrefix(line, "!") {
		pattern.negate = true
		line = line[1:]
	}
	if strings.HasSuffix(line, "/") {
		pattern.dirOnly = true
		line = line[:len(line)-1]
	}
	if !strings.Contains(line, "/") {
		pattern.basename = true
	}
	pattern.glob = strings.TrimPrefix(line, "/")
	return pattern, pattern.glob != ""
}

// matches reports whether the pattern matches relative, a slash-separated
// path from the repository root.
func (p ignorePattern) matches(relative string, isDir bool) bool {
	if p.dirOnly && !isDir {
		return false
	}
	if p.base != "" {
		var ok bool
		if relative, ok = strings.CutPrefix(relative, p.base+"/"); !ok {
			return false
		}
	}
	if p.basename {
		relative = relative[strings.LastIndexByte(relative, '/')+1:]
	}
	return wildmatch(p.glob, relative)
}

// trimTrailingSpaces removes trailing spaces that are not escaped with a
// backslash, as git's trim_trailing_spaces does.
func trimTrailingSpaces(line string) string {
	lastSpace := -1
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case ' ':
			if lastSpace < 0 {
				lastSpace = i
			}
		case '\\':
			i++
			if i == len(line) {
				return line
			}
			lastSpace = -1
		default:
			lastSpace = -1
		}
	}
	if lastSpace >= 0 {
		return line[:lastSpace]
	}
	return line
}

// wildmatch reports whether text matches a git glob, following git's
// wildmatch with WM_PATHNAME: *, ? and bracket expressions do not match /;
// ** matches across directories only as a whole path component (leading
// "**/", trailing "/**" or "/**/"), and elsewhere acts like *; [!...] and
// [^...] negate a bracket expression, which may hold ranges and [:class:]
// names; a backslash escapes the next character. Matching is byte-wise and
// case-sensitive.
func wildmatch(pattern, text string) bool {
	for p := 0; p < len(pattern); p++ {
		c := pattern[p]
		if text == "" && c != '*' {
			return false
		}
		switch c {
		case '?':
			if text[0] == '/' {
				return false
			}
		case '[':
			end, matched, ok := matchBracket(pattern, p, text[0])
			if !ok || !matched {
				return false
			}
			p = end
		case '*':
			start := p
			for p+1 < len(pattern) && pattern[p+1] == '*' {
				p++
			}
			rest := pattern[p+1:]
			matchSlash := false
			if p > start && (start == 0 || pattern[start-1] == '/') &&
				(rest == "" || rest[0] == '/' || strings.HasPrefix(rest, `\/`)) {
				// "**/" may also match no directory at all.
				if strings.HasPrefix(rest, "/") && wildmatch(rest[1:], text) {
					return true
				}
				matchSlash = true
			}
			if rest == "" {
				return matchSlash || !strings.Contains(text, "/")
			}
			if !matchSlash && rest[0] == '/' {
				// A single * followed by / matches the rest of
				// this path component.
				slash := strings.IndexByte(text, '/')
				if slash < 0 {
					return false
				}
				return wildmatch(rest, text[slash:])
			}
			for ; text != ""; text = text[1:] {
				if wildmatch(rest, text) {
					return true
				}
				if !matchSlash && text[0] == '/' {
					return false
				}
			}
			return false
		case '\\':
			p++
			if p == len(pattern) || text[0] != pattern[p] {
				return false
			}
		default:
			if text[0] != c {
				return false
			}
		}
		text = text[1:]
	}
	return text == ""
}

// matchBracket matches t against the bracket expression that opens at
// pattern[open]. It returns the index of the closing ], whether t matched,
// and false for ok when the expression is malformed, which git treats as
// no match.
func matchBracket(pattern string, open int, t byte) (end int, matched, ok bool) {
	p := open + 1
	negated := p < len(pattern) && (pattern[p] == '!' || pattern[p] == '^')
	if negated {
		p++
	}
	var previous byte
	// The first character is literal, even ], as in git.
	for first := true; first || (p < len(pattern) && pattern[p] != ']'); first = false {
		if p >= len(pattern) {
			return 0, false, false
		}
		c := pattern[p]
		switch {
		case c == '\\':
			p++
			if p >= len(pattern) {
				return 0, false, false
			}
			c = pattern[p]
			if t == c {
				matched = true
			}
		case c == '-' && previous != 0 && p+1 < len(pattern) && pattern[p+1] != ']':
			p++
			c = pattern[p]
			if c == '\\' {
				p++
				if p >= len(pattern) {
					return 0, false, false
				}
				c = pattern[p]
			}
			if previous <= t && t <= c {
				matched = true
			}
			c = 0
		case c == '[' && p+1 < len(pattern) && pattern[p+1] == ':':
			closing := strings.IndexByte(pattern[p+2:], ']')
			if closing < 0 {
				return 0, false, false
			}
			name := pattern[p+2 : p+2+closing]
			if !strings.HasSuffix(name, ":") {
				// No ":]": the [ is an ordinary member.
				if t == '[' {
					matched = true
				}
				break
			}
			inClass, known := asciiClass(strings.TrimSuffix(name, ":"), t)
			if !known {
				return 0, false, false
			}
			if inClass {
				matched = true
			}
			p += 2 + closing
			c = 0
		default:
			if t == c {
				matched = true
			}
		}
		previous = c
		p++
	}
	if p >= len(pattern) {
		return 0, false, false
	}
	return p, matched != negated && t != '/', true
}

// asciiClass reports whether t is in the named POSIX character class, and
// whether the name is one git knows.
func asciiClass(name string, t byte) (in, known bool) {
	lower := 'a' <= t && t <= 'z'
	upper := 'A' <= t && t <= 'Z'
	digit := '0' <= t && t <= '9'
	graph := '!' <= t && t <= '~'
	switch name {
	case "alnum":
		return lower || upper || digit, true
	case "alpha":
		return lower || upper, true
	case "blank":
		return t == ' ' || t == '\t', true
	case "cntrl":
		return t < ' ' || t == 0x7f, true
	case "digit":
		return digit, true
	case "graph":
		return graph, true
	case "lower":
		return lower, true
	case "print":
		return graph || t == ' ', true
	case "punct":
		return graph && !lower && !upper && !digit, true
	case "space":
		return t == ' ' || ('\t' <= t && t <= '\r'), true
	case "upper":
		return upper, true
	case "xdigit":
		return digit || ('a' <= t && t <= 'f') || ('A' <= t && t <= 'F'), true
	}
	return false, false
}
