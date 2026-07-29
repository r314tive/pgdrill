package cnpg

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/artifact"
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
	artifactStore := artifact.NewMemoryStore()
	controller := Controller{
		Spec:      testVerifyClusterSpec(t),
		Client:    client,
		Artifacts: artifactStore,
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

	if got, want := client.calls, []string{"create", "find-owned", "wait"}; !reflect.DeepEqual(got, want) {
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
	if len(controller.artifactRefs) != 1 || len(evidence[0].ArtifactIDs) != 1 || evidence[0].ArtifactIDs[0] != controller.artifactRefs[0].ID {
		t.Fatalf("manifest artifact provenance is incomplete: refs=%#v evidence=%#v", controller.artifactRefs, evidence[0])
	}
	contractDigest, err := controller.Spec.ContractDigest()
	if err != nil {
		t.Fatalf("compute expected recovery contract: %v", err)
	}
	if evidence[0].Attributes["recovery_contract"] != contractDigest ||
		evidence[0].Attributes["verify_cluster_uid"] != "" {
		t.Fatalf("manifest evidence made an invalid identity claim: %#v", evidence[0].Attributes)
	}
	storedManifest, err := artifactStore.Read(context.Background(), controller.artifactRefs[0])
	if err != nil {
		t.Fatalf("read manifest artifact: %v", err)
	}
	if string(storedManifest) != string(client.manifest) {
		t.Fatalf("stored manifest does not match kubectl input")
	}
}

func TestControllerStartUsesRestoreScaleWaitDefault(t *testing.T) {
	client := &fakeLifecycleClient{}
	controller := Controller{
		Spec:      testVerifyClusterSpec(t),
		Client:    client,
		Artifacts: artifact.NewMemoryStore(),
	}

	if _, _, err := controller.Start(context.Background()); err != nil {
		t.Fatalf("start controller: %v", err)
	}
	if client.waitOptions.Timeout != 2*time.Hour || client.waitOptions.PollInterval != DefaultPollInterval {
		t.Fatalf("unexpected default wait options %#v", client.waitOptions)
	}
}

func TestControllerPersistsManifestBeforeCreate(t *testing.T) {
	wantErr := errors.New("artifact store unavailable")
	client := &fakeLifecycleClient{}
	controller := Controller{
		Spec:      testVerifyClusterSpec(t),
		Client:    client,
		Artifacts: failingArtifactSink{err: wantErr},
	}

	_, _, err := controller.Start(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("Start() error = %v, want artifact error", err)
	}
	if len(client.calls) != 0 {
		t.Fatalf("artifact failure must prevent target mutation, calls=%v", client.calls)
	}
}

func TestControllerStartFailureCapturesAndCleansUp(t *testing.T) {
	client := &fakeLifecycleClient{waitErr: errors.New("full-recovery job failed")}
	controller := Controller{
		Spec:      testVerifyClusterSpec(t),
		Client:    client,
		Artifacts: artifact.NewMemoryStore(),
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

	if got, want := client.calls, []string{"create", "find-owned", "wait", "capture:start-failed", "find-owned", "find-owned-pvcs", "delete-cluster", "find-owned", "find-owned-pvcs", "delete-pvcs", "find-owned-pvcs"}; !reflect.DeepEqual(got, want) {
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
		Spec:      testVerifyClusterSpec(t),
		Client:    client,
		Artifacts: artifact.NewMemoryStore(),
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
	if got, want := client.calls, []string{"create", "find-owned", "find-owned-pvcs", "delete-cluster", "find-owned", "find-owned-pvcs", "delete-pvcs", "find-owned-pvcs"}; !reflect.DeepEqual(got, want) {
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
		Spec:      testVerifyClusterSpec(t),
		Client:    client,
		Artifacts: artifact.NewMemoryStore(),
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
		Spec:      spec,
		Client:    client,
		Artifacts: artifact.NewMemoryStore(),
		Options: LifecycleOptions{
			CleanupOnFail: true,
			CleanupPVC:    true,
		},
	}

	_, _, err := controller.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "request timed out") {
		t.Fatalf("expected ambiguous create error, got %v", err)
	}
	if got, want := client.calls, []string{"create", "find-owned", "find-owned-pvcs", "delete-cluster", "find-owned", "find-owned-pvcs", "delete-pvcs", "find-owned-pvcs"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("explicit-name cleanup must remain ownership-scoped: got %#v want %#v", got, want)
	}
	if controller.created {
		t.Fatal("successful ownership cleanup must release possible ownership state")
	}
}

func TestControllerAmbiguousCreateRespectsDisabledFailureCleanup(t *testing.T) {
	client := &fakeLifecycleClient{createErr: errors.New("request timed out")}
	controller := Controller{
		Spec:      testVerifyClusterSpec(t),
		Client:    client,
		Artifacts: artifact.NewMemoryStore(),
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
		Spec:      testVerifyClusterSpec(t),
		Client:    client,
		Artifacts: artifact.NewMemoryStore(),
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
	if got, want := client.calls, []string{"create", "find-owned", "wait", "capture:start-failed", "find-owned", "find-owned-pvcs", "delete-cluster", "find-owned", "find-owned-pvcs", "delete-pvcs", "find-owned-pvcs"}; !reflect.DeepEqual(got, want) {
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
		Spec:      testVerifyClusterSpec(t),
		Client:    client,
		Artifacts: artifact.NewMemoryStore(),
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

	if got, want := client.calls, []string{"create", "find-owned", "wait", "capture:destroy", "find-owned", "find-owned-pvcs", "delete-cluster", "find-owned", "find-owned-pvcs", "delete-pvcs", "find-owned-pvcs"}; !reflect.DeepEqual(got, want) {
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

func TestControllerCleanupDoesNotDeletePVCsWhenClusterDeleteFails(t *testing.T) {
	wantErr := errors.New("cluster delete denied")
	client := &fakeLifecycleClient{
		ownedCluster:     OwnedCluster{Found: true, Name: "verify-cluster"},
		ownedPVCs:        OwnedPVCs{Items: []OwnedPVC{{Name: "verify-cluster-1"}}},
		deleteClusterErr: wantErr,
	}
	controller := Controller{
		Spec:    testVerifyClusterSpec(t),
		Client:  client,
		created: true,
		Options: LifecycleOptions{CleanupPVC: true},
	}

	_, err := controller.Destroy(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("Destroy() error = %v, want cluster delete error", err)
	}
	if got, want := client.calls, []string{"find-owned", "find-owned-pvcs", "delete-cluster"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("PVC cleanup must not run after cluster delete error: got %#v want %#v", got, want)
	}
	if !controller.created {
		t.Fatal("failed cleanup must retain possible ownership state")
	}
}

func TestControllerCleanupDoesNotDeletePVCsWhileOwnedClusterRemains(t *testing.T) {
	spec := testVerifyClusterSpec(t)
	client := &fakeLifecycleClient{
		ownedCluster:           OwnedCluster{Found: true, Name: spec.Name},
		ownedPVCs:              OwnedPVCs{Items: []OwnedPVC{{Name: spec.Name + "-1"}}},
		keepClusterAfterDelete: true,
	}
	controller := Controller{
		Spec:    spec,
		Client:  client,
		created: true,
		Options: LifecycleOptions{CleanupPVC: true},
	}

	_, err := controller.Destroy(context.Background())
	if err == nil || !strings.Contains(err.Error(), "owned cluster") {
		t.Fatalf("Destroy() error = %v, want remaining cluster error", err)
	}
	if got, want := client.calls, []string{"find-owned", "find-owned-pvcs", "delete-cluster", "find-owned"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("PVC cleanup must wait for observed cluster absence: got %#v want %#v", got, want)
	}
	if !controller.created {
		t.Fatal("incomplete cluster cleanup must retain possible ownership state")
	}
}

func TestControllerCleanupRejectsReplacedClusterBeforeDelete(t *testing.T) {
	spec := testVerifyClusterSpec(t)
	bound := ownedClusterForSpec(spec, "original-cluster-uid")
	replacement := ownedClusterForSpec(spec, "replacement-cluster-uid")
	client := &fakeLifecycleClient{ownedCluster: replacement}
	controller := Controller{
		Spec:      spec,
		Client:    client,
		Artifacts: artifact.NewMemoryStore(),
		created:   true,
		cluster:   bound,
	}

	_, err := controller.Destroy(context.Background())
	if err == nil || !strings.Contains(err.Error(), "cluster identity changed") {
		t.Fatalf("Destroy() error = %v, want replacement refusal", err)
	}
	if got, want := client.calls, []string{"find-owned"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("replacement cleanup calls = %#v, want %#v", got, want)
	}
	if !controller.created {
		t.Fatal("replacement refusal must retain unresolved cleanup state")
	}
}

func TestControllerCleanupFailsWhenPVCDeleteFailsAfterClusterAbsence(t *testing.T) {
	wantErr := errors.New("PVC delete timed out")
	spec := testVerifyClusterSpec(t)
	client := &fakeLifecycleClient{
		ownedCluster:  OwnedCluster{Found: true, Name: spec.Name},
		ownedPVCs:     OwnedPVCs{Items: []OwnedPVC{{Name: spec.Name + "-1"}}},
		deletePVCsErr: wantErr,
	}
	controller := Controller{
		Spec:    spec,
		Client:  client,
		created: true,
		Options: LifecycleOptions{CleanupPVC: true},
	}

	_, err := controller.Destroy(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("Destroy() error = %v, want PVC delete error", err)
	}
	if got, want := client.calls, []string{"find-owned", "find-owned-pvcs", "delete-cluster", "find-owned", "find-owned-pvcs", "delete-pvcs"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cleanup calls = %#v, want %#v", got, want)
	}
	if !controller.created {
		t.Fatal("failed PVC cleanup must retain possible ownership state")
	}
}

func TestControllerCleanupFailsWhenOwnedPVCRemains(t *testing.T) {
	spec := testVerifyClusterSpec(t)
	client := &fakeLifecycleClient{
		ownedCluster:        OwnedCluster{Found: true, Name: spec.Name},
		ownedPVCs:           OwnedPVCs{Items: []OwnedPVC{{Name: spec.Name + "-1"}}},
		keepPVCsAfterDelete: true,
	}
	controller := Controller{
		Spec:    spec,
		Client:  client,
		created: true,
		Options: LifecycleOptions{CleanupPVC: true},
	}

	_, err := controller.Destroy(context.Background())
	if err == nil || !strings.Contains(err.Error(), "1 owned PVCs still exist") {
		t.Fatalf("Destroy() error = %v, want remaining PVC error", err)
	}
	if got, want := client.calls, []string{"find-owned", "find-owned-pvcs", "delete-cluster", "find-owned", "find-owned-pvcs", "delete-pvcs", "find-owned-pvcs"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cleanup calls = %#v, want %#v", got, want)
	}
	if !controller.created {
		t.Fatal("incomplete PVC cleanup must retain possible ownership state")
	}
}

func TestControllerCleanupAcceptsPVCsRemovedWithCluster(t *testing.T) {
	spec := testVerifyClusterSpec(t)
	client := &fakeLifecycleClient{
		ownedCluster:          OwnedCluster{Found: true, Name: spec.Name},
		ownedPVCs:             OwnedPVCs{Items: []OwnedPVC{{Name: spec.Name + "-1"}}},
		removePVCsWithCluster: true,
	}
	controller := Controller{
		Spec:    spec,
		Client:  client,
		created: true,
		Options: LifecycleOptions{CleanupPVC: true},
	}

	if _, err := controller.Destroy(context.Background()); err != nil {
		t.Fatalf("Destroy() error = %v", err)
	}
	if got, want := client.calls, []string{"find-owned", "find-owned-pvcs", "delete-cluster", "find-owned", "find-owned-pvcs"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cleanup calls = %#v, want %#v", got, want)
	}
	if controller.created {
		t.Fatal("cluster-cascaded PVC cleanup must release ownership state")
	}
}

type fakeLifecycleClient struct {
	calls                  []string
	manifest               []byte
	waitOptions            WaitOptions
	captureOptions         CaptureOptions
	instance               Instance
	createErr              error
	createEvidence         []model.EvidenceRecord
	ownedCluster           OwnedCluster
	ownedPVCs              OwnedPVCs
	findErr                error
	findPVCsErr            error
	waitErr                error
	waitHook               func() error
	captureContextErr      error
	deleteContextErr       error
	deleteClusterErr       error
	deletePVCsErr          error
	keepClusterAfterDelete bool
	keepPVCsAfterDelete    bool
	removePVCsWithCluster  bool
	evidenceSequence       int
}

type failingArtifactSink struct {
	err error
}

func (s failingArtifactSink) Put(context.Context, model.ArtifactMetadata, io.Reader) (model.ArtifactRef, error) {
	return model.ArtifactRef{}, s.err
}

func (c *fakeLifecycleClient) FindOwnedCluster(_ context.Context, _ VerifyClusterSpec) (OwnedCluster, []model.EvidenceRecord, error) {
	c.calls = append(c.calls, "find-owned")
	return c.ownedCluster, []model.EvidenceRecord{c.testEvidence("find-owned")}, c.findErr
}

func (c *fakeLifecycleClient) FindOwnedPVCs(_ context.Context, _ VerifyClusterSpec) (OwnedPVCs, []model.EvidenceRecord, error) {
	c.calls = append(c.calls, "find-owned-pvcs")
	return c.ownedPVCs, []model.EvidenceRecord{c.testEvidence("find-owned-pvcs")}, c.findPVCsErr
}

func (c *fakeLifecycleClient) CreateCluster(_ context.Context, spec VerifyClusterSpec, manifest []byte) ([]model.EvidenceRecord, error) {
	c.calls = append(c.calls, "create")
	c.manifest = append([]byte{}, manifest...)
	evidence := c.createEvidence
	if c.createEvidence != nil {
		if createMayHaveSucceeded(evidence) {
			if !c.ownedCluster.Found {
				c.ownedCluster = ownedClusterForSpec(spec, "cluster-uid")
			}
			c.ensureOwnedPVC(spec)
		}
		return evidence, c.createErr
	}
	evidence = []model.EvidenceRecord{c.testEvidence("create")}
	if !c.ownedCluster.Found {
		c.ownedCluster = ownedClusterForSpec(spec, "cluster-uid")
	}
	c.ensureOwnedPVC(spec)
	return evidence, c.createErr
}

func (c *fakeLifecycleClient) ensureOwnedPVC(spec VerifyClusterSpec) {
	if len(c.ownedPVCs.Items) != 0 {
		return
	}
	c.ownedPVCs = OwnedPVCs{Items: []OwnedPVC{{
		Name:             spec.Name + "-1",
		UID:              "pvc-uid",
		OwnerClusterName: spec.Name,
		OwnerClusterUID:  c.ownedCluster.UID,
		ContractDigest:   c.ownedCluster.ContractDigest,
	}}}
}

func (c *fakeLifecycleClient) WaitForInstanceReady(_ context.Context, spec VerifyClusterSpec, opts WaitOptions) (Instance, []model.EvidenceRecord, error) {
	c.calls = append(c.calls, "wait")
	c.waitOptions = opts
	if c.instance.PodName == "" {
		c.instance.PodName = spec.InstancePodName
	}
	if c.instance.UID == "" {
		c.instance.UID = "pod-uid"
	}
	if c.instance.ClusterUID == "" {
		c.instance.ClusterUID = opts.Cluster.UID
	}
	if c.instance.ContractDigest == "" {
		c.instance.ContractDigest = opts.Cluster.ContractDigest
	}
	if c.waitHook != nil {
		return c.instance, []model.EvidenceRecord{c.testEvidence("wait")}, c.waitHook()
	}
	return c.instance, []model.EvidenceRecord{c.testEvidence("wait")}, c.waitErr
}

func (c *fakeLifecycleClient) CaptureEvidence(ctx context.Context, _ VerifyClusterSpec, _ Instance, opts CaptureOptions) ([]model.EvidenceRecord, error) {
	c.calls = append(c.calls, "capture:"+opts.Reason)
	c.captureOptions = opts
	c.captureContextErr = ctx.Err()
	return []model.EvidenceRecord{c.testEvidence("capture:" + opts.Reason)}, nil
}

func (c *fakeLifecycleClient) DeleteCluster(
	ctx context.Context,
	_ VerifyClusterSpec,
	_ OwnedCluster,
) ([]model.EvidenceRecord, error) {
	c.calls = append(c.calls, "delete-cluster")
	c.deleteContextErr = ctx.Err()
	if c.deleteClusterErr == nil && !c.keepClusterAfterDelete {
		c.ownedCluster = OwnedCluster{}
		if c.removePVCsWithCluster {
			c.ownedPVCs = OwnedPVCs{}
		}
	}
	return []model.EvidenceRecord{c.testEvidence("delete-cluster")}, c.deleteClusterErr
}

func (c *fakeLifecycleClient) DeletePVCs(
	ctx context.Context,
	_ VerifyClusterSpec,
	_ OwnedCluster,
	_ OwnedPVCs,
) ([]model.EvidenceRecord, error) {
	c.calls = append(c.calls, "delete-pvcs")
	if c.deleteContextErr == nil {
		c.deleteContextErr = ctx.Err()
	}
	if c.deletePVCsErr == nil && !c.keepPVCsAfterDelete {
		c.ownedPVCs = OwnedPVCs{}
	}
	return []model.EvidenceRecord{c.testEvidence("delete-pvcs")}, c.deletePVCsErr
}

func (c *fakeLifecycleClient) testEvidence(operation string) model.EvidenceRecord {
	c.evidenceSequence++
	record := testEvidence(operation)
	record.ID += ":" + strconv.Itoa(c.evidenceSequence)
	record.CollectedAt = record.CollectedAt.Add(time.Duration(c.evidenceSequence) * time.Second)
	return record
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

func ownedClusterForSpec(spec VerifyClusterSpec, uid string) OwnedCluster {
	digest, err := spec.ContractDigest()
	if err != nil {
		panic(err)
	}
	return OwnedCluster{
		Found:          true,
		Name:           spec.Name,
		UID:            uid,
		ContractDigest: digest,
	}
}

func testOwnedCluster(t *testing.T, spec VerifyClusterSpec) OwnedCluster {
	t.Helper()
	return ownedClusterForSpec(spec, "cluster-uid")
}

func testReadyPodJSON(t *testing.T, spec VerifyClusterSpec, cluster OwnedCluster) string {
	t.Helper()
	return fmt.Sprintf(
		`{"metadata":{"name":%q,"uid":"pod-uid","labels":{"cnpg.io/cluster":%q,%q:%q},"annotations":{"cnpg.io/operatorVersion":"1.26.3",%q:%q},"ownerReferences":[{"apiVersion":"postgresql.cnpg.io/v1","kind":"Cluster","name":%q,"uid":%q}]},"status":{"conditions":[{"type":"Ready","status":"True"}]}}`,
		spec.InstancePodName,
		spec.Name,
		labelOwnershipID,
		spec.OwnershipID,
		annotationRecoveryContract,
		cluster.ContractDigest,
		cluster.Name,
		cluster.UID,
	)
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
