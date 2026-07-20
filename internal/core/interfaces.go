package core

import (
	"context"

	"github.com/r314tive/pgdrill/internal/model"
)

type BackupProvider interface {
	Type() model.ProviderType
	DiscoverBackups(ctx context.Context) (model.BackupCatalog, error)
	ValidateCatalog(ctx context.Context, catalog model.BackupCatalog, backup model.Backup, target model.RecoveryTarget) (model.CheckReport, error)
	PlanRestore(ctx context.Context, backup model.Backup, target model.RecoveryTarget, spec model.TargetSpec) (model.RestorePlan, error)
}

type RestoreTarget interface {
	Type() model.RestoreTargetType
	Prepare(ctx context.Context, spec model.TargetSpec) error
	Execute(ctx context.Context, step model.RestoreStep) ([]model.EvidenceRecord, error)
	StartPostgres(ctx context.Context, cfg model.RuntimeConfig) (model.RunningPostgres, []model.EvidenceRecord, error)
	Destroy(ctx context.Context) ([]model.EvidenceRecord, error)
}

type Probe interface {
	Type() model.ProbeType
	Run(ctx context.Context, pg model.RunningPostgres) (model.CheckReport, error)
}

type EvidenceSink interface {
	Write(ctx context.Context, result model.DrillResult) error
}

type Preflight interface {
	Check(ctx context.Context) (model.CheckReport, error)
}
