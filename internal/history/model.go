package history

import (
	"fmt"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
)

const (
	PreGACompatibilityFloor      = "v0.3.0-alpha.1"
	CurrentStoreSchemaVersion    = "pgdrill.history-store/v1"
	CurrentRunSchemaVersion      = "pgdrill.history-run/v1"
	CurrentAttemptSchemaVersion  = "pgdrill.history-attempt/v1"
	CurrentSummarySchemaVersion  = "pgdrill.history-summary/v1"
	CurrentViewSchemaVersion     = "pgdrill.history-view/v1"
	CurrentRetentionPlanSchema   = "pgdrill.history-retention-plan/v1"
	CurrentPruneResultSchema     = "pgdrill.history-prune-result/v1"
	CurrentVerificationSchema    = "pgdrill.history-verification/v1"
	CurrentMigrationPlanSchema   = "pgdrill.history-migration-plan/v1"
	CurrentMigrationResultSchema = "pgdrill.history-migration-result/v1"

	LegacyStoreSchemaVersion   = "pgdrill.history-store/v1alpha1"
	LegacyRunSchemaVersion     = "pgdrill.history-run/v1alpha1"
	LegacyAttemptSchemaVersion = "pgdrill.history-attempt/v1alpha1"
	LegacySummarySchemaVersion = "pgdrill.history-summary/v1alpha1"

	CurrentLayoutVersion = 1
	MaxRuns              = 10_000
	MaxAttemptsPerRun    = 1_000
	MaxTotalAttempts     = 10_000
	MaxEventsPerAttempt  = 4_096
	MaxEventsPerRun      = 100_000
	MaxIdentityBytes     = 64 << 10
	MaxSpecBytes         = 1 << 20
	MaxEventBytes        = 1 << 20
	MaxReportBytes       = 64 << 20
	MaxAttemptEventBytes = 64 << 20
	MaxRunEventBytes     = 256 << 20
	MaxRunReportBytes    = 256 << 20
	MaxMigrationFiles    = 250_000
)

type StoreMetadata struct {
	SchemaVersion string `json:"schema_version"`
	LayoutVersion int    `json:"layout_version"`
}

type RunIdentity struct {
	SchemaVersion string `json:"schema_version"`
	RunID         string `json:"run_id"`
	SpecDigest    string `json:"spec_digest"`
}

type AttemptIdentity struct {
	SchemaVersion string `json:"schema_version"`
	RunID         string `json:"run_id"`
	AttemptID     string `json:"attempt_id"`
	SpecDigest    string `json:"spec_digest"`
}

type RunRecord struct {
	SchemaVersion string           `json:"schema_version"`
	RunID         string           `json:"run_id"`
	SpecDigest    string           `json:"spec_digest"`
	Spec          *model.DrillSpec `json:"spec,omitempty"`
	Attempts      []AttemptRecord  `json:"attempts"`
}

type AttemptRecord struct {
	AttemptID string             `json:"attempt_id"`
	Events    []model.RunEvent   `json:"events"`
	Report    *model.DrillResult `json:"report,omitempty"`
}

type AttemptSummary struct {
	RunID           string            `json:"run_id"`
	AttemptID       string            `json:"attempt_id"`
	SpecDigest      string            `json:"spec_digest"`
	Status          model.DrillStatus `json:"status"`
	FailureStage    model.DrillStage  `json:"failure_stage,omitempty"`
	StartedAt       time.Time         `json:"started_at,omitempty"`
	FinishedAt      time.Time         `json:"finished_at,omitempty"`
	EventCount      int               `json:"event_count"`
	ReportAvailable bool              `json:"report_available"`
	ArtifactCount   int               `json:"artifact_count"`
	EvidenceCount   int               `json:"evidence_count"`
	BlockingPolicy  int               `json:"blocking_policy_verdicts"`
}

type attemptSummaryIndex struct {
	SchemaVersion string         `json:"schema_version"`
	Summary       AttemptSummary `json:"summary"`
}

func (m StoreMetadata) validate() error {
	if m.SchemaVersion != CurrentStoreSchemaVersion &&
		m.SchemaVersion != LegacyStoreSchemaVersion {
		return fmt.Errorf(
			"history store schema_version %q is unsupported; expected %q or %q",
			m.SchemaVersion,
			CurrentStoreSchemaVersion,
			LegacyStoreSchemaVersion,
		)
	}
	if m.LayoutVersion != CurrentLayoutVersion {
		return fmt.Errorf("history store layout_version %d is unsupported; expected %d", m.LayoutVersion, CurrentLayoutVersion)
	}
	return nil
}

func (i RunIdentity) validate() error {
	if i.SchemaVersion != CurrentRunSchemaVersion &&
		i.SchemaVersion != LegacyRunSchemaVersion {
		return fmt.Errorf(
			"run identity schema_version must be %q or %q",
			CurrentRunSchemaVersion,
			LegacyRunSchemaVersion,
		)
	}
	if err := validateIdentityText("run_id", i.RunID); err != nil {
		return err
	}
	if !model.IsSHA256Digest(i.SpecDigest) {
		return fmt.Errorf("run identity spec_digest must be a sha256 digest")
	}
	return nil
}

func (i AttemptIdentity) validate() error {
	if i.SchemaVersion != CurrentAttemptSchemaVersion &&
		i.SchemaVersion != LegacyAttemptSchemaVersion {
		return fmt.Errorf(
			"attempt identity schema_version must be %q or %q",
			CurrentAttemptSchemaVersion,
			LegacyAttemptSchemaVersion,
		)
	}
	if err := validateIdentityText("run_id", i.RunID); err != nil {
		return err
	}
	if err := validateIdentityText("attempt_id", i.AttemptID); err != nil {
		return err
	}
	if !model.IsSHA256Digest(i.SpecDigest) {
		return fmt.Errorf("attempt identity spec_digest must be a sha256 digest")
	}
	return nil
}

func (i attemptSummaryIndex) validate(identity AttemptIdentity) error {
	if i.SchemaVersion != CurrentSummarySchemaVersion &&
		i.SchemaVersion != LegacySummarySchemaVersion {
		return fmt.Errorf(
			"attempt summary schema_version must be %q or %q",
			CurrentSummarySchemaVersion,
			LegacySummarySchemaVersion,
		)
	}
	summary := i.Summary
	if summary.RunID != identity.RunID || summary.AttemptID != identity.AttemptID || summary.SpecDigest != identity.SpecDigest {
		return fmt.Errorf("attempt summary identity does not match attempt %q", identity.AttemptID)
	}
	if summary.Status != model.DrillStatusUnknown && !summary.Status.IsTerminal() {
		return fmt.Errorf("attempt summary has unsupported status %q", summary.Status)
	}
	if summary.ReportAvailable && !summary.Status.IsTerminal() {
		return fmt.Errorf("attempt summary with report requires terminal status")
	}
	if summary.FailureStage != "" && !summary.FailureStage.IsKnown() {
		return fmt.Errorf("attempt summary has unsupported failure stage %q", summary.FailureStage)
	}
	if summary.EventCount < 0 || summary.EventCount > MaxEventsPerAttempt {
		return fmt.Errorf("attempt summary event_count is out of bounds")
	}
	if summary.ArtifactCount < 0 || summary.EvidenceCount < 0 || summary.BlockingPolicy < 0 {
		return fmt.Errorf("attempt summary counts must not be negative")
	}
	if !summary.StartedAt.IsZero() && !summary.FinishedAt.IsZero() && summary.FinishedAt.Before(summary.StartedAt) {
		return fmt.Errorf("attempt summary finished_at must not precede started_at")
	}
	return nil
}

func validateIdentityText(field, value string) error {
	return model.ValidateIdentity(field, value)
}
