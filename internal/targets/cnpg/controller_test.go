package cnpg

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
)

func TestControllerStartCreatesClusterAndWaitsForInstance(t *testing.T) {
	client := &fakeLifecycleClient{
		instance: Instance{
			PodName:    "verify-altbox-abc12345-1",
			Host:       "verify-altbox-abc12345-rw.d003-db.svc",
			Port:       5432,
			ConnString: "postgresql://verify-altbox-abc12345-rw.d003-db.svc:5432/postgres?sslmode=disable",
		},
	}
	controller := Controller{
		Spec:   testVerifyClusterSpec(t),
		Client: client,
		Options: LifecycleOptions{
			WaitTimeout:  3 * time.Minute,
			PollInterval: 10 * time.Second,
			Clock:        fixedClock(time.Date(2026, 7, 7, 8, 30, 0, 0, time.UTC)),
		},
	}

	pg, evidence, err := controller.Start(context.Background())
	if err != nil {
		t.Fatalf("start controller: %v", err)
	}

	if got, want := client.calls, []string{"create", "wait"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected calls: got %#v want %#v", got, want)
	}
	if !strings.Contains(string(client.manifest), "bootstrap:") || !strings.Contains(string(client.manifest), "altbox-backup-20260707") {
		t.Fatalf("unexpected manifest:\n%s", client.manifest)
	}
	if client.waitOptions.Timeout != 3*time.Minute || client.waitOptions.PollInterval != 10*time.Second {
		t.Fatalf("unexpected wait options %#v", client.waitOptions)
	}
	if pg.ConnString != client.instance.ConnString || pg.Host != client.instance.Host || pg.Port != 5432 {
		t.Fatalf("unexpected running postgres %#v", pg)
	}
	if !hasOperation(evidence, "cnpg-manifest-render") || !hasOperation(evidence, "create") || !hasOperation(evidence, "wait") {
		t.Fatalf("missing expected evidence operations %#v", evidence)
	}
}

func TestControllerStartUsesRestoreScaleWaitDefault(t *testing.T) {
	client := &fakeLifecycleClient{}
	controller := Controller{
		Spec:   testVerifyClusterSpec(t),
		Client: client,
	}

	if _, _, err := controller.Start(context.Background()); err != nil {
		t.Fatalf("start controller: %v", err)
	}
	if client.waitOptions.Timeout != 2*time.Hour || client.waitOptions.PollInterval != DefaultPollInterval {
		t.Fatalf("unexpected default wait options %#v", client.waitOptions)
	}
}

func TestControllerStartFailureCapturesAndCleansUp(t *testing.T) {
	client := &fakeLifecycleClient{waitErr: errors.New("full-recovery job failed")}
	controller := Controller{
		Spec:   testVerifyClusterSpec(t),
		Client: client,
		Options: LifecycleOptions{
			CaptureLogs:   true,
			CleanupOnFail: true,
			CleanupPVC:    true,
			EventsTail:    200,
		},
	}

	_, evidence, err := controller.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "full-recovery job failed") {
		t.Fatalf("expected wait failure, got %v", err)
	}

	if got, want := client.calls, []string{"create", "wait", "capture:start-failed", "delete-cluster", "delete-pvcs"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected calls: got %#v want %#v", got, want)
	}
	if client.captureOptions.Reason != "start-failed" || client.captureOptions.EventsTail != 200 {
		t.Fatalf("unexpected capture options %#v", client.captureOptions)
	}
	if controller.created {
		t.Fatal("expected controller to be marked not created after successful cleanup")
	}
	if !hasOperation(evidence, "capture:start-failed") || !hasOperation(evidence, "delete-cluster") || !hasOperation(evidence, "delete-pvcs") {
		t.Fatalf("missing cleanup evidence %#v", evidence)
	}
}

func TestControllerAmbiguousCreateFailureCleansUpByOwnership(t *testing.T) {
	client := &fakeLifecycleClient{createErr: errors.New("create response lost")}
	controller := Controller{
		Spec:   testVerifyClusterSpec(t),
		Client: client,
		Options: LifecycleOptions{
			CaptureLogs:   true,
			CleanupOnFail: true,
			CleanupPVC:    true,
		},
	}

	_, evidence, err := controller.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "create response lost") {
		t.Fatalf("expected create failure, got %v", err)
	}
	if got, want := client.calls, []string{"create", "delete-cluster", "delete-pvcs"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected calls: got %#v want %#v", got, want)
	}
	if controller.created {
		t.Fatal("expected successful uncertain-create cleanup to release ownership")
	}
	if !hasOperation(evidence, "create") || !hasOperation(evidence, "delete-cluster") || !hasOperation(evidence, "delete-pvcs") {
		t.Fatalf("missing create failure evidence %#v", evidence)
	}
}

func TestControllerCreateStartFailureDoesNotDelete(t *testing.T) {
	client := &fakeLifecycleClient{
		createErr: errors.New("executable not found"),
		createEvidence: []model.EvidenceRecord{{
			ID:      "test:create",
			Command: &model.CommandEvidence{},
		}},
	}
	controller := Controller{
		Spec:   testVerifyClusterSpec(t),
		Client: client,
		Options: LifecycleOptions{
			CaptureLogs:   true,
			CleanupOnFail: true,
			CleanupPVC:    true,
		},
	}

	_, _, err := controller.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "executable not found") {
		t.Fatalf("expected create start failure, got %v", err)
	}
	if got, want := client.calls, []string{"create"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("command start failure must not trigger cleanup: got %#v want %#v", got, want)
	}
	if controller.created {
		t.Fatal("command start failure must not claim possible target ownership")
	}
}

func TestControllerAmbiguousCreateWithExplicitNameUsesOwnershipCleanup(t *testing.T) {
	spec := testVerifyClusterSpec(t)
	client := &fakeLifecycleClient{createErr: errors.New("request timed out")}
	controller := Controller{
		Spec:   spec,
		Client: client,
		Options: LifecycleOptions{
			CleanupOnFail: true,
			CleanupPVC:    true,
		},
	}

	_, _, err := controller.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "request timed out") {
		t.Fatalf("expected ambiguous create error, got %v", err)
	}
	if got, want := client.calls, []string{"create", "delete-cluster", "delete-pvcs"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("explicit-name cleanup must remain ownership-scoped: got %#v want %#v", got, want)
	}
	if controller.created {
		t.Fatal("successful ownership cleanup must release possible ownership state")
	}
}

func TestControllerAmbiguousCreateRespectsDisabledFailureCleanup(t *testing.T) {
	client := &fakeLifecycleClient{createErr: errors.New("request timed out")}
	controller := Controller{
		Spec:   testVerifyClusterSpec(t),
		Client: client,
		Options: LifecycleOptions{
			CleanupOnFail: false,
			CleanupPVC:    true,
		},
	}

	_, _, err := controller.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "request timed out") {
		t.Fatalf("expected ambiguous create error, got %v", err)
	}
	if got, want := client.calls, []string{"create"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("disabled failure cleanup invoked extra operations: got %#v want %#v", got, want)
	}
	if !controller.created {
		t.Fatal("ambiguous create must retain possible ownership when cleanup is disabled")
	}
}

func TestCreateMayHaveSucceededClassifiesCommandEvidence(t *testing.T) {
	tests := []struct {
		name     string
		evidence []model.EvidenceRecord
		want     bool
	}{
		{name: "missing command evidence is ambiguous", want: true},
		{
			name:     "command did not start",
			evidence: []model.EvidenceRecord{{Command: &model.CommandEvidence{}}},
			want:     false,
		},
		{
			name: "nonzero exit after start is ambiguous",
			evidence: []model.EvidenceRecord{{Command: &model.CommandEvidence{ExitStatus: model.ExitStatus{
				Started: true,
				Exited:  true,
			}}}},
			want: true,
		},
		{
			name: "timeout is ambiguous",
			evidence: []model.EvidenceRecord{{Command: &model.CommandEvidence{ExitStatus: model.ExitStatus{
				Started:  true,
				Exited:   true,
				TimedOut: true,
			}}}},
			want: true,
		},
		{
			name: "cancellation is ambiguous",
			evidence: []model.EvidenceRecord{{Command: &model.CommandEvidence{ExitStatus: model.ExitStatus{
				Started:  true,
				Exited:   true,
				Canceled: true,
			}}}},
			want: true,
		},
		{
			name: "success followed by client error is ambiguous",
			evidence: []model.EvidenceRecord{{Command: &model.CommandEvidence{ExitStatus: model.ExitStatus{
				Started: true,
				Exited:  true,
				Success: true,
			}}}},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := createMayHaveSucceeded(tt.evidence); got != tt.want {
				t.Fatalf("createMayHaveSucceeded() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestControllerStartCancellationFinalizesWithLiveContexts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := &fakeLifecycleClient{
		waitHook: func() error {
			cancel()
			return ctx.Err()
		},
	}
	controller := Controller{
		Spec:   testVerifyClusterSpec(t),
		Client: client,
		Options: LifecycleOptions{
			CaptureLogs:   true,
			CleanupOnFail: true,
			CleanupPVC:    true,
		},
	}

	_, _, err := controller.Start(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation error, got %v", err)
	}
	if client.captureContextErr != nil {
		t.Fatalf("capture inherited canceled context: %v", client.captureContextErr)
	}
	if client.deleteContextErr != nil {
		t.Fatalf("cleanup inherited canceled context: %v", client.deleteContextErr)
	}
	if got, want := client.calls, []string{"create", "wait", "capture:start-failed", "delete-cluster", "delete-pvcs"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected calls: got %#v want %#v", got, want)
	}
}

func TestControllerDestroyCapturesAndDeletesCluster(t *testing.T) {
	client := &fakeLifecycleClient{
		instance: Instance{
			PodName: "verify-altbox-abc12345-1",
			Host:    "verify-altbox-abc12345-rw.d003-db.svc",
			Port:    5432,
		},
	}
	controller := Controller{
		Spec:   testVerifyClusterSpec(t),
		Client: client,
		Options: LifecycleOptions{
			CaptureLogs:     true,
			CleanupPVC:      true,
			PostgresLogTail: 5000,
		},
	}

	if _, _, err := controller.Start(context.Background()); err != nil {
		t.Fatalf("start controller: %v", err)
	}
	evidence, err := controller.Destroy(context.Background())
	if err != nil {
		t.Fatalf("destroy controller: %v", err)
	}

	if got, want := client.calls, []string{"create", "wait", "capture:destroy", "delete-cluster", "delete-pvcs"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected calls: got %#v want %#v", got, want)
	}
	if client.captureOptions.Reason != "destroy" || client.captureOptions.PostgresLogTail != 5000 {
		t.Fatalf("unexpected capture options %#v", client.captureOptions)
	}
	if controller.created {
		t.Fatal("expected controller to be marked not created after destroy")
	}
	if !hasOperation(evidence, "capture:destroy") || !hasOperation(evidence, "delete-cluster") || !hasOperation(evidence, "delete-pvcs") {
		t.Fatalf("missing destroy evidence %#v", evidence)
	}
}

func TestControllerStartRequiresClient(t *testing.T) {
	controller := Controller{Spec: testVerifyClusterSpec(t)}

	_, _, err := controller.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "client is required") {
		t.Fatalf("expected missing client error, got %v", err)
	}
}

type fakeLifecycleClient struct {
	calls             []string
	manifest          []byte
	waitOptions       WaitOptions
	captureOptions    CaptureOptions
	instance          Instance
	createErr         error
	createEvidence    []model.EvidenceRecord
	waitErr           error
	waitHook          func() error
	captureContextErr error
	deleteContextErr  error
}

func (c *fakeLifecycleClient) CreateCluster(_ context.Context, _ VerifyClusterSpec, manifest []byte) ([]model.EvidenceRecord, error) {
	c.calls = append(c.calls, "create")
	c.manifest = append([]byte{}, manifest...)
	if c.createEvidence != nil {
		return c.createEvidence, c.createErr
	}
	return []model.EvidenceRecord{testEvidence("create")}, c.createErr
}

func (c *fakeLifecycleClient) WaitForInstanceReady(_ context.Context, _ VerifyClusterSpec, opts WaitOptions) (Instance, []model.EvidenceRecord, error) {
	c.calls = append(c.calls, "wait")
	c.waitOptions = opts
	if c.waitHook != nil {
		return c.instance, []model.EvidenceRecord{testEvidence("wait")}, c.waitHook()
	}
	return c.instance, []model.EvidenceRecord{testEvidence("wait")}, c.waitErr
}

func (c *fakeLifecycleClient) CaptureEvidence(ctx context.Context, _ VerifyClusterSpec, _ Instance, opts CaptureOptions) ([]model.EvidenceRecord, error) {
	c.calls = append(c.calls, "capture:"+opts.Reason)
	c.captureOptions = opts
	c.captureContextErr = ctx.Err()
	return []model.EvidenceRecord{testEvidence("capture:" + opts.Reason)}, nil
}

func (c *fakeLifecycleClient) DeleteCluster(ctx context.Context, _ VerifyClusterSpec) ([]model.EvidenceRecord, error) {
	c.calls = append(c.calls, "delete-cluster")
	c.deleteContextErr = ctx.Err()
	return []model.EvidenceRecord{testEvidence("delete-cluster")}, nil
}

func (c *fakeLifecycleClient) DeletePVCs(ctx context.Context, _ VerifyClusterSpec) ([]model.EvidenceRecord, error) {
	c.calls = append(c.calls, "delete-pvcs")
	if c.deleteContextErr == nil {
		c.deleteContextErr = ctx.Err()
	}
	return []model.EvidenceRecord{testEvidence("delete-pvcs")}, nil
}

func testVerifyClusterSpec(t *testing.T) VerifyClusterSpec {
	t.Helper()
	spec, err := BuildVerifyClusterSpec(Config{
		Namespace:     "d003-db",
		SourceCluster: "altbox",
		BackupName:    "altbox-backup-20260707",
		ImageName:     "ghcr.io/cloudnative-pg/postgresql:16",
	}, "drill-1")
	if err != nil {
		t.Fatalf("build verify cluster spec: %v", err)
	}
	return spec
}

func testEvidence(operation string) model.EvidenceRecord {
	return model.EvidenceRecord{
		ID:          "test:" + operation,
		Kind:        model.EvidenceRuntime,
		Source:      string(model.RestoreTargetKubernetes),
		CollectedAt: time.Date(2026, 7, 7, 8, 30, 0, 0, time.UTC),
		Attributes: map[string]string{
			"operation": operation,
		},
	}
}

func hasOperation(records []model.EvidenceRecord, operation string) bool {
	for _, record := range records {
		if record.Attributes["operation"] == operation {
			return true
		}
	}
	return false
}

func fixedClock(value time.Time) func() time.Time {
	return func() time.Time {
		return value
	}
}
