package cnpgverify

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/command"
	"github.com/r314tive/pgdrill/internal/config"
	"github.com/r314tive/pgdrill/internal/model"
	"github.com/r314tive/pgdrill/internal/report"
)

func TestIDPreservesExplicitValueAndUsesNanoseconds(t *testing.T) {
	startedAt := time.Date(2026, 7, 20, 12, 34, 56, 123456789, time.UTC)

	if got, want := ID("explicit-id", startedAt), "explicit-id"; got != want {
		t.Fatalf("ID() = %q, want %q", got, want)
	}
	if got, want := ID("", startedAt), "target-verify-20260720T123456.123456789Z"; got != want {
		t.Fatalf("ID() = %q, want %q", got, want)
	}
	if first, second := ID("", startedAt), ID("", startedAt.Add(time.Nanosecond)); first == second {
		t.Fatalf("generated IDs must distinguish concurrent starts, both were %q", first)
	}
}

func TestAppendCheckReportPreservesAllSections(t *testing.T) {
	destination := model.CheckReport{
		Checks:    []model.Check{{Name: "preflight"}},
		Evidence:  []model.EvidenceRecord{{ID: "preflight-evidence"}},
		Artifacts: []model.ArtifactRef{{ID: "sha256:" + strings.Repeat("a", 64)}},
	}
	appendCheckReport(&destination, model.CheckReport{
		Checks:    []model.Check{{Name: "probe"}},
		Evidence:  []model.EvidenceRecord{{ID: "probe-evidence"}},
		Artifacts: []model.ArtifactRef{{ID: "sha256:" + strings.Repeat("b", 64)}},
	})

	if len(destination.Checks) != 2 ||
		destination.Checks[1].Name != "probe" ||
		len(destination.Evidence) != 2 ||
		destination.Evidence[1].ID != "probe-evidence" ||
		len(destination.Artifacts) != 2 ||
		destination.Artifacts[1].ID != "sha256:"+strings.Repeat("b", 64) {
		t.Fatalf("merged check report = %#v", destination)
	}
}

func TestBuildSpecRejectsCanonicalRedactionOverlap(t *testing.T) {
	const secret = "manifest-secret"
	cfg := config.Config{
		Target: config.TargetConfig{
			Kubernetes:   config.KubernetesTargetConfig{Namespace: "d003-db"},
			RedactValues: []string{secret},
		},
	}
	target := config.CNPGTargetConfig{
		SourceCluster: "altbox",
		BackupName:    "backup-" + secret,
		ImageName:     "ghcr.io/cloudnative-pg/postgresql:17.5",
	}

	_, err := BuildSpec(cfg, target, "drill", "", "")
	if err == nil || !strings.Contains(err.Error(), "target intent") {
		t.Fatalf("BuildSpec() error = %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("BuildSpec() error leaked configured value: %v", err)
	}
}

func TestServiceRequiresMutationConfirmation(t *testing.T) {
	runner := &successRunner{}
	_, err := (Service{Runner: runner}).Run(context.Background(), config.Config{}, Options{})

	if err == nil || !strings.Contains(err.Error(), "explicit create confirmation") {
		t.Fatalf("Run() error = %v, want confirmation error", err)
	}
	if runner.calls != 0 {
		t.Fatalf("runner was called %d times before confirmation", runner.calls)
	}
}

func TestServiceRequiresProbeBeforePreflight(t *testing.T) {
	runner := &successRunner{}
	_, err := (Service{Runner: runner}).Run(context.Background(), config.Config{
		Target: config.TargetConfig{Type: model.RestoreTargetKubernetes},
	}, Options{ConfirmCreate: true})

	if err == nil || !strings.Contains(err.Error(), "at least one post-restore probe") {
		t.Fatalf("Run() error = %v, want probe error", err)
	}
	if runner.calls != 0 {
		t.Fatalf("runner was called %d times for invalid probe configuration", runner.calls)
	}
}

func TestServicePersistsCheckpointFailureThroughManagedLifecycle(t *testing.T) {
	now := time.Date(2026, 7, 21, 8, 0, 0, 0, time.UTC)
	reportPath := filepath.Join(t.TempDir(), "report.json")
	wantErr := errors.New("checkpoint unavailable")
	cfg := config.Config{
		Cluster: config.ClusterConfig{Name: "altbox"},
		Target: config.TargetConfig{
			Type: model.RestoreTargetKubernetes,
			Kubernetes: config.KubernetesTargetConfig{
				Namespace:     "d003-db",
				KubectlBinary: "kubectl",
			},
			CNPG: config.CNPGTargetConfig{
				SourceCluster: "altbox",
				BackupName:    "altbox-backup-1",
				ImageName:     "ghcr.io/cloudnative-pg/postgresql:16",
			},
		},
		Recovery: config.RecoveryConfig{Target: model.RecoveryTargetLatest},
		Probes: []config.ProbeConfig{{
			Type:  model.ProbeSQL,
			Query: "select 1",
		}},
		Report: config.ReportConfig{Path: reportPath},
	}

	result, err := (Service{
		Runner:      &successRunner{now: now},
		Clock:       func() time.Time { return now },
		Checkpoints: failingCheckpointStore{err: wantErr},
	}).Run(context.Background(), cfg, Options{
		DrillID:       "ownership-failure",
		ConfirmCreate: true,
	})

	if !errors.Is(err, wantErr) {
		t.Fatalf("Run() error = %v, want checkpoint error", err)
	}
	if result.Status != model.DrillStatusFailed || result.Failure == nil || result.Failure.Stage != model.DrillStageTargetStart {
		t.Fatalf("unexpected result %#v", result)
	}
	stored, readErr := report.ReadJSONFile(reportPath)
	if readErr != nil {
		t.Fatalf("read report: %v", readErr)
	}
	if stored.ID != "ownership-failure" || stored.Status != model.DrillStatusFailed {
		t.Fatalf("unexpected stored result %#v", stored)
	}
	if len(stored.Checks) != 1 || stored.Checks[0].Name != "tool.kubectl" || stored.Checks[0].Status != model.CheckStatusPassed {
		t.Fatalf("unexpected preflight checks %#v", stored.Checks)
	}
}

func TestDiscoverInputsResolvesExactPluginRecoveryContract(t *testing.T) {
	cfg := config.Config{
		Target: config.TargetConfig{
			Type: model.RestoreTargetKubernetes,
			Kubernetes: config.KubernetesTargetConfig{
				Namespace: "d003-db",
			},
		},
	}
	target := config.CNPGTargetConfig{
		SourceCluster:  "altbox",
		RecoveryMethod: config.CNPGRecoveryPlugin,
	}
	runner := &discoveryRunner{
		backupJSON: `{
  "items": [{
    "metadata": {"name": "altbox-backup-1", "creationTimestamp": "2026-07-21T01:00:00Z"},
    "spec": {
      "cluster": {"name": "altbox"},
      "method": "plugin",
      "pluginConfiguration": {"name": "barman-cloud.cloudnative-pg.io"}
    },
    "status": {
      "phase": "completed",
      "backupId": "20260721T010203",
      "pluginMetadata": {"version": "0.13.0"}
    }
  }]
}`,
		clusterJSON: `{
  "spec": {
    "imageName": "ghcr.io/cloudnative-pg/postgresql:17.5",
    "plugins": [{
      "name": "barman-cloud.cloudnative-pg.io",
      "isWALArchiver": true,
      "parameters": {"barmanObjectName": "altbox-backups"}
    }]
  }
}`,
	}

	evidence, err := DiscoverInputs(context.Background(), cfg, &target, runner)
	if err != nil {
		t.Fatalf("DiscoverInputs() error = %v", err)
	}
	if target.BackupName != "altbox-backup-1" ||
		target.BackupID != "20260721T010203" ||
		target.ImageName != "ghcr.io/cloudnative-pg/postgresql:17.5" {
		t.Fatalf("unexpected discovered backup/image %#v", target)
	}
	if target.Plugin.Name != config.DefaultCNPGPluginName ||
		target.Plugin.ObjectStore != "altbox-backups" ||
		target.Plugin.ServerName != "altbox" ||
		target.Plugin.RecoverySource != config.DefaultCNPGRecoverySource {
		t.Fatalf("unexpected discovered plugin config %#v", target.Plugin)
	}
	if target.ObservedPluginVersion != "0.13.0" {
		t.Fatalf("unexpected observed plugin version %q", target.ObservedPluginVersion)
	}
	for _, operation := range []string{
		"kubectl-discover-cnpg-backups",
		"kubectl-discover-cnpg-source-plugin",
		"kubectl-discover-cnpg-source-image",
	} {
		if !hasEvidenceOperation(evidence, operation) {
			t.Fatalf("missing %s evidence: %#v", operation, evidence)
		}
	}
}

func TestDiscoverInputsRejectsConfiguredPluginBackupIDMismatch(t *testing.T) {
	const secret = "configured-different-id"
	cfg := config.Config{
		Target: config.TargetConfig{
			Type:         model.RestoreTargetKubernetes,
			Kubernetes:   config.KubernetesTargetConfig{Namespace: "d003-db"},
			RedactValues: []string{secret},
		},
	}
	target := config.CNPGTargetConfig{
		SourceCluster:  "altbox",
		RecoveryMethod: config.CNPGRecoveryPlugin,
		BackupName:     "altbox-backup-1",
		BackupID:       secret,
		ImageName:      "ghcr.io/cloudnative-pg/postgresql:17.5",
		Plugin: config.CNPGPluginConfig{
			ObjectStore: "altbox-backups",
			ServerName:  "altbox",
		},
	}
	runner := &discoveryRunner{
		backupJSON: `{
  "items": [{
    "metadata": {"name": "altbox-backup-1", "creationTimestamp": "2026-07-21T01:00:00Z"},
    "spec": {
      "cluster": {"name": "altbox"},
      "method": "plugin",
      "pluginConfiguration": {"name": "barman-cloud.cloudnative-pg.io"}
    },
    "status": {"phase": "completed", "backupId": "actual-backup-id"}
  }]
}`,
	}

	evidence, err := DiscoverInputs(context.Background(), cfg, &target, runner)
	if err == nil || !strings.Contains(err.Error(), "does not match configured backup_id") {
		t.Fatalf("DiscoverInputs() error = %v, want backup ID mismatch", err)
	}
	if strings.Contains(err.Error(), secret) ||
		!strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("DiscoverInputs() leaked configured backup ID: %v", err)
	}
	if !hasEvidenceOperation(evidence, "kubectl-discover-cnpg-backups") {
		t.Fatalf("backup mismatch lost discovery evidence %#v", evidence)
	}
}

func TestDiscoverInputsRejectsConfiguredPluginSourceMismatch(t *testing.T) {
	cfg := config.Config{
		Target: config.TargetConfig{
			Type:       model.RestoreTargetKubernetes,
			Kubernetes: config.KubernetesTargetConfig{Namespace: "d003-db"},
		},
	}
	target := config.CNPGTargetConfig{
		SourceCluster:  "altbox",
		RecoveryMethod: config.CNPGRecoveryPlugin,
		Plugin: config.CNPGPluginConfig{
			ObjectStore: "wrong-backups",
		},
	}
	runner := &discoveryRunner{
		backupJSON: `{
  "items": [{
    "metadata": {"name": "altbox-backup-1", "creationTimestamp": "2026-07-21T01:00:00Z"},
    "spec": {
      "cluster": {"name": "altbox"},
      "method": "plugin",
      "pluginConfiguration": {"name": "barman-cloud.cloudnative-pg.io"}
    },
    "status": {"phase": "completed", "backupId": "20260721T010203"}
  }]
}`,
		clusterJSON: `{
  "spec": {
    "plugins": [{
      "name": "barman-cloud.cloudnative-pg.io",
      "isWALArchiver": true,
      "parameters": {"barmanObjectName": "altbox-backups"}
    }]
  }
}`,
	}

	evidence, err := DiscoverInputs(context.Background(), cfg, &target, runner)
	if err == nil || !strings.Contains(err.Error(), "does not match configured object_store") {
		t.Fatalf("DiscoverInputs() error = %v, want object store mismatch", err)
	}
	if !hasEvidenceOperation(evidence, "kubectl-discover-cnpg-source-plugin") {
		t.Fatalf("plugin mismatch lost discovery evidence %#v", evidence)
	}
}

func TestBackupRetainsObservedPluginMetadata(t *testing.T) {
	got := backup(config.CNPGTargetConfig{
		SourceCluster:         "altbox",
		RecoveryMethod:        config.CNPGRecoveryPlugin,
		BackupName:            "altbox-backup-1",
		BackupID:              "20260721T010203",
		ObservedPluginVersion: "0.13.0",
		Plugin: config.CNPGPluginConfig{
			Name:        config.DefaultCNPGPluginName,
			ObjectStore: "altbox-backups",
			ServerName:  "altbox",
		},
	}, "verify-altbox")

	for key, want := range map[string]string{
		"cnpg_backup_id":           "20260721T010203",
		"cnpg_plugin":              config.DefaultCNPGPluginName,
		"cnpg_plugin_object_store": "altbox-backups",
		"cnpg_plugin_server":       "altbox",
		"cnpg_plugin_version":      "0.13.0",
		"cnpg_recovery_method":     string(config.CNPGRecoveryPlugin),
	} {
		if got.Metadata[key] != want {
			t.Fatalf("backup metadata %s = %q, want %q", key, got.Metadata[key], want)
		}
	}
}

type failingCheckpointStore struct {
	err error
}

func (s failingCheckpointStore) Save(context.Context, model.OperationCheckpoint) error {
	return s.err
}

func (s failingCheckpointStore) Load(context.Context, model.Operation) (model.OperationCheckpoint, bool, error) {
	return model.OperationCheckpoint{}, false, s.err
}

func (s failingCheckpointStore) List(context.Context, model.AttemptIdentity) ([]model.OperationCheckpoint, error) {
	return nil, s.err
}

type successRunner struct {
	now   time.Time
	calls int
}

type discoveryRunner struct {
	backupJSON  string
	clusterJSON string
}

func (r *discoveryRunner) Run(_ context.Context, inv command.Invocation) (command.Result, error) {
	stdout := r.clusterJSON
	if strings.Contains(strings.Join(inv.Args, " "), "backups.postgresql.cnpg.io") {
		stdout = r.backupJSON
	}
	now := time.Date(2026, 7, 21, 8, 0, 0, 0, time.UTC)
	return command.Result{
		Raw: command.RawEvidence{
			Path:   inv.Path,
			Args:   append([]string{}, inv.Args...),
			Stdout: []byte(stdout),
		},
		Evidence: model.CommandEvidence{
			Path:       inv.Path,
			Args:       append([]string{}, inv.Args...),
			StartedAt:  now,
			FinishedAt: now,
			ExitStatus: model.ExitStatus{Started: true, Exited: true, Success: true},
			Stdout:     stdout,
		},
	}, nil
}

func hasEvidenceOperation(evidence []model.EvidenceRecord, operation string) bool {
	for _, record := range evidence {
		if record.Attributes["operation"] == operation {
			return true
		}
	}
	return false
}

func (r *successRunner) Run(_ context.Context, inv command.Invocation) (command.Result, error) {
	r.calls++
	now := r.now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return command.Result{Evidence: model.CommandEvidence{
		Path:       inv.Path,
		Args:       append([]string{}, inv.Args...),
		StartedAt:  now,
		FinishedAt: now,
		ExitStatus: model.ExitStatus{Started: true, Exited: true, Success: true},
		Stdout:     `{"clientVersion":{"gitVersion":"v1.34.1"}}`,
	}}, nil
}
