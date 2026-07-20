package adapters

import (
	"context"
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
	})
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	if provider.Type() != model.ProviderPGProbackup {
		t.Fatalf("unexpected provider type %q", provider.Type())
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
