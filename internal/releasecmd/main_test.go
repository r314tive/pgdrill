package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunRejectsMissingAndUnknownCommands(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "missing", want: "usage:"},
		{name: "unknown", args: []string{"destroy"}, want: "unknown release command"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(test.args, &stdout, &stderr); code != 2 {
				t.Fatalf("run() code = %d, stderr = %q", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("run() stderr = %q, want %q", stderr.String(), test.want)
			}
		})
	}
}

func TestRunNotesWritesExactVersionEntry(t *testing.T) {
	dir := t.TempDir()
	changelogPath := filepath.Join(dir, "CHANGELOG.md")
	outputPath := filepath.Join(dir, "RELEASE_NOTES.md")
	changelog := `# Changelog

## [Unreleased]

- Future.

## [0.3.0] - 2026-07-29

### Fixed

- Exact release note.
`
	if err := os.WriteFile(changelogPath, []byte(changelog), 0o600); err != nil {
		t.Fatalf("write changelog: %v", err)
	}
	var stdout, stderr bytes.Buffer

	code := run([]string{
		"notes",
		"-version", "v0.3.0",
		"-changelog", changelogPath,
		"-output", outputPath,
	}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("run(notes) code = %d, stderr = %q", code, stderr.String())
	}
	payload, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read release notes: %v", err)
	}
	if string(payload) != "### Fixed\n\n- Exact release note.\n" {
		t.Fatalf("release notes = %q", payload)
	}
	if strings.TrimSpace(stdout.String()) != outputPath {
		t.Fatalf("run(notes) stdout = %q", stdout.String())
	}
}

func TestRunReleaseSubcommandsFailClosedOnInvalidInputs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		code int
		want string
	}{
		{
			name: "artifacts invalid target",
			args: []string{"artifacts", "-targets", "invalid"},
			code: 2,
			want: "parse release targets",
		},
		{
			name: "notes positional argument",
			args: []string{"notes", "extra"},
			code: 2,
			want: "does not accept positional arguments",
		},
		{
			name: "verify OCI missing archive",
			args: []string{"verify-oci"},
			code: 1,
			want: "verify OCI image",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(test.args, &stdout, &stderr); code != test.code {
				t.Fatalf("run() code = %d, want %d, stderr = %q", code, test.code, stderr.String())
			}
			if !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("run() stderr = %q, want %q", stderr.String(), test.want)
			}
		})
	}
}

func TestDefaultTargetsValueIsStableAndNonempty(t *testing.T) {
	value := defaultTargetsValue()
	for _, target := range []string{
		"linux/amd64",
		"linux/arm64",
		"darwin/amd64",
		"darwin/arm64",
	} {
		if !strings.Contains(value, target) {
			t.Fatalf("defaultTargetsValue() = %q, missing %q", value, target)
		}
	}
}
