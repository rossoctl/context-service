package localcontext

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// differentialTree is the file tree every differential case is packed
// from; the cases differ only in their ignore files.
var differentialTree = []string{
	"a.go", "b.txt", "1.txt", "az", "bz", "dz", "-z", "]z", "x.log", "keep.log",
	"cache", "#literal.txt", "!bang.txt", "trailing ", "a b", "UPPER.TXT", "a?c", "abc", "a*c",
	"build/out.bin", "src/build/source.go", "src/a.go", "src/lib/b.txt", "src/lib/deep/c.go",
	"src/__pycache__/m.pyc", "__pycache__/top.pyc", "src/cache/blob", "docs/draft.md",
	"docs/a/b/draft.md", "docs/final.md", "tmp/x", "src/tmp/y", "vendor/lib/x.go",
	"other/d.txt", "other/a.tmp", "sub/c.go", "sub/d.txt", "sub/deep/e.txt", "sub/deep/f.go",
	"pkg/a.tmp", "pkg/important.tmp", "pkg/generated/code.go", "generated/code.go",
	"pkg/sub/re-included.log", "foo/bar/baz", "foo/barbaz", "foobar/x", "x/foo/y",
	"logs/2026/app.log", "logs/keep", "a/b/c/d.md", "a/x.md",
}

type differentialCase struct {
	name    string
	exclude string            // .git/info/exclude
	ignores map[string]string // .gitignore files by directory ("" for the root)
}

var differentialCases = []differentialCase{
	{name: "negation", ignores: map[string]string{"": "*.log\n!keep.log\n*.txt\n!/b.txt\n"}},
	{name: "dir-only", ignores: map[string]string{"": "__pycache__/\ncache/\nbuild/\n"}},
	{name: "anchored", ignores: map[string]string{"": "/build\n/src/lib\nfoo/bar\n"}},
	{name: "double-star", ignores: map[string]string{"": "docs/**/draft.md\n**/tmp\nvendor/**\na/**/*.md\n**/deep/**\n"}},
	{name: "double-star-not-component", ignores: map[string]string{"": "foo**\n**bar\nsrc/**.go\n"}},
	{name: "single-star-slash", ignores: map[string]string{"": "*/deep\nfoo/*\nlogs/*/\n"}},
	{name: "escapes", ignores: map[string]string{"": "\\#literal.txt\n\\!bang.txt\ntrailing\\ \na\\?c\na\\*c\n"}},
	{name: "trailing-spaces", ignores: map[string]string{"": "b.txt   \nx.log\t\n"}},
	{name: "comments-and-blank", ignores: map[string]string{"": "# a.go\n\n   \n#b.txt\nabc\n"}},
	{name: "question-mark", ignores: map[string]string{"": "a?c\n?z\nfoo/ba?\n"}},
	{name: "classes", ignores: map[string]string{"": "[a-b]z\n[[:digit:]].txt\n[[:upper:]]*\n[]]z\n[-]z\n"}},
	{name: "negated-classes", ignores: map[string]string{"": "[!a-c]z\n[!0-9]*.txt\n"}},
	{name: "caret-classes", ignores: map[string]string{"": "[^a-c]z\n[^[:alpha:]]*.txt\n"}},
	{name: "class-edges", ignores: map[string]string{"": "[!]]z\n[a-]z\n[\\]]z\n[!\\-]z\n"}},
	{name: "whitelist-go", ignores: map[string]string{"": "*\n!*/\n!*.go\n"}},
	{name: "whitelist-src", ignores: map[string]string{"": "*\n!*/\n!/src/**\n"}},
	{name: "excluded-parent-wins", ignores: map[string]string{"": "sub/\n!sub/c.go\n!sub/deep/\n"}},
	{name: "dir-content-re-include", ignores: map[string]string{"": "/sub/*\n!/sub/deep\n"}},
	{name: "nested", ignores: map[string]string{
		"":        "*.log\n",
		"pkg":     "*.tmp\n!important.tmp\n/generated\n",
		"pkg/sub": "!*.log\n",
		"sub":     "*\n",
	}},
	{name: "info-exclude", exclude: "# local\n*.txt\n!b.txt\n", ignores: map[string]string{"": "!1.txt\n"}},
}

// TestPackMatchesGitLsFiles packs each case and compares the file list with
// git ls-files --others --exclude-standard in a real repository, with the
// user's and system git configuration out of the way.
func TestPackMatchesGitLsFiles(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not on PATH")
	}
	empty := t.TempDir()
	env := append(os.Environ(), "HOME="+empty, "XDG_CONFIG_HOME="+empty, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+filepath.Join(empty, "none"))
	git := func(t *testing.T, directory string, args ...string) string {
		t.Helper()
		command := exec.Command(gitPath, args...)
		command.Dir = directory
		command.Env = env
		output, err := command.Output()
		if err != nil {
			t.Fatalf("git %s: %v", strings.Join(args, " "), err)
		}
		return string(output)
	}
	for _, testCase := range differentialCases {
		t.Run(testCase.name, func(t *testing.T) {
			directory := t.TempDir()
			git(t, directory, "init", "-q")
			// git init turns core.ignorecase on for a case-insensitive
			// file system; pack, like git on Linux, is case-sensitive.
			git(t, directory, "config", "core.ignorecase", "false")
			tree := map[string]string{}
			for _, name := range differentialTree {
				tree[name] = name
			}
			for dir, content := range testCase.ignores {
				tree[filepath.ToSlash(filepath.Join(dir, ".gitignore"))] = content
			}
			if testCase.exclude != "" {
				tree[".git/info/exclude"] = testCase.exclude
			}
			writeTree(t, directory, tree)

			want := strings.Split(strings.TrimSuffix(git(t, directory, "-c", "core.quotePath=false", "ls-files", "-z", "--others", "--exclude-standard"), "\x00"), "\x00")
			if len(want) == 1 && want[0] == "" {
				want = nil
			}
			sort.Strings(want)
			got, _ := packedFiles(t, directory, PackOptions{})
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Fatalf("pack and git disagree\npack:\n  %s\ngit:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
			}

			// Packing a subdirectory applies the same rules, and an
			// ignored one is rejected.
			check := exec.Command(gitPath, "check-ignore", "-q", "src")
			check.Dir, check.Env = directory, env
			srcIgnored := check.Run() == nil
			var wantSub []string
			for _, name := range want {
				if relative, ok := strings.CutPrefix(name, "src/"); ok {
					wantSub = append(wantSub, relative)
				}
			}
			if srcIgnored {
				_, err := Pack(filepath.Join(directory, "src"), filepath.Join(t.TempDir(), "x.context"), PackOptions{})
				if err == nil || !strings.Contains(err.Error(), "--no-ignore") {
					t.Fatalf("packing ignored src/: error = %v", err)
				}
				return
			}
			gotSub, _ := packedFiles(t, filepath.Join(directory, "src"), PackOptions{})
			if strings.Join(gotSub, "\n") != strings.Join(wantSub, "\n") {
				t.Fatalf("packing src/: pack and git disagree\npack:\n  %s\ngit:\n  %s", strings.Join(gotSub, "\n  "), strings.Join(wantSub, "\n  "))
			}
		})
	}
}
