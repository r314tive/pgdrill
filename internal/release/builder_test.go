package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
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

func TestReleaseLDFlagsRemoveHostDependentBuildID(t *testing.T) {
	releaseTime := time.Date(2026, 7, 20, 17, 0, 0, 0, time.UTC)
	flags := releaseLDFlags(Options{Version: "v0.1.0-alpha.8", Commit: "abcdef123456"}, releaseTime)

	for _, expected := range []string{
		"-s",
		"-w",
		"-buildid=",
		versionPackage + ".Version=v0.1.0-alpha.8",
		versionPackage + ".Commit=abcdef123456",
		versionPackage + ".Date=2026-07-20T17:00:00Z",
	} {
		if !strings.Contains(flags, expected) {
			t.Fatalf("release linker flags %q do not contain %q", flags, expected)
		}
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
