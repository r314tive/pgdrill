package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersionCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := run([]string{"version"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("expected exit code 0, got %d: %s", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got == "" {
		t.Fatal("expected version output")
	}
}

func TestExplainJSONCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := run([]string{"explain", "-format", "json"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("expected exit code 0, got %d: %s", code, stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, `"providers"`) {
		t.Fatalf("expected providers in json output, got: %s", got)
	}
}

func TestUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := run([]string{"missing"}, &stdout, &stderr)

	if code != 2 {
		t.Fatalf("expected exit code 2, got %d", code)
	}
	if got := stderr.String(); !strings.Contains(got, "unknown command") {
		t.Fatalf("expected unknown command error, got: %s", got)
	}
}
