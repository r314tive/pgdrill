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

func TestControllerStartAppliesClusterAndWaitsForInstance(t *testing.T) {
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

	if got, want := client.calls, []string{"apply", "wait"}; !reflect.DeepEqual(got, want) {
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
	if !hasOperation(evidence, "cnpg-manifest-render") || !hasOperation(evidence, "apply") || !hasOperation(evidence, "wait") {
		t.Fatalf("missing expected evidence operations %#v", evidence)
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

	if got, want := client.calls, []string{"apply", "wait", "capture:start-failed", "delete-cluster", "delete-pvcs"}; !reflect.DeepEqual(got, want) {
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

	if got, want := client.calls, []string{"apply", "wait", "capture:destroy", "delete-cluster", "delete-pvcs"}; !reflect.DeepEqual(got, want) {
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
	calls          []string
	manifest       []byte
	waitOptions    WaitOptions
	captureOptions CaptureOptions
	instance       Instance
	waitErr        error
}

func (c *fakeLifecycleClient) ApplyCluster(_ context.Context, _ VerifyClusterSpec, manifest []byte) ([]model.EvidenceRecord, error) {
	c.calls = append(c.calls, "apply")
	c.manifest = append([]byte{}, manifest...)
	return []model.EvidenceRecord{testEvidence("apply")}, nil
}

func (c *fakeLifecycleClient) WaitForInstanceReady(_ context.Context, _ VerifyClusterSpec, opts WaitOptions) (Instance, []model.EvidenceRecord, error) {
	c.calls = append(c.calls, "wait")
	c.waitOptions = opts
	return c.instance, []model.EvidenceRecord{testEvidence("wait")}, c.waitErr
}

func (c *fakeLifecycleClient) CaptureEvidence(_ context.Context, _ VerifyClusterSpec, _ Instance, opts CaptureOptions) ([]model.EvidenceRecord, error) {
	c.calls = append(c.calls, "capture:"+opts.Reason)
	c.captureOptions = opts
	return []model.EvidenceRecord{testEvidence("capture:" + opts.Reason)}, nil
}

func (c *fakeLifecycleClient) DeleteCluster(context.Context, VerifyClusterSpec) ([]model.EvidenceRecord, error) {
	c.calls = append(c.calls, "delete-cluster")
	return []model.EvidenceRecord{testEvidence("delete-cluster")}, nil
}

func (c *fakeLifecycleClient) DeletePVCs(context.Context, VerifyClusterSpec) ([]model.EvidenceRecord, error) {
	c.calls = append(c.calls, "delete-pvcs")
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
