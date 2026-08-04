package doccheck

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRepositoryLocalMarkdownLinks(t *testing.T) {
	t.Parallel()

	root := repositoryRoot(t)
	issues, err := CheckRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) == 0 {
		return
	}

	for _, issue := range issues {
		t.Errorf("%s:%d: invalid link %q: %s", issue.Source, issue.Line, issue.Destination, issue.Reason)
	}
}

func TestCheckFileReportsOnlyMissingLocalTargets(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	docs := filepath.Join(root, "docs")
	if err := os.MkdirAll(docs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(docs, "guide.md"), []byte("# Guide\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	readme := filepath.Join(root, "README.md")
	content := strings.Join([]string{
		"# Test",
		"",
		"[existing](docs/guide.md)",
		"[external](https://example.com/guide)",
		"[anchor](#test)",
		"[missing](docs/missing.md)",
		"",
		"```md",
		"[fenced example](not-a-real-file.md)",
		"```",
		"",
	}, "\n")
	if err := os.WriteFile(readme, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	issues, err := checkFile(root, readme)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 {
		t.Fatalf("expected one missing link, got %#v", issues)
	}
	if got, want := issues[0].Destination, "docs/missing.md"; got != want {
		t.Fatalf("unexpected missing destination: got %q want %q", got, want)
	}
}

func TestCheckFileRejectsAbsoluteLocalDestinations(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	readme := filepath.Join(root, "README.md")
	content := strings.Join([]string{
		"[unix](/etc/passwd)",
		"[windows](C:/Users/example/file.txt)",
		"[unc](\\\\server/share/file.txt)",
		"[file URL](file:///etc/passwd)",
		"[external](https://example.com/file.txt)",
	}, "\n")
	if err := os.WriteFile(readme, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	issues, err := checkFile(root, readme)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 4 {
		t.Fatalf("expected four absolute local link issues, got %#v", issues)
	}
}

func TestCheckFileRejectsSymlinkTargetOutsideRepository(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(root, "linked.md")
	if err := os.Symlink(outside, linked); err != nil {
		t.Skipf("create symlink: %v", err)
	}
	readme := filepath.Join(root, "README.md")
	if err := os.WriteFile(readme, []byte("[outside](linked.md)\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	issues, err := checkFile(root, readme)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || !strings.Contains(issues[0].Reason, "resolves outside") {
		t.Fatalf("expected repository-escaping symlink issue, got %#v", issues)
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve doccheck source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), "../.."))
}
