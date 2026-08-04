package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/compatibility"
	"github.com/r314tive/pgdrill/internal/doccheck"
)

func TestParseTargets(t *testing.T) {
	targets, err := ParseTargets("linux/amd64, darwin/arm64")
	if err != nil {
		t.Fatalf("parse targets: %v", err)
	}
	want := []Target{{OS: "linux", Arch: "amd64"}, {OS: "darwin", Arch: "arm64"}}
	if !reflect.DeepEqual(targets, want) {
		t.Fatalf("unexpected targets: got %#v want %#v", targets, want)
	}
}

func TestParseTargetsRejectsUnsupportedAndDuplicateTargets(t *testing.T) {
	for _, value := range []string{"windows/amd64", "linux/386", "linux-amd64", "linux/amd64,linux/amd64"} {
		t.Run(value, func(t *testing.T) {
			if _, err := ParseTargets(value); err == nil {
				t.Fatalf("expected %q to fail", value)
			}
		})
	}
}

func TestWriteTarGzIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	modTime := time.Date(2026, 7, 20, 8, 0, 0, 0, time.UTC)
	entries := []archiveEntry{
		{Name: "pgdrill/pgdrill", Mode: 0o755, Body: []byte("binary")},
		{Name: "pgdrill/LICENSE", Mode: 0o644, Body: []byte("license")},
	}
	first := filepath.Join(dir, "first.tar.gz")
	second := filepath.Join(dir, "second.tar.gz")
	if err := writeTarGz(first, modTime, entries); err != nil {
		t.Fatalf("write first archive: %v", err)
	}
	if err := writeTarGz(second, modTime, entries); err != nil {
		t.Fatalf("write second archive: %v", err)
	}
	firstData, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	secondData, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstData, secondData) {
		t.Fatal("expected byte-for-byte deterministic archives")
	}

	contents := readArchive(t, first)
	if got, want := contents, map[string]string{"pgdrill/LICENSE": "license", "pgdrill/pgdrill": "binary"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected archive contents: got %#v want %#v", got, want)
	}
}

func TestWriteChecksums(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checksums.txt")
	artifacts := []Artifact{
		{Name: "a.tar.gz", SHA256: strings.Repeat("a", 64)},
		{Name: "b.tar.gz", SHA256: strings.Repeat("b", 64)},
	}
	if err := writeChecksums(path, artifacts); err != nil {
		t.Fatalf("write checksums: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Repeat("a", 64) + "  a.tar.gz\n" + strings.Repeat("b", 64) + "  b.tar.gz\n"
	if string(data) != want {
		t.Fatalf("unexpected checksums:\n%s", data)
	}
}

func TestBuildEnvironmentNormalizesReleaseInputs(t *testing.T) {
	current := []string{
		"PATH=/usr/bin",
		"GOTOOLCHAIN=go1.26.5",
		"GOOS=windows",
		"GOARCH=386",
		"CGO_ENABLED=1",
		"GOFLAGS=-race",
		"GOEXPERIMENT=arenas",
		"GOENV=/tmp/goenv",
		"GOWORK=/tmp/go.work",
		"GOAMD64=v3",
		"GOARM64=v9.0",
	}
	env := buildEnvironment(current, Target{OS: "linux", Arch: "amd64"})
	for _, expected := range []string{
		"PATH=/usr/bin",
		"GOTOOLCHAIN=go1.26.5",
		"GOOS=linux",
		"GOARCH=amd64",
		"CGO_ENABLED=0",
		"GOFLAGS=",
		"GOEXPERIMENT=",
		"GOENV=off",
		"GOWORK=off",
		"GOAMD64=v1",
	} {
		if countString(env, expected) != 1 {
			t.Fatalf("expected normalized environment entry %q exactly once in %#v", expected, env)
		}
	}
	for _, forbidden := range []string{"GOOS=windows", "GOARCH=386", "CGO_ENABLED=1", "GOFLAGS=-race", "GOAMD64=v3", "GOARM64=v9.0"} {
		if countString(env, forbidden) != 0 {
			t.Fatalf("unexpected inherited environment entry %q in %#v", forbidden, env)
		}
	}
}

func TestReleaseLDFlagsOmitBuildID(t *testing.T) {
	releaseTime := time.Date(2026, 7, 20, 17, 0, 0, 0, time.UTC)
	commit := strings.Repeat("a", 40)
	flags := releaseLDFlags(Options{Version: "v0.1.0-alpha.9", Commit: commit}, releaseTime)

	for _, expected := range []string{
		"-s",
		"-w",
		"-buildid=",
		versionPackage + ".Version=v0.1.0-alpha.9",
		versionPackage + ".Commit=" + commit,
		versionPackage + ".Date=2026-07-20T17:00:00Z",
	} {
		if !strings.Contains(flags, expected) {
			t.Fatalf("release linker flags %q do not contain %q", flags, expected)
		}
	}
}

func TestValidateOptionsRequiresFullGitObjectID(t *testing.T) {
	base := Options{
		Version: "v0.1.0-alpha.9",
		Date:    "2026-07-20T17:00:00Z",
		Targets: []Target{{OS: "linux", Arch: "amd64"}},
	}
	for _, commit := range []string{strings.Repeat("a", 40), strings.Repeat("b", 64)} {
		opts := base
		opts.Commit = commit
		if _, _, err := validateOptions(opts); err != nil {
			t.Fatalf("full commit %q rejected: %v", commit, err)
		}
	}

	for _, commit := range []string{"859af58", strings.Repeat("z", 40)} {
		opts := base
		opts.Commit = commit
		if _, _, err := validateOptions(opts); err == nil || !strings.Contains(err.Error(), "full 40- or 64-character") {
			t.Fatalf("commit %q should be rejected, got %v", commit, err)
		}
	}
}

func TestReleaseSupportFilesAreSelfContained(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	entries, err := loadReleaseSupportFiles(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"README.md",
		"CHANGELOG.md",
		"compatibility/matrix.yaml",
		"demo/README.md",
		"demo/yandex-cloud/USER-RUNBOOK.md",
		"docs/README.md",
		"docs/getting-started.md",
		"docs/operator-guide.md",
		"examples/fleet.yaml",
		"internal/adapters/barman/adapter_test.go",
		"internal/adapters/barman/testdata/list-backups.json",
		"internal/targets/local/target_test.go",
	}
	names := make(map[string]struct{}, len(entries))
	extracted := t.TempDir()
	for _, entry := range entries {
		if _, exists := names[entry.Name]; exists {
			t.Fatalf("duplicate release support entry %q", entry.Name)
		}
		names[entry.Name] = struct{}{}
	}
	if err := materializeReleaseSupportFiles(extracted, entries); err != nil {
		t.Fatal(err)
	}
	for _, name := range want {
		if _, ok := names[name]; !ok {
			t.Errorf("release support files do not contain %q", name)
		}
	}
	for name := range names {
		if strings.Contains(name, "/.state/") || strings.Contains(name, "/.terraform/") ||
			strings.HasSuffix(name, ".tfvars") || strings.HasSuffix(name, ".plan") {
			t.Errorf("release support files contain local artifact %q", name)
		}
	}

	issues, err := doccheck.CheckRepository(extracted)
	if err != nil {
		t.Fatal(err)
	}
	for _, issue := range issues {
		t.Errorf("%s:%d: invalid packaged link %q: %s", issue.Source, issue.Line, issue.Destination, issue.Reason)
	}

	matrixPayload, err := os.ReadFile(filepath.Join(extracted, "compatibility", "matrix.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	matrix, err := compatibility.Parse(matrixPayload)
	if err != nil {
		t.Fatal(err)
	}
	if err := matrix.ValidateReferences(extracted); err != nil {
		t.Fatalf("packaged compatibility references are not self-contained: %v", err)
	}
}

func TestLoadReleaseSupportFilesExcludesLocalArtifacts(t *testing.T) {
	root := newReleaseFixture(t)

	want := []string{"demo/kept.txt", "demo/.env.example"}
	for _, name := range want {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.WriteFile(path, []byte("public\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	excluded := []string{
		"demo/.cache/cache.bin",
		"demo/.notes/private.txt",
		"demo/.state/report.json",
		"demo/.terraform/provider",
		"demo/.env.local",
		"demo/editor.swp",
		"demo/coverage.out",
		"demo/terraform.tfstate.backup",
	}
	for _, name := range excluded {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("private\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	stageReleaseFixture(t, root)
	if err := os.WriteFile(filepath.Join(root, "demo", "untracked-secret.txt"), []byte("private\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	entries, err := loadReleaseSupportFiles(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	names := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		names[entry.Name] = struct{}{}
	}
	for _, name := range want {
		if _, ok := names[name]; !ok {
			t.Errorf("release support files do not contain %q", name)
		}
	}
	for _, name := range excluded {
		if _, ok := names[name]; ok {
			t.Errorf("release support files contain local artifact %q", name)
		}
	}
	if _, ok := names["demo/untracked-secret.txt"]; ok {
		t.Error("release support files contain an untracked file")
	}
}

func TestLoadReleaseSupportFilesUsesIndexContentAndMode(t *testing.T) {
	root := newReleaseFixture(t)
	path := filepath.Join(root, "demo", "script.sh")
	if err := os.WriteFile(path, []byte("staged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stageReleaseFixture(t, root)
	runGit(t, root, "update-index", "--chmod=+x", "demo/script.sh")
	if err := os.WriteFile(path, []byte("unstaged\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	entries, err := loadReleaseSupportFiles(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name != "demo/script.sh" {
			continue
		}
		if got, want := string(entry.Body), "staged\n"; got != want {
			t.Fatalf("release support body = %q, want indexed body %q", got, want)
		}
		if got, want := entry.Mode, int64(0o755); got != want {
			t.Fatalf("release support mode = %#o, want indexed mode %#o", got, want)
		}
		return
	}
	t.Fatal("release support files do not contain demo/script.sh")
}

func TestLoadReleaseSupportFilesRejectsTrackedSymlink(t *testing.T) {
	root := newReleaseFixture(t)
	readme := filepath.Join(root, "README.md")
	if err := os.Remove(readme); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("LICENSE", readme); err != nil {
		t.Skipf("create test symlink: %v", err)
	}
	stageReleaseFixture(t, root)

	_, err := loadReleaseSupportFiles(context.Background(), root)
	if err == nil || !strings.Contains(err.Error(), "unsupported Git mode 120000") {
		t.Fatalf("expected tracked symlink rejection, got %v", err)
	}
}

func TestLoadReleaseSupportFilesRejectsOversizedFile(t *testing.T) {
	root := newReleaseFixture(t)
	path := filepath.Join(root, "README.md")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxReleaseSupportFileBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	stageReleaseFixture(t, root)

	_, err = loadReleaseSupportFiles(context.Background(), root)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected oversized release support file rejection, got %v", err)
	}
}

func newReleaseFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range releaseRootFiles {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range releaseSupportDirectories {
		if err := os.MkdirAll(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, root, "init", "--quiet")
	return root
}

func stageReleaseFixture(t *testing.T, root string) {
	t.Helper()
	runGit(t, root, "add", "--force", "--all")
}

func runGit(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
}

func countString(values []string, target string) int {
	count := 0
	for _, value := range values {
		if value == target {
			count++
		}
	}
	return count
}

func readArchive(t *testing.T, path string) map[string]string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	contents := map[string]string{}
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(tarReader)
		if err != nil {
			t.Fatal(err)
		}
		contents[header.Name] = string(body)
	}
	return contents
}
