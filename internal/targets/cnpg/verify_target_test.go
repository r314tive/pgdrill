package cnpg

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

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
	target, _, cleanup := boundVerifyTarget(t, spec, client, LifecycleOptions{CleanupPVC: true})

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
	if err := target.BeginOperation(cleanup); err != nil {
		t.Fatalf("BeginOperation(cleanup) error = %v", err)
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
	target, _, _ := boundVerifyTarget(t, spec, client, LifecycleOptions{})

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

func TestVerifyTargetReconcilesOwnedReadyClusterAfterExecutorLoss(t *testing.T) {
	spec := testVerifyClusterSpec(t)
	client := &fakeLifecycleClient{instance: Instance{
		PodName:    spec.InstancePodName,
		Host:       "verify-rw.d003-db.svc",
		Port:       5432,
		ConnString: "host=/controller/run dbname=postgres user=postgres",
	}}
	target, start, _ := boundVerifyTarget(t, spec, client, LifecycleOptions{})
	client.ownedCluster = OwnedCluster{Found: true, Name: target.Spec.Name}

	reconciliation, err := target.Reconcile(context.Background(), verifyCheckpoint(start))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if reconciliation.Disposition != model.ReconciliationCompleted || reconciliation.Postgres == nil {
		t.Fatalf("unexpected reconciliation %#v", reconciliation)
	}
	if len(reconciliation.Report.Checks) != 1 || reconciliation.Report.Checks[0].Status != model.CheckStatusPassed {
		t.Fatalf("unexpected reconciliation report %#v", reconciliation.Report)
	}
	if got, want := client.calls, []string{"find-owned", "wait"}; !slices.Equal(got, want) {
		t.Fatalf("reconciliation calls = %#v, want %#v", got, want)
	}
}

func TestVerifyTargetReconciliationDoesNotCreateMissingCluster(t *testing.T) {
	spec := testVerifyClusterSpec(t)
	client := &fakeLifecycleClient{}
	target, start, cleanup := boundVerifyTarget(t, spec, client, LifecycleOptions{})

	startResult, err := target.Reconcile(context.Background(), verifyCheckpoint(start))
	if err != nil {
		t.Fatalf("Reconcile(start) error = %v", err)
	}
	if startResult.Disposition != model.ReconciliationNotApplied {
		t.Fatalf("start reconciliation = %#v", startResult)
	}
	if err := target.BeginOperation(cleanup); err != nil {
		t.Fatalf("BeginOperation(cleanup) error = %v", err)
	}
	cleanupResult, err := target.Reconcile(context.Background(), verifyCheckpoint(cleanup))
	if err != nil {
		t.Fatalf("Reconcile(cleanup) error = %v", err)
	}
	if cleanupResult.Disposition != model.ReconciliationCompleted {
		t.Fatalf("cleanup reconciliation = %#v", cleanupResult)
	}
	if slices.Contains(client.calls, "create") {
		t.Fatalf("reconciliation invoked create: %v", client.calls)
	}
}

func verifyCheckpoint(operation model.Operation) model.OperationCheckpoint {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	return model.OperationCheckpoint{
		SchemaVersion: model.CurrentOperationCheckpointSchemaVersion,
		Operation:     operation,
		State:         model.OperationStateIntent,
		StartedAt:     now,
		UpdatedAt:     now,
	}
}

func boundVerifyTarget(t *testing.T, spec VerifyClusterSpec, client Client, options LifecycleOptions) (*VerifyTarget, model.Operation, model.Operation) {
	t.Helper()
	identity := model.AttemptIdentity{
		RunID:      "run-1",
		AttemptID:  "attempt-1",
		SpecDigest: "sha256:" + strings.Repeat("a", 64),
	}
	ownerID, err := identity.OwnershipID()
	if err != nil {
		t.Fatalf("OwnershipID() error = %v", err)
	}
	spec.OwnershipID = ownerID
	spec.Labels[labelOwnershipID] = ownerID
	target := NewVerifyTarget(spec, client, options)
	attempt := model.AttemptContext{Identity: identity, Target: model.TargetSpec{Type: model.RestoreTargetKubernetes}}
	if err := target.BindAttempt(attempt); err != nil {
		t.Fatalf("BindAttempt() error = %v", err)
	}
	start, err := model.NewOperation(identity, model.DrillStageTargetStart, model.OperationManagedStart, "start-managed-target", 0)
	if err != nil {
		t.Fatalf("NewOperation(start) error = %v", err)
	}
	cleanup, err := model.NewOperation(identity, model.DrillStageTargetCleanup, model.OperationTargetCleanup, "cleanup-target", 1)
	if err != nil {
		t.Fatalf("NewOperation(cleanup) error = %v", err)
	}
	if err := target.BeginOperation(start); err != nil {
		t.Fatalf("BeginOperation(start) error = %v", err)
	}
	return target, start, cleanup
}
