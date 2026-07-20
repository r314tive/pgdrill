package cnpg

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/command"
)

func TestPodExecRunnerWrapsCommandWithoutShell(t *testing.T) {
	transport := &fakeCommandRunner{}
	spec := testVerifyClusterSpec(t)
	runner := NewPodExecRunner(KubectlConfig{
		Binary:       "/usr/local/bin/kubectl",
		Kubeconfig:   "/tmp/kubeconfig",
		Context:      "d003",
		Namespace:    "override-ns",
		RedactValues: []string{"target-secret"},
	}, spec, transport)

	stdin := []byte("select 1;\n")
	_, err := runner.Run(context.Background(), command.Invocation{
		Path:    "/usr/bin/psql",
		Args:    []string{"-X", "-d", DefaultPodConnString},
		Env:     map[string]string{"PGUSER": "postgres", "PGPASSWORD": "secret"},
		Timeout: 30 * time.Second,
		Stdin:   stdin,
		RedactValues: []string{
			"probe-secret",
		},
	})
	if err != nil {
		t.Fatalf("run pod command: %v", err)
	}
	if len(transport.invocations) != 1 {
		t.Fatalf("expected one kubectl invocation, got %d", len(transport.invocations))
	}

	inv := transport.invocations[0]
	if inv.Path != "/usr/local/bin/kubectl" {
		t.Fatalf("unexpected transport path %q", inv.Path)
	}
	wantArgs := []string{
		"--kubeconfig", "/tmp/kubeconfig",
		"--context", "d003",
		"-n", "override-ns",
		"exec", "-i", spec.InstancePodName, "-c", "postgres", "--",
		"env", "PGPASSWORD=secret", "PGUSER=postgres",
		"/usr/bin/psql", "-X", "-d", DefaultPodConnString,
	}
	if !reflect.DeepEqual(inv.Args, wantArgs) {
		t.Fatalf("unexpected pod exec args:\ngot  %#v\nwant %#v", inv.Args, wantArgs)
	}
	if !reflect.DeepEqual(inv.Stdin, stdin) || inv.Timeout != 30*time.Second {
		t.Fatalf("unexpected stdin/timeout: stdin=%q timeout=%s", inv.Stdin, inv.Timeout)
	}
	for _, secret := range []string{"target-secret", "probe-secret", "secret"} {
		if !containsString(inv.RedactValues, secret) {
			t.Fatalf("value %q is not redacted: %#v", secret, inv.RedactValues)
		}
	}
}

func TestPodExecRunnerOmitsInteractiveFlagWithoutStdin(t *testing.T) {
	transport := &fakeCommandRunner{}
	runner := NewPodExecRunner(KubectlConfig{Namespace: "d003-db"}, testVerifyClusterSpec(t), transport)

	_, err := runner.Run(context.Background(), command.Invocation{Path: "psql", Args: []string{"--version"}})
	if err != nil {
		t.Fatalf("run pod command: %v", err)
	}
	if len(transport.invocations) != 1 {
		t.Fatalf("expected one kubectl invocation, got %d", len(transport.invocations))
	}
	if containsString(transport.invocations[0].Args, "-i") {
		t.Fatalf("unexpected interactive flag: %#v", transport.invocations[0].Args)
	}
	if transport.invocations[0].Stdin != nil {
		t.Fatalf("nil stdin changed to non-nil: %#v", transport.invocations[0].Stdin)
	}
}

func TestPodExecRunnerRejectsWorkDir(t *testing.T) {
	transport := &fakeCommandRunner{}
	runner := NewPodExecRunner(KubectlConfig{}, testVerifyClusterSpec(t), transport)

	_, err := runner.Run(context.Background(), command.Invocation{Path: "psql", WorkDir: "/tmp"})
	if err == nil || !strings.Contains(err.Error(), "work_dir") {
		t.Fatalf("expected work_dir error, got %v", err)
	}
	if len(transport.invocations) != 0 {
		t.Fatalf("transport invoked after validation failure: %#v", transport.invocations)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
