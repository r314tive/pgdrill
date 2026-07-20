package adapters

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/config"
	"github.com/r314tive/pgdrill/internal/model"
)

func TestNewProviderBuildsPGProbackupAdapter(t *testing.T) {
	provider, err := NewProvider(config.ProviderConfig{
		Type:      model.ProviderPGProbackup,
		BackupDir: "/srv/pg_probackup",
		Instance:  "main",
	}, config.RestoreConfig{
		VerifyBackup: config.VerifyBackupConfig{Enabled: true, Profile: "strict"},
	})
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	if provider.Type() != model.ProviderPGProbackup {
		t.Fatalf("unexpected provider type %q", provider.Type())
	}
	plan, err := provider.PlanRestore(context.Background(), model.Backup{
		Provider:    model.ProviderPGProbackup,
		ProviderID:  "main/SBOL94",
		ClusterName: "main",
	}, model.RecoveryTarget{Type: model.RecoveryTargetLatest}, model.TargetSpec{
		Type:    model.RestoreTargetLocal,
		WorkDir: "/tmp/pgdrill/main",
	})
	if err != nil {
		t.Fatalf("plan restore: %v", err)
	}
	if len(plan.Steps) != 2 || plan.Steps[1].Name != "pg-verifybackup" {
		t.Fatalf("expected mapped pg_verifybackup step, got %#v", plan.Steps)
	}
}

func TestNewProviderKeepsRepositoryAndRestoreTimeoutsSeparate(t *testing.T) {
	provider, err := NewProvider(config.ProviderConfig{
		Type:    model.ProviderWALG,
		Timeout: config.Duration{Duration: 2 * time.Minute},
	}, config.RestoreConfig{
		Timeout: config.Duration{Duration: 6 * time.Hour},
	})
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}

	plan, err := provider.PlanRestore(context.Background(), model.Backup{
		ID:         "wal-g:base_1",
		Provider:   model.ProviderWALG,
		ProviderID: "base_1",
	}, model.RecoveryTarget{Type: model.RecoveryTargetLatest}, model.TargetSpec{
		Type:    model.RestoreTargetLocal,
		WorkDir: "/tmp/pgdrill/main",
	})
	if err != nil {
		t.Fatalf("plan restore: %v", err)
	}
	if len(plan.Steps) == 0 || plan.Steps[0].Command == nil || plan.Steps[0].Command.Timeout != "6h0m0s" {
		t.Fatalf("unexpected restore timeout in plan %#v", plan.Steps)
	}
}

func TestNewProviderRejectsInvalidSemanticConfig(t *testing.T) {
	tests := []struct {
		name     string
		provider config.ProviderConfig
		restore  config.RestoreConfig
		want     string
	}{
		{
			name:     "barman server",
			provider: config.ProviderConfig{Type: model.ProviderBarman},
			want:     "provider.server is required",
		},
		{
			name: "pgbackrest verify output",
			provider: config.ProviderConfig{
				Type:             model.ProviderPGBackRest,
				PGBackRestVerify: config.PGBackRestVerifyConfig{Output: "json"},
			},
			want: "unsupported provider.pgbackrest_verify.output",
		},
		{
			name:     "pgprobackup directory",
			provider: config.ProviderConfig{Type: model.ProviderPGProbackup},
			want:     "provider.backup_dir is required",
		},
		{
			name: "pgprobackup threads",
			provider: config.ProviderConfig{
				Type:                model.ProviderPGProbackup,
				BackupDir:           "/backups",
				PGProbackupValidate: config.PGProbackupValidateConfig{Threads: -1},
			},
			want: "threads must not be negative",
		},
		{
			name:     "verify backup profile",
			provider: config.ProviderConfig{Type: model.ProviderWALG},
			restore: config.RestoreConfig{VerifyBackup: config.VerifyBackupConfig{
				Profile: "future",
			}},
			want: "unsupported pg_verifybackup profile",
		},
		{
			name:     "verify backup format",
			provider: config.ProviderConfig{Type: model.ProviderWALG},
			restore: config.RestoreConfig{VerifyBackup: config.VerifyBackupConfig{
				Format: "json",
			}},
			want: "unsupported pg_verifybackup format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewProvider(tt.provider, tt.restore)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected error containing %q, got %v", tt.want, err)
			}
		})
	}
}
