package cnpg

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/r314tive/pgdrill/internal/model"
)

func TestVerifyTargetReportsReadyAndDestroysOwnedTarget(t *testing.T) {
	spec := testVerifyClusterSpec(t)
	client := &fakeLifecycleClient{instance: Instance{
		PodName:    spec.InstancePodName,
		Host:       spec.Name + "-rw.namespace.svc",
		Port:       5432,
		ConnString: "host=/controller/run dbname=postgres user=postgres",
	}}
	target := NewVerifyTarget(spec, client, LifecycleOptions{CleanupPVC: true})

	pg, report, err := target.Start(context.Background())
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if target.Type() != model.RestoreTargetKubernetes {
		t.Fatalf("Type() = %q", target.Type())
	}
	if pg.Host != client.instance.Host || len(report.Checks) != 1 || report.Checks[0].Status != model.CheckStatusPassed {
		t.Fatalf("unexpected start result pg=%#v report=%#v", pg, report)
	}
	if got := report.Checks[0].Attributes["verify_cluster"]; got != spec.Name {
		t.Fatalf("verify_cluster attribute = %q, want %q", got, spec.Name)
	}
	if _, err := target.Destroy(context.Background()); err != nil {
		t.Fatalf("Destroy() error = %v", err)
	}
	if !slices.Contains(client.calls, "delete-cluster") || !slices.Contains(client.calls, "delete-pvcs") {
		t.Fatalf("cleanup calls %v", client.calls)
	}
}

func TestVerifyTargetReportsControllerStartFailure(t *testing.T) {
	wantErr := errors.New("create denied")
	spec := testVerifyClusterSpec(t)
	client := &fakeLifecycleClient{createErr: wantErr}
	target := NewVerifyTarget(spec, client, LifecycleOptions{})

	_, report, err := target.Start(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("Start() error = %v, want create error", err)
	}
	if len(report.Checks) != 1 || report.Checks[0].Status != model.CheckStatusFailed {
		t.Fatalf("unexpected failed start report %#v", report)
	}
	if report.Checks[0].Message == "" {
		t.Fatal("failed readiness check must retain an error message")
	}
}
