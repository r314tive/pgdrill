package cnpg

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/command"
	"github.com/r314tive/pgdrill/internal/model"
)

func TestKubectlClientApplyUsesManifestStdin(t *testing.T) {
	runner := &fakeCommandRunner{}
	client := NewKubectlClient(KubectlConfig{
		Binary:     "/usr/local/bin/kubectl",
		Namespace:  "override-ns",
		Kubeconfig: "/tmp/kubeconfig",
		Context:    "d003",
		Timeout:    2 * time.Minute,
	}, runner)
	spec := testVerifyClusterSpec(t)

	evidence, err := client.ApplyCluster(context.Background(), spec, []byte("cluster-yaml"))
	if err != nil {
		t.Fatalf("apply cluster: %v", err)
	}

	if len(runner.invocations) != 1 {
		t.Fatalf("expected one invocation, got %d", len(runner.invocations))
	}
	inv := runner.invocations[0]
	if inv.Path != "/usr/local/bin/kubectl" {
		t.Fatalf("unexpected kubectl path %q", inv.Path)
	}
	wantArgs := []string{"--kubeconfig", "/tmp/kubeconfig", "--context", "d003", "-n", "override-ns", "apply", "-f", "-"}
	if !reflect.DeepEqual(inv.Args, wantArgs) {
		t.Fatalf("unexpected args: got %#v want %#v", inv.Args, wantArgs)
	}
	if string(inv.Stdin) != "cluster-yaml" {
		t.Fatalf("unexpected stdin %q", string(inv.Stdin))
	}
	if inv.Timeout != 2*time.Minute {
		t.Fatalf("unexpected timeout %s", inv.Timeout)
	}
	if !hasOperation(evidence, "kubectl-apply-cluster") {
		t.Fatalf("missing apply evidence %#v", evidence)
	}
}

func TestKubectlClientWaitReturnsRunningInstance(t *testing.T) {
	runner := &fakeCommandRunner{}
	client := NewKubectlClient(KubectlConfig{}, runner)
	spec := testVerifyClusterSpec(t)

	instance, evidence, err := client.WaitForInstanceReady(context.Background(), spec, WaitOptions{Timeout: 90 * time.Second})
	if err != nil {
		t.Fatalf("wait for instance: %v", err)
	}

	if len(runner.invocations) != 1 {
		t.Fatalf("expected one invocation, got %d", len(runner.invocations))
	}
	args := runner.invocations[0].Args
	wantArgs := []string{"-n", "d003-db", "wait", "--for=condition=Ready", "pod/" + spec.InstancePodName, "--timeout=90s"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("unexpected args: got %#v want %#v", args, wantArgs)
	}
	if instance.PodName != spec.InstancePodName {
		t.Fatalf("unexpected pod name %q", instance.PodName)
	}
	if instance.Host != spec.Name+"-rw.d003-db.svc" {
		t.Fatalf("unexpected host %q", instance.Host)
	}
	if !strings.Contains(instance.ConnString, spec.Name+"-rw.d003-db.svc:5432") {
		t.Fatalf("unexpected conn string %q", instance.ConnString)
	}
	if !hasOperation(evidence, "kubectl-wait-instance-ready") {
		t.Fatalf("missing wait evidence %#v", evidence)
	}
}

func TestKubectlClientCaptureEvidenceIsBestEffort(t *testing.T) {
	runner := &fakeCommandRunner{
		failWhenArgsContain: "logs",
	}
	client := NewKubectlClient(KubectlConfig{}, runner)
	spec := testVerifyClusterSpec(t)

	evidence, err := client.CaptureEvidence(context.Background(), spec, Instance{PodName: spec.InstancePodName}, CaptureOptions{
		Reason:          "start-failed",
		EventsTail:      200,
		PostgresLogTail: 5000,
	})
	if err != nil {
		t.Fatalf("capture evidence should be best effort, got %v", err)
	}

	if len(runner.invocations) != 6 {
		t.Fatalf("expected six capture invocations, got %d", len(runner.invocations))
	}
	if !hasOperation(evidence, "kubectl-capture-cluster-yaml") ||
		!hasOperation(evidence, "kubectl-capture-full-recovery-log") ||
		!hasOperation(evidence, "kubectl-capture-postgres-log") ||
		!hasOperation(evidence, "kubectl-capture-summary") {
		t.Fatalf("missing capture evidence %#v", evidence)
	}

	summary := evidence[len(evidence)-1]
	if summary.Attributes["capture_status"] != "warning" || summary.Attributes["best_effort"] != "true" {
		t.Fatalf("unexpected capture summary %#v", summary.Attributes)
	}
	if !strings.Contains(summary.Attributes["capture_error"], "kubectl-capture-full-recovery-log") {
		t.Fatalf("expected capture error in summary, got %#v", summary.Attributes)
	}
}

func TestKubectlClientDeletePVCsUsesClusterLabel(t *testing.T) {
	runner := &fakeCommandRunner{}
	client := NewKubectlClient(KubectlConfig{Timeout: 10 * time.Minute}, runner)
	spec := testVerifyClusterSpec(t)

	evidence, err := client.DeletePVCs(context.Background(), spec)
	if err != nil {
		t.Fatalf("delete pvcs: %v", err)
	}

	if len(runner.invocations) != 1 {
		t.Fatalf("expected one invocation, got %d", len(runner.invocations))
	}
	args := runner.invocations[0].Args
	wantArgs := []string{"-n", "d003-db", "delete", "pvc", "-l", "cnpg.io/cluster=" + spec.Name, "--wait=true", "--timeout=600s"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("unexpected args: got %#v want %#v", args, wantArgs)
	}
	if !hasOperation(evidence, "kubectl-delete-pvcs") {
		t.Fatalf("missing delete pvcs evidence %#v", evidence)
	}
}

func TestKubectlClientLatestCompletedBackupSelectsNewest(t *testing.T) {
	runner := &fakeCommandRunner{
		stdoutByArgContains: map[string]string{
			"backups.postgresql.cnpg.io": `{
  "items": [
    {
      "metadata": {"name": "altbox-old", "creationTimestamp": "2026-07-06T01:00:00Z"},
      "spec": {"cluster": {"name": "altbox"}},
      "status": {"phase": "completed"}
    },
    {
      "metadata": {"name": "altbox-running", "creationTimestamp": "2026-07-07T01:00:00Z"},
      "spec": {"cluster": {"name": "altbox"}},
      "status": {"phase": "running"}
    },
    {
      "metadata": {"name": "other-new", "creationTimestamp": "2026-07-08T01:00:00Z"},
      "spec": {"cluster": {"name": "other"}},
      "status": {"phase": "completed"}
    },
    {
      "metadata": {"name": "altbox-new", "creationTimestamp": "2026-07-07T02:00:00Z"},
      "spec": {"cluster": {"name": "altbox"}},
      "status": {"phase": "completed"}
    }
  ]
}`,
		},
	}
	client := NewKubectlClient(KubectlConfig{}, runner)
	spec := testVerifyClusterSpec(t)

	backupName, evidence, err := client.LatestCompletedBackup(context.Background(), spec)
	if err != nil {
		t.Fatalf("latest completed backup: %v", err)
	}

	if backupName != "altbox-new" {
		t.Fatalf("unexpected backup name %q", backupName)
	}
	if !hasOperation(evidence, "kubectl-discover-cnpg-backups") {
		t.Fatalf("missing discovery evidence %#v", evidence)
	}
	if got := runner.invocations[0].Args; !reflect.DeepEqual(got, []string{"-n", "d003-db", "get", "backups.postgresql.cnpg.io", "-o", "json"}) {
		t.Fatalf("unexpected args %#v", got)
	}
}

func TestKubectlClientSourceClusterImage(t *testing.T) {
	runner := &fakeCommandRunner{
		stdoutByArgContains: map[string]string{
			"cluster.postgresql.cnpg.io": `{"spec":{"imageName":"ghcr.io/cloudnative-pg/postgresql:16.4"}}`,
		},
	}
	client := NewKubectlClient(KubectlConfig{}, runner)
	spec := testVerifyClusterSpec(t)

	image, evidence, err := client.SourceClusterImage(context.Background(), spec)
	if err != nil {
		t.Fatalf("source cluster image: %v", err)
	}

	if image != "ghcr.io/cloudnative-pg/postgresql:16.4" {
		t.Fatalf("unexpected image %q", image)
	}
	if !hasOperation(evidence, "kubectl-discover-cnpg-source-image") {
		t.Fatalf("missing image discovery evidence %#v", evidence)
	}
}

type fakeCommandRunner struct {
	invocations         []command.Invocation
	failWhenArgsContain string
	stdoutByArgContains map[string]string
}

func (r *fakeCommandRunner) Run(_ context.Context, inv command.Invocation) (command.Result, error) {
	r.invocations = append(r.invocations, inv)

	success := true
	exitCode := 0
	if r.failWhenArgsContain != "" && strings.Contains(strings.Join(inv.Args, " "), r.failWhenArgsContain) {
		success = false
		exitCode = 1
	}

	stdout := "ok\n"
	for marker, value := range r.stdoutByArgContains {
		if strings.Contains(strings.Join(inv.Args, " "), marker) {
			stdout = value
			break
		}
	}

	now := time.Date(2026, 7, 7, 8, 40, 0, 0, time.UTC).Add(time.Duration(len(r.invocations)) * time.Second)
	return command.Result{
		Raw: command.RawEvidence{
			Path:   inv.Path,
			Args:   append([]string{}, inv.Args...),
			Stdout: []byte(stdout),
		},
		Evidence: model.CommandEvidence{
			Path:       inv.Path,
			Args:       append([]string{}, inv.Args...),
			StartedAt:  now.Add(-1 * time.Second),
			FinishedAt: now,
			ExitStatus: model.ExitStatus{
				Started:  true,
				Exited:   true,
				Success:  success,
				ExitCode: exitCode,
			},
			Stdout: stdout,
		},
	}, nil
}
