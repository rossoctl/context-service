package localcontext

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// goldenWorkspaceRevision is the revision writeWorkspaceFixture packed to
// before .claude and .gitignore filtering existed. A tree with nothing to
// filter must keep it.
const goldenWorkspaceRevision = "6776635f7076722ff572bdb01fed998be159d2a6baa66e281b62d0af92185312"

func TestPackRevisionIsUnchangedWithoutFilteredFiles(t *testing.T) {
	result, err := Pack(writeWorkspaceFixture(t), filepath.Join(t.TempDir(), "demo.context"), PackOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Revision != goldenWorkspaceRevision || result.Files != 3 {
		t.Fatalf("pack result = %+v, want revision %s and 3 files", result, goldenWorkspaceRevision)
	}
	if result.Skipped != (PackSkipped{Git: 2}) {
		t.Fatalf("skipped = %+v, want only the two .git entries", result.Skipped)
	}
}

// writeTree creates files under directory; a name ending in / is an empty
// directory.
func writeTree(t *testing.T, directory string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		target := filepath.Join(directory, filepath.FromSlash(name))
		if strings.HasSuffix(name, "/") {
			if err := os.MkdirAll(target, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// packedFiles packs directory and returns the workspace files in the bundle,
// relative to workspace/.
func packedFiles(t *testing.T, directory string, options PackOptions) ([]string, PackResult) {
	t.Helper()
	bundlePath := filepath.Join(t.TempDir(), "packed.context")
	result, err := Pack(directory, bundlePath, options)
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for name, mode := range bundleModes(t, bundlePath) {
		relative, ok := strings.CutPrefix(name, "workspace/")
		if ok && mode != portableDirMode {
			files = append(files, relative)
		}
	}
	sort.Strings(files)
	if len(files) != result.Files {
		t.Fatalf("bundle holds %d files, result reports %d", len(files), result.Files)
	}
	return files, result
}

func assertFiles(t *testing.T, got []string, want ...string) {
	t.Helper()
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("packed files:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func TestPackSkipsClaudeByDefault(t *testing.T) {
	directory := t.TempDir()
	writeTree(t, directory, map[string]string{
		".claude/skills/x/SKILL.md":   "skill",
		".claude/settings.json":       "{}",
		"src/.claude/settings.json":   "{}",
		"docs/.claude":                "a file named .claude",
		"src/app.py":                  "print()",
		"claude/notes.md":             "kept",
		".claudeignore":               "kept",
		"src/.claude-backup/notes.md": "kept",
	})
	files, result := packedFiles(t, directory, PackOptions{})
	assertFiles(t, files, "src/app.py", "claude/notes.md", ".claudeignore", "src/.claude-backup/notes.md")
	if result.Skipped != (PackSkipped{Claude: 3}) {
		t.Fatalf("skipped = %+v", result.Skipped)
	}

	files, result = packedFiles(t, directory, PackOptions{IncludeClaude: true})
	assertFiles(t, files, "src/app.py", "claude/notes.md", ".claudeignore", "src/.claude-backup/notes.md",
		".claude/skills/x/SKILL.md", ".claude/settings.json", "src/.claude/settings.json", "docs/.claude")
	if result.Skipped != (PackSkipped{}) {
		t.Fatalf("skipped = %+v", result.Skipped)
	}
}

// gitignoreFixture exercises the gitignore pattern forms. Each value says
// whether git keeps the file.
func gitignoreFixture() map[string]string {
	return map[string]string{
		".git/HEAD": "ref: refs/heads/main\n",
		".git/info/exclude": "# local excludes\n" +
			"local-only.txt\n",
		".gitignore": "# comment line\n" +
			"\n" +
			"__pycache__/\n" + // directory-only, any depth
			"*.log\n" +
			"!keep.log\n" + // negation
			"/build\n" + // anchored to the root
			"docs/**/draft.md\n" + // ** in the middle
			"**/tmp\n" + // leading **
			"cache/\n" + // directory-only: a file named cache is kept
			"\\#literal.txt\n" + // escaped #
			"\\!bang.txt\n" + // escaped !
			"vendor/**\n", // trailing **
		"src/app.py":                    "print()",
		"src/__pycache__/app.pyc":       "bytecode",
		"__pycache__/top.pyc":           "bytecode",
		"run.log":                       "log",
		"src/deep/run.log":              "log",
		"keep.log":                      "kept by negation",
		"build/out.bin":                 "built",
		"src/build/source.go":           "kept: /build is anchored",
		"docs/draft.md":                 "ignored: ** matches zero directories",
		"docs/a/b/draft.md":             "ignored",
		"docs/final.md":                 "kept",
		"tmp/x":                         "ignored",
		"src/tmp/y":                     "ignored",
		"cache":                         "kept: a file, and cache/ matches only directories",
		"src/cache/blob":                "ignored",
		"#literal.txt":                  "ignored",
		"!bang.txt":                     "ignored",
		"vendor/lib/x.go":               "ignored",
		"local-only.txt":                "ignored by info/exclude",
		"pkg/.gitignore":                "*.tmp\n!important.tmp\n/generated\n",
		"pkg/a.tmp":                     "ignored by the nested file",
		"pkg/important.tmp":             "kept by nested negation",
		"pkg/generated/code.go":         "ignored: anchored to pkg/",
		"generated/code.go":             "kept: the nested rule is relative to pkg/",
		"other/a.tmp":                   "kept: the nested rule does not apply here",
		"pkg/sub/.gitignore":            "!*.log\n",
		"pkg/sub/re-included.log":       "kept: a deeper negation wins",
		"src/__pycache__/nested/x.pyc":  "ignored",
		"pkg/sub/ignored-dir/":          "",
		"pkg/sub/ignored-dir/.gitkeep":  "kept",
		"pkg/sub/excluded/.gitignore":   "*\n",
		"pkg/sub/excluded/anything.txt": "ignored, as is its own .gitignore",
	}
}

func TestPackHonorsGitignore(t *testing.T) {
	directory := t.TempDir()
	writeTree(t, directory, gitignoreFixture())
	files, result := packedFiles(t, directory, PackOptions{})
	assertFiles(t, files,
		".gitignore",
		"src/app.py",
		"keep.log",
		"src/build/source.go",
		"docs/final.md",
		"cache",
		"pkg/.gitignore",
		"pkg/important.tmp",
		"generated/code.go",
		"other/a.tmp",
		"pkg/sub/.gitignore",
		"pkg/sub/re-included.log",
		"pkg/sub/ignored-dir/.gitkeep",
	)
	if result.Skipped.Git != 1 || result.Skipped.Ignored == 0 {
		t.Fatalf("skipped = %+v", result.Skipped)
	}

	// --no-ignore packs everything but .git.
	files, result = packedFiles(t, directory, PackOptions{NoIgnore: true})
	if len(files) != 32 || result.Skipped != (PackSkipped{Git: 1}) {
		t.Fatalf("--no-ignore packed %d files, skipped %+v:\n%s", len(files), result.Skipped, strings.Join(files, "\n"))
	}
	// --no-ignore is independent of --include-git.
	files, _ = packedFiles(t, directory, PackOptions{NoIgnore: true, IncludeGit: true})
	if len(files) != 34 {
		t.Fatalf("--no-ignore --include-git packed %d files", len(files))
	}
	// --include-git alone still honors the ignore rules, and .git contents
	// are never matched against them.
	files, _ = packedFiles(t, directory, PackOptions{IncludeGit: true})
	if len(files) != 15 {
		t.Fatalf("--include-git packed %d files:\n%s", len(files), strings.Join(files, "\n"))
	}
}

func TestPackDoesNotHonorGitignoreOutsideARepository(t *testing.T) {
	directory := t.TempDir()
	writeTree(t, directory, map[string]string{
		".gitignore":        "*.pyc\n",
		"__pycache__/a.pyc": "bytecode",
		"src/app.py":        "print()",
	})
	files, result := packedFiles(t, directory, PackOptions{})
	assertFiles(t, files, ".gitignore", "__pycache__/a.pyc", "src/app.py")
	if result.Skipped != (PackSkipped{}) {
		t.Fatalf("skipped = %+v", result.Skipped)
	}
}

func TestPackIgnoresGlobalExcludesFile(t *testing.T) {
	home := t.TempDir()
	writeTree(t, home, map[string]string{
		".gitconfig":         "[core]\n\texcludesFile = " + filepath.Join(home, "global-ignore") + "\n",
		"global-ignore":      "*.py\n",
		".config/git/ignore": "*.py\n",
	})
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	directory := t.TempDir()
	writeTree(t, directory, map[string]string{
		".git/HEAD":  "ref\n",
		".gitignore": "*.pyc\n",
		"src/app.py": "print()",
		"src/a.pyc":  "bytecode",
	})
	files, _ := packedFiles(t, directory, PackOptions{})
	assertFiles(t, files, ".gitignore", "src/app.py")
}

func TestPackSubdirectoryOfARepository(t *testing.T) {
	repo := t.TempDir()
	writeTree(t, repo, map[string]string{
		".git/HEAD":                   "ref\n",
		".git/info/exclude":           "*.secret\n",
		".gitignore":                  "__pycache__/\n/top-only\nservice/dist/\n",
		"service/.gitignore":          "*.tmp\n",
		"service/app.py":              "print()",
		"service/__pycache__/app.pyc": "bytecode",
		"service/top-only":            "kept: /top-only is anchored to the repository root",
		"service/dist/out":            "ignored by a root rule that names the subdirectory",
		"service/x.tmp":               "ignored by the subdirectory's own file",
		"service/k.secret":            "ignored by info/exclude",
		"service/lib/b.tmp":           "ignored",
		"service/lib/b.py":            "kept",
		"ignored-root/a.py":           "",
	})
	files, result := packedFiles(t, filepath.Join(repo, "service"), PackOptions{})
	assertFiles(t, files, ".gitignore", "app.py", "top-only", "lib/b.py")
	if result.Skipped.Ignored != 5 || result.Skipped.Git != 0 {
		t.Fatalf("skipped = %+v", result.Skipped)
	}

	// A pack root the repository ignores is rejected rather than packed empty.
	writeTree(t, repo, map[string]string{"service/__pycache__/more.pyc": ""})
	_, err := Pack(filepath.Join(repo, "service", "__pycache__"), filepath.Join(t.TempDir(), "x.context"), PackOptions{})
	if err == nil || !strings.Contains(err.Error(), "--no-ignore") {
		t.Fatalf("packing an ignored root: error = %v", err)
	}
	files, _ = packedFiles(t, filepath.Join(repo, "service", "__pycache__"), PackOptions{NoIgnore: true})
	assertFiles(t, files, "app.pyc", "more.pyc")
}

func TestPackWorktreeGitFileUsesCommonInfoExclude(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"main/.git/info/exclude":           "*.secret\n",
		"main/.git/worktrees/wt/commondir": "../..\n",
		"main/.git/worktrees/wt/HEAD":      "ref\n",
		"wt/a.py":                          "",
		"wt/b.secret":                      "",
		"wt/sub/c.secret":                  "",
	})
	if err := os.WriteFile(filepath.Join(root, "wt", ".git"), []byte("gitdir: "+filepath.Join(root, "main", ".git", "worktrees", "wt")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files, result := packedFiles(t, filepath.Join(root, "wt"), PackOptions{})
	assertFiles(t, files, "a.py")
	if result.Skipped != (PackSkipped{Git: 1, Ignored: 2}) {
		t.Fatalf("skipped = %+v", result.Skipped)
	}
}

func TestPackNestedRepositoryStartsANewScope(t *testing.T) {
	directory := t.TempDir()
	writeTree(t, directory, map[string]string{
		".git/HEAD":        "ref\n",
		".gitignore":       "*.out\n/lib/vendored.txt\n",
		"lib/.git":         "gitdir: elsewhere\n",
		"lib/.gitignore":   "*.cache\n",
		"lib/a.out":        "kept: the parent's rules stop at the nested repository",
		"lib/vendored.txt": "kept",
		"lib/b.cache":      "ignored",
		"top.out":          "ignored",
	})
	files, _ := packedFiles(t, directory, PackOptions{})
	assertFiles(t, files, ".gitignore", "lib/.gitignore", "lib/a.out", "lib/vendored.txt")
}

func TestPackIgnoredFilesDoNotCountTowardLimits(t *testing.T) {
	directory := t.TempDir()
	tree := map[string]string{
		".git/HEAD":   "ref\n",
		".gitignore":  "__pycache__/\n*.bin\n",
		"src/app.py":  "print()",
		"big.bin":     strings.Repeat("x", 4096),
		".claude/big": strings.Repeat("x", 4096),
	}
	for i := 0; i < 50; i++ {
		tree[filepath.ToSlash(filepath.Join("__pycache__", "f"+string(rune('a'+i%26))+strings.Repeat("x", i/26)+".pyc"))] = "bytecode"
	}
	writeTree(t, directory, tree)
	// Two files, two directories (workspace/, workspace/src/) and two
	// metadata files.
	limits := PackLimits{MaxEntries: 6, MaxFileBytes: 100, MaxTotalBytes: 100}
	files, _ := packedFiles(t, directory, PackOptions{Limits: limits})
	assertFiles(t, files, ".gitignore", "src/app.py")
	if _, err := Pack(directory, filepath.Join(t.TempDir(), "x.context"), PackOptions{NoIgnore: true, Limits: limits}); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("--no-ignore under the same limits: error = %v", err)
	}
}

func TestPackRevisionDependsOnIgnoreState(t *testing.T) {
	directory := t.TempDir()
	writeTree(t, directory, map[string]string{
		".git/HEAD":  "ref\n",
		".gitignore": "*.pyc\n",
		"src/app.py": "print()",
		"src/a.pyc":  "bytecode",
	})
	_, honored := packedFiles(t, directory, PackOptions{})
	_, everything := packedFiles(t, directory, PackOptions{NoIgnore: true})
	if honored.Revision == everything.Revision {
		t.Fatal("ignoring a file did not change the revision")
	}
	_, again := packedFiles(t, directory, PackOptions{})
	if again.Revision != honored.Revision {
		t.Fatal("packing the same tree twice changed the revision")
	}
}
