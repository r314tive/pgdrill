package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r314tive/pgdrill/internal/planner"
)

func TestPlanCommandsValidateAndShowFixture(t *testing.T) {
	t.Parallel()

	path := filepath.Join("..", "..", "internal", "planner", "testdata", "fleet.yaml")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"plan", "validate", "-f", path}, &stdout, &stderr); code != 0 {
		t.Fatalf("plan validate exit = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Mutations:") || !strings.Contains(stdout.String(), "2") {
		t.Fatalf("plan validate output = %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"plan", "show", "-f", path, "-format", "json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("plan show exit = %d, stderr = %q", code, stderr.String())
	}
	var output planner.Plan
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode plan output: %v", err)
	}
	if err := output.Validate(); err != nil {
		t.Fatalf("output.Validate() error = %v", err)
	}
}

func TestPlanValidateFailsOnPlacementRejectionWhileShowExplainsIt(t *testing.T) {
	t.Parallel()

	fixturePath := filepath.Join("..", "..", "internal", "planner", "testdata", "fleet.yaml")
	payload, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	payload = bytes.Replace(
		payload,
		[]byte("    target_pool: demo-local\n"),
		[]byte("    target_pool: demo-local\n    target_selector:\n      ids: [restore-a]\n"),
		1,
	)
	path := filepath.Join(t.TempDir(), "fleet.yaml")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"plan", "validate", "-f", path}, &stdout, &stderr); code != 1 {
		t.Fatalf("plan validate exit = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "1 rejected placement") {
		t.Fatalf("plan validate stderr = %q", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"plan", "show", "-f", path}, &stdout, &stderr); code != 0 {
		t.Fatalf("plan show exit = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "no_compatible_target") || !strings.Contains(stdout.String(), "prod/ledger") {
		t.Fatalf("plan show output = %q", stdout.String())
	}
}

func TestPlanCommandRejectsUsageErrors(t *testing.T) {
	t.Parallel()

	tests := [][]string{
		{"plan"},
		{"plan", "validate"},
		{"plan", "show"},
		{"plan", "show", "-f", "missing.yaml", "-format", "xml"},
	}
	for _, args := range tests {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code == 0 {
			t.Fatalf("run(%v) unexpectedly succeeded", args)
		}
	}
}
