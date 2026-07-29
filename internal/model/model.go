package model

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type ProviderType string

const (
	ProviderWALG        ProviderType = "wal-g"
	ProviderBarman      ProviderType = "barman"
	ProviderPGBackRest  ProviderType = "pgbackrest"
	ProviderPGProbackup ProviderType = "pg_probackup"
)

func (p ProviderType) IsKnown() bool {
	switch p {
	case ProviderWALG, ProviderBarman, ProviderPGBackRest, ProviderPGProbackup:
		return true
	default:
		return false
	}
}

type RestoreTargetType string

const (
	RestoreTargetLocal      RestoreTargetType = "local"
	RestoreTargetContainer  RestoreTargetType = "container"
	RestoreTargetKubernetes RestoreTargetType = "kubernetes"
)

func (t RestoreTargetType) IsKnown() bool {
	switch t {
	case RestoreTargetLocal, RestoreTargetContainer, RestoreTargetKubernetes:
		return true
	default:
		return false
	}
}

type RecoveryTargetType string

const (
	RecoveryTargetImmediate    RecoveryTargetType = "immediate"
	RecoveryTargetLatest       RecoveryTargetType = "latest"
	RecoveryTargetTimestamp    RecoveryTargetType = "timestamp"
	RecoveryTargetLSN          RecoveryTargetType = "lsn"
	RecoveryTargetXID          RecoveryTargetType = "xid"
	RecoveryTargetRestorePoint RecoveryTargetType = "restore_point"
)

func (t RecoveryTargetType) IsKnown() bool {
	switch t {
	case RecoveryTargetImmediate,
		RecoveryTargetLatest,
		RecoveryTargetTimestamp,
		RecoveryTargetLSN,
		RecoveryTargetXID,
		RecoveryTargetRestorePoint:
		return true
	default:
		return false
	}
}

type ProbeType string

const (
	ProbePGIsReady ProbeType = "pg_isready"
	ProbeSQL       ProbeType = "sql"
	ProbeAMCheck   ProbeType = "amcheck"
	ProbePGDump    ProbeType = "pg_dump"
)

func (p ProbeType) IsKnown() bool {
	switch p {
	case ProbePGIsReady, ProbeSQL, ProbeAMCheck, ProbePGDump:
		return true
	default:
		return false
	}
}

type ToolType string

const (
	ToolWALG           ToolType = "wal-g"
	ToolBarman         ToolType = "barman"
	ToolPGBackRest     ToolType = "pgbackrest"
	ToolPGProbackup    ToolType = "pg_probackup"
	ToolPGVerifyBackup ToolType = "pg_verifybackup"
	ToolPGAMCheck      ToolType = "pg_amcheck"
	ToolPGDump         ToolType = "pg_dump"
	ToolPGIsReady      ToolType = "pg_isready"
	ToolPSQL           ToolType = "psql"
	ToolPostgres       ToolType = "postgres"
	ToolKubectl        ToolType = "kubectl"
)

func (t ToolType) IsKnown() bool {
	switch t {
	case ToolWALG,
		ToolBarman,
		ToolPGBackRest,
		ToolPGProbackup,
		ToolPGVerifyBackup,
		ToolPGAMCheck,
		ToolPGDump,
		ToolPGIsReady,
		ToolPSQL,
		ToolPostgres,
		ToolKubectl:
		return true
	default:
		return false
	}
}

type Overview struct {
	Providers          []ProviderType       `json:"providers"`
	RestoreTargets     []RestoreTargetType  `json:"restore_targets"`
	TargetCapabilities TargetCapabilities   `json:"target_capabilities"`
	RecoveryTargets    []RecoveryTargetType `json:"recovery_targets"`
	PolicyAssertions   []PolicyAssertion    `json:"policy_assertions"`
	Probes             []ProbeType          `json:"probes"`
	Tools              []ToolType           `json:"tools"`
}

type TargetCapabilities struct {
	Run      []RestoreTargetType `json:"run"`
	Manifest []RestoreTargetType `json:"manifest"`
	Verify   []RestoreTargetType `json:"verify"`
}

func ProjectOverview() Overview {
	return Overview{
		Providers: []ProviderType{
			ProviderWALG,
			ProviderBarman,
			ProviderPGBackRest,
			ProviderPGProbackup,
		},
		RestoreTargets: []RestoreTargetType{
			RestoreTargetLocal,
			RestoreTargetContainer,
			RestoreTargetKubernetes,
		},
		TargetCapabilities: TargetCapabilities{
			Run:      []RestoreTargetType{RestoreTargetLocal},
			Manifest: []RestoreTargetType{RestoreTargetKubernetes},
			Verify:   []RestoreTargetType{RestoreTargetKubernetes},
		},
		RecoveryTargets: []RecoveryTargetType{
			RecoveryTargetImmediate,
			RecoveryTargetLatest,
			RecoveryTargetTimestamp,
			RecoveryTargetLSN,
			RecoveryTargetXID,
			RecoveryTargetRestorePoint,
		},
		PolicyAssertions: RecoveryPolicyAssertions(),
		Probes: []ProbeType{
			ProbePGIsReady,
			ProbeSQL,
			ProbeAMCheck,
			ProbePGDump,
		},
		Tools: []ToolType{
			ToolWALG,
			ToolBarman,
			ToolPGBackRest,
			ToolPGProbackup,
			ToolPGVerifyBackup,
			ToolPGAMCheck,
			ToolPGDump,
			ToolPGIsReady,
			ToolPSQL,
			ToolPostgres,
			ToolKubectl,
		},
	}
}

type BackupKind string

const (
	BackupKindUnknown      BackupKind = "unknown"
	BackupKindFull         BackupKind = "full"
	BackupKindDifferential BackupKind = "differential"
	BackupKindIncremental  BackupKind = "incremental"
	BackupKindDelta        BackupKind = "delta"
	BackupKindLogical      BackupKind = "logical"
)

func (k BackupKind) IsKnown() bool {
	switch k {
	case BackupKindUnknown,
		BackupKindFull,
		BackupKindDifferential,
		BackupKindIncremental,
		BackupKindDelta,
		BackupKindLogical:
		return true
	default:
		return false
	}
}

type BackupStatus string

const (
	BackupStatusUnknown       BackupStatus = "unknown"
	BackupStatusAvailable     BackupStatus = "available"
	BackupStatusWaitingForWAL BackupStatus = "waiting_for_wal"
	BackupStatusRunning       BackupStatus = "running"
	BackupStatusFailed        BackupStatus = "failed"
	BackupStatusInvalid       BackupStatus = "invalid"
)

func (s BackupStatus) IsKnown() bool {
	switch s {
	case BackupStatusUnknown,
		BackupStatusAvailable,
		BackupStatusWaitingForWAL,
		BackupStatusRunning,
		BackupStatusFailed,
		BackupStatusInvalid:
		return true
	default:
		return false
	}
}

type WALRange struct {
	StartSegment string `json:"start_segment,omitempty"`
	EndSegment   string `json:"end_segment,omitempty"`
	StartLSN     string `json:"start_lsn,omitempty"`
	EndLSN       string `json:"end_lsn,omitempty"`
	Timeline     string `json:"timeline,omitempty"`
}

func (r WALRange) Validate() error {
	lsnValues := make(map[string]uint64, 2)
	for _, item := range []struct {
		field string
		value string
	}{
		{field: "wal_range.start_lsn", value: r.StartLSN},
		{field: "wal_range.end_lsn", value: r.EndLSN},
	} {
		if item.value == "" {
			continue
		}
		if err := (RecoveryTarget{Type: RecoveryTargetLSN, Value: item.value}).Validate(); err != nil {
			return fmt.Errorf("invalid %s: %w", item.field, err)
		}
		value, _ := parseLSN(item.value)
		lsnValues[item.field] = value
	}
	if start, startOK := lsnValues["wal_range.start_lsn"]; startOK {
		if end, endOK := lsnValues["wal_range.end_lsn"]; endOK && end < start {
			return fmt.Errorf("wal_range.end_lsn must not be earlier than wal_range.start_lsn")
		}
	}

	segments := make(map[string]uint32, 2)
	for _, item := range []struct {
		field string
		value string
	}{
		{field: "wal_range.start_segment", value: r.StartSegment},
		{field: "wal_range.end_segment", value: r.EndSegment},
	} {
		if item.value == "" {
			continue
		}
		timeline, err := parseWALSegment(item.value)
		if err != nil {
			return fmt.Errorf("invalid %s: %w", item.field, err)
		}
		segments[item.field] = timeline
	}
	if startTimeline, startOK := segments["wal_range.start_segment"]; startOK {
		if endTimeline, endOK := segments["wal_range.end_segment"]; endOK {
			if endTimeline != startTimeline {
				return fmt.Errorf("wal_range start and end segments must use the same timeline")
			}
			if strings.ToUpper(r.EndSegment) < strings.ToUpper(r.StartSegment) {
				return fmt.Errorf("wal_range.end_segment must not be earlier than wal_range.start_segment")
			}
		}
	}

	if r.Timeline != "" {
		if err := (RecoveryTarget{Type: RecoveryTargetLatest, Timeline: r.Timeline}).Validate(); err != nil {
			return fmt.Errorf("invalid wal_range.timeline: %w", err)
		}
		if r.Timeline != "latest" && r.Timeline != "current" {
			timeline, _ := strconv.ParseUint(r.Timeline, 10, 32)
			for field, segmentTimeline := range segments {
				if uint32(timeline) != segmentTimeline {
					return fmt.Errorf(
						"%s timeline %d does not match wal_range.timeline %s",
						field,
						segmentTimeline,
						r.Timeline,
					)
				}
			}
		}
	}
	return nil
}

func parseWALSegment(value string) (uint32, error) {
	if len(value) != 24 {
		return 0, fmt.Errorf("WAL segment must contain exactly 24 hexadecimal characters")
	}
	for offset := 0; offset < len(value); offset += 8 {
		if _, err := strconv.ParseUint(value[offset:offset+8], 16, 32); err != nil {
			return 0, fmt.Errorf("WAL segment must contain exactly 24 hexadecimal characters: %w", err)
		}
	}
	timeline, _ := strconv.ParseUint(value[:8], 16, 32)
	if timeline == 0 {
		return 0, fmt.Errorf("WAL segment timeline must be positive")
	}
	return uint32(timeline), nil
}

type Backup struct {
	ID                string            `json:"id"`
	Provider          ProviderType      `json:"provider"`
	ProviderID        string            `json:"provider_id"`
	ClusterName       string            `json:"cluster_name,omitempty"`
	ParentID          string            `json:"parent_id,omitempty"`
	Kind              BackupKind        `json:"kind"`
	Status            BackupStatus      `json:"status"`
	StartedAt         *time.Time        `json:"started_at,omitempty"`
	FinishedAt        *time.Time        `json:"finished_at,omitempty"`
	LastModifiedAt    *time.Time        `json:"last_modified_at,omitempty"`
	WALRange          WALRange          `json:"wal_range,omitempty"`
	PostgreSQLVersion string            `json:"postgresql_version,omitempty"`
	DataDirectory     string            `json:"data_directory,omitempty"`
	Hostname          string            `json:"hostname,omitempty"`
	Permanent         bool              `json:"permanent,omitempty"`
	Metadata          map[string]string `json:"metadata,omitempty"`
}

func (b Backup) ValidateRecoveryMetadata() error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "backup id", value: b.ID},
		{name: "backup provider_id", value: b.ProviderID},
		{name: "backup cluster_name", value: b.ClusterName},
		{name: "backup parent_id", value: b.ParentID},
	} {
		if field.value == "" {
			continue
		}
		if err := ValidateIdentity(field.name, field.value); err != nil {
			return err
		}
	}
	for _, field := range []struct {
		name     string
		value    string
		maxBytes int
	}{
		{name: "backup postgresql_version", value: b.PostgreSQLVersion, maxBytes: 256},
		{name: "backup data_directory", value: b.DataDirectory, maxBytes: MaxCommandPathBytes},
		{name: "backup hostname", value: b.Hostname, maxBytes: 1024},
	} {
		if strings.IndexByte(field.value, 0) >= 0 {
			return fmt.Errorf("%s must not contain NUL", field.name)
		}
		if err := validateBoundedUTF8(field.name, field.value, field.maxBytes); err != nil {
			return err
		}
	}
	if err := validateStringAttributes("backup metadata", b.Metadata); err != nil {
		return err
	}
	if b.StartedAt != nil && b.FinishedAt != nil && b.FinishedAt.Before(*b.StartedAt) {
		return fmt.Errorf("finished_at must not be earlier than started_at")
	}
	return b.WALRange.Validate()
}

func ProviderScopedID(provider ProviderType, providerID string) string {
	if providerID == "" {
		return string(provider)
	}
	return string(provider) + ":" + providerID
}

type BackupCatalog struct {
	Provider ProviderType     `json:"provider"`
	Backups  []Backup         `json:"backups"`
	Evidence []EvidenceRecord `json:"evidence,omitempty"`
}

type RecoveryTarget struct {
	Type      RecoveryTargetType `json:"type"`
	Value     string             `json:"value,omitempty"`
	Timeline  string             `json:"timeline,omitempty"`
	Inclusive *bool              `json:"inclusive,omitempty"`
}

func (t RecoveryTarget) Normalized() RecoveryTarget {
	t.Type = RecoveryTargetType(strings.TrimSpace(string(t.Type)))
	if t.Type == "" {
		t.Type = RecoveryTargetLatest
	}
	t.Value = strings.TrimSpace(t.Value)
	t.Timeline = strings.TrimSpace(t.Timeline)
	return t
}

func (t RecoveryTarget) Validate() error {
	t = t.Normalized()
	switch t.Type {
	case RecoveryTargetLatest, RecoveryTargetImmediate:
		if t.Value != "" {
			return fmt.Errorf("%s recovery target does not accept value", t.Type)
		}
	case RecoveryTargetTimestamp:
		if t.Value == "" {
			return fmt.Errorf("timestamp recovery target requires value")
		}
		if _, err := time.Parse(time.RFC3339Nano, t.Value); err != nil {
			return fmt.Errorf("timestamp recovery target value must be RFC3339 with timezone: %w", err)
		}
	case RecoveryTargetLSN:
		if t.Value == "" {
			return fmt.Errorf("lsn recovery target requires value")
		}
		if err := validateLSN(t.Value); err != nil {
			return err
		}
	case RecoveryTargetXID:
		if t.Value == "" {
			return fmt.Errorf("xid recovery target requires value")
		}
		if _, err := strconv.ParseUint(t.Value, 10, 32); err != nil {
			return fmt.Errorf("xid recovery target value must be an unsigned 32-bit decimal integer: %w", err)
		}
	case RecoveryTargetRestorePoint:
		if t.Value == "" {
			return fmt.Errorf("restore point recovery target requires value")
		}
	default:
		return fmt.Errorf("unsupported recovery target %q", t.Type)
	}

	if t.Inclusive != nil {
		switch t.Type {
		case RecoveryTargetTimestamp, RecoveryTargetLSN, RecoveryTargetXID:
		default:
			return fmt.Errorf("recovery target %q does not support inclusive", t.Type)
		}
	}
	if t.Timeline != "" && t.Timeline != "latest" && t.Timeline != "current" {
		timeline, err := strconv.ParseUint(t.Timeline, 10, 32)
		if err != nil || timeline == 0 {
			return fmt.Errorf("recovery target timeline must be latest, current, or a positive decimal timeline ID")
		}
	}
	return nil
}

func (t RecoveryTarget) Timestamp() (time.Time, error) {
	t = t.Normalized()
	if t.Type != RecoveryTargetTimestamp {
		return time.Time{}, fmt.Errorf("recovery target %q is not a timestamp", t.Type)
	}
	if err := t.Validate(); err != nil {
		return time.Time{}, err
	}
	return time.Parse(time.RFC3339Nano, t.Value)
}

func validateLSN(value string) error {
	_, err := parseLSN(value)
	return err
}

func parseLSN(value string) (uint64, error) {
	parts := strings.Split(value, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return 0, fmt.Errorf("lsn recovery target value must use PostgreSQL X/Y hexadecimal format")
	}
	values := [2]uint64{}
	for index, part := range parts {
		parsed, err := strconv.ParseUint(part, 16, 32)
		if err != nil {
			return 0, fmt.Errorf("lsn recovery target value must use PostgreSQL X/Y hexadecimal format: %w", err)
		}
		values[index] = parsed
	}
	return values[0]<<32 | values[1], nil
}

type TargetSpec struct {
	Type    RestoreTargetType `json:"type"`
	WorkDir string            `json:"work_dir,omitempty"`
	Labels  map[string]string `json:"labels,omitempty"`
}

type RuntimeConfig struct {
	DataDirectory  string            `json:"data_directory,omitempty"`
	Port           int               `json:"port,omitempty"`
	Environment    map[string]string `json:"environment,omitempty"`
	PostgresBinary string            `json:"postgres_binary,omitempty"`
}

type RunningPostgres struct {
	ConnString        string `json:"conn_string,omitempty"`
	DataDirectory     string `json:"data_directory,omitempty"`
	PostgreSQLVersion string `json:"postgresql_version,omitempty"`
	Host              string `json:"host,omitempty"`
	Port              int    `json:"port,omitempty"`
}

type CommandSpec struct {
	Tool       ToolType          `json:"tool,omitempty"`
	Path       string            `json:"path,omitempty"`
	Args       []string          `json:"args,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	WorkDir    string            `json:"work_dir,omitempty"`
	Timeout    string            `json:"timeout,omitempty"`
	Redactions []string          `json:"-"`
}

type FileSpec struct {
	Path    string `json:"path"`
	Content string `json:"-"`
	Mode    string `json:"mode,omitempty"`
	Append  bool   `json:"append,omitempty"`
}

type RestoreStep struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Command     *CommandSpec      `json:"command,omitempty"`
	Files       []FileSpec        `json:"files,omitempty"`
	Inputs      map[string]string `json:"inputs,omitempty"`
	Outputs     map[string]string `json:"outputs,omitempty"`
}

type RestorePlan struct {
	Provider       ProviderType     `json:"provider"`
	BackupID       string           `json:"backup_id"`
	Target         TargetSpec       `json:"target"`
	RecoveryTarget RecoveryTarget   `json:"recovery_target"`
	Steps          []RestoreStep    `json:"steps"`
	Runtime        RuntimeConfig    `json:"runtime,omitempty"`
	Evidence       []EvidenceRecord `json:"evidence,omitempty"`
}

const (
	MaxChecksPerReport              = 4096
	MaxEvidenceRecordsPerReport     = 4096
	MaxOperationsPerReport          = 1024
	MaxBackupsPerCatalog            = 10_000
	MaxCommandArguments             = 4096
	MaxCommandArgumentBytes         = 64 << 10
	MaxCommandEnvironmentEntries    = 4096
	MaxCommandEnvironmentNameBytes  = 1024
	MaxCommandEnvironmentValueBytes = 64 << 10
	MaxCommandPathBytes             = 4096
	MaxCommandErrorBytes            = 16 << 10
	MaxCommandEvidenceBytes         = 1 << 20
)

type CheckStatus string

const (
	CheckStatusUnknown CheckStatus = "unknown"
	CheckStatusPassed  CheckStatus = "passed"
	CheckStatusFailed  CheckStatus = "failed"
	CheckStatusWarning CheckStatus = "warning"
	CheckStatusSkipped CheckStatus = "skipped"
)

func (s CheckStatus) IsTerminal() bool {
	switch s {
	case CheckStatusPassed, CheckStatusFailed, CheckStatusWarning, CheckStatusSkipped:
		return true
	default:
		return false
	}
}

type Check struct {
	Name        string            `json:"name"`
	Probe       ProbeType         `json:"probe,omitempty"`
	Status      CheckStatus       `json:"status"`
	Message     string            `json:"message,omitempty"`
	EvidenceIDs []string          `json:"evidence_ids,omitempty"`
	Attributes  map[string]string `json:"attributes,omitempty"`
}

type CheckReport struct {
	Checks    []Check          `json:"checks"`
	Evidence  []EvidenceRecord `json:"evidence,omitempty"`
	Artifacts []ArtifactRef    `json:"artifacts,omitempty"`
}

type DrillStatus string

const (
	DrillStatusUnknown DrillStatus = "unknown"
	DrillStatusPassed  DrillStatus = "passed"
	DrillStatusFailed  DrillStatus = "failed"
	DrillStatusAborted DrillStatus = "aborted"
)

func (s DrillStatus) IsTerminal() bool {
	switch s {
	case DrillStatusPassed, DrillStatusFailed, DrillStatusAborted:
		return true
	default:
		return false
	}
}

const (
	CurrentReportSchemaVersion  = "pgdrill.report/v2"
	PreviousReportSchemaVersion = "pgdrill.report/v1"
	LegacyReportSchemaVersion   = "pgdrill.report/v1alpha1"
)

type DrillStage string

const (
	DrillStageRequestValidation DrillStage = "request_validation"
	DrillStagePreflight         DrillStage = "preflight"
	DrillStageBackupDiscovery   DrillStage = "backup_discovery"
	DrillStageBackupSelection   DrillStage = "backup_selection"
	DrillStageCatalogValidation DrillStage = "catalog_validation"
	DrillStageRestorePlanning   DrillStage = "restore_planning"
	DrillStageTargetPreparation DrillStage = "target_preparation"
	DrillStageRestoreExecution  DrillStage = "restore_execution"
	DrillStagePostgresStart     DrillStage = "postgres_start"
	DrillStageProbeExecution    DrillStage = "probe_execution"
	DrillStageTargetDiscovery   DrillStage = "target_discovery"
	DrillStageTargetStart       DrillStage = "target_start"
	DrillStageTargetCleanup     DrillStage = "target_cleanup"
	DrillStagePolicyEvaluation  DrillStage = "policy_evaluation"
	DrillStageReportWrite       DrillStage = "report_write"
)

func (s DrillStage) IsKnown() bool {
	switch s {
	case DrillStageRequestValidation,
		DrillStagePreflight,
		DrillStageBackupDiscovery,
		DrillStageBackupSelection,
		DrillStageCatalogValidation,
		DrillStageRestorePlanning,
		DrillStageTargetPreparation,
		DrillStageRestoreExecution,
		DrillStagePostgresStart,
		DrillStageProbeExecution,
		DrillStageTargetDiscovery,
		DrillStageTargetStart,
		DrillStageTargetCleanup,
		DrillStagePolicyEvaluation,
		DrillStageReportWrite:
		return true
	default:
		return false
	}
}

type DrillFailure struct {
	Stage       DrillStage `json:"stage"`
	Message     string     `json:"message"`
	EvidenceIDs []string   `json:"evidence_ids,omitempty"`
}

func NewDrillFailure(stage DrillStage, err error, evidence []EvidenceRecord) *DrillFailure {
	failure := &DrillFailure{Stage: stage}
	if err != nil {
		failure.Message = boundedUTF8Text(err.Error(), MaxFailureMessageBytes)
	}
	seen := map[string]struct{}{}
	for _, record := range evidence {
		if record.ID == "" {
			continue
		}
		if _, ok := seen[record.ID]; ok {
			continue
		}
		seen[record.ID] = struct{}{}
		failure.EvidenceIDs = append(failure.EvidenceIDs, record.ID)
	}
	return failure
}

func boundedUTF8Text(value string, maxBytes int) string {
	value = strings.ToValidUTF8(strings.TrimSpace(value), "\uFFFD")
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

type DrillResult struct {
	SchemaVersion    string                    `json:"schema_version"`
	PGDrillVersion   string                    `json:"pgdrill_version,omitempty"`
	ID               string                    `json:"id"`
	AttemptID        string                    `json:"attempt_id,omitempty"`
	SpecDigest       string                    `json:"spec_digest,omitempty"`
	Spec             *DrillSpec                `json:"spec,omitempty"`
	Cluster          string                    `json:"cluster,omitempty"`
	Provider         ProviderType              `json:"provider"`
	Backup           Backup                    `json:"backup"`
	Target           TargetSpec                `json:"target"`
	RecoveryTarget   RecoveryTarget            `json:"recovery_target"`
	StartedAt        time.Time                 `json:"started_at"`
	FinishedAt       time.Time                 `json:"finished_at"`
	Status           DrillStatus               `json:"status"`
	Failure          *DrillFailure             `json:"failure,omitempty"`
	Checks           []Check                   `json:"checks,omitempty"`
	Evidence         []EvidenceRecord          `json:"evidence,omitempty"`
	Artifacts        []ArtifactRef             `json:"artifacts,omitempty"`
	Operations       []OperationCheckpoint     `json:"operations,omitempty"`
	PolicyEvaluation *RecoveryPolicyEvaluation `json:"policy_evaluation,omitempty"`
}

type EvidenceKind string

const (
	EvidenceCommand EvidenceKind = "command"
	EvidenceCheck   EvidenceKind = "check"
	EvidenceFile    EvidenceKind = "file"
	EvidencePlan    EvidenceKind = "plan"
	EvidenceRuntime EvidenceKind = "runtime"
)

func (k EvidenceKind) IsKnown() bool {
	switch k {
	case EvidenceCommand, EvidenceCheck, EvidenceFile, EvidencePlan, EvidenceRuntime:
		return true
	default:
		return false
	}
}

type EvidenceRecord struct {
	ID          string            `json:"id"`
	Kind        EvidenceKind      `json:"kind"`
	Source      string            `json:"source"`
	CollectedAt time.Time         `json:"collected_at"`
	Command     *CommandEvidence  `json:"command,omitempty"`
	ArtifactIDs []string          `json:"artifact_ids,omitempty"`
	Attributes  map[string]string `json:"attributes,omitempty"`
}

type CommandEvidence struct {
	Path            string            `json:"path"`
	ResolvedPath    string            `json:"resolved_path,omitempty"`
	Args            []string          `json:"args,omitempty"`
	Env             map[string]string `json:"env,omitempty"`
	WorkDir         string            `json:"work_dir,omitempty"`
	StartedAt       time.Time         `json:"started_at"`
	FinishedAt      time.Time         `json:"finished_at"`
	DurationMillis  int64             `json:"duration_millis"`
	ExitStatus      ExitStatus        `json:"exit_status"`
	Stdout          string            `json:"stdout,omitempty"`
	StdoutBytes     int64             `json:"stdout_bytes,omitempty"`
	StdoutTruncated bool              `json:"stdout_truncated,omitempty"`
	Stderr          string            `json:"stderr,omitempty"`
	StderrBytes     int64             `json:"stderr_bytes,omitempty"`
	StderrTruncated bool              `json:"stderr_truncated,omitempty"`
}

type ExitStatus struct {
	Started  bool   `json:"started"`
	Exited   bool   `json:"exited"`
	Success  bool   `json:"success"`
	ExitCode int    `json:"exit_code"`
	TimedOut bool   `json:"timed_out,omitempty"`
	Canceled bool   `json:"canceled,omitempty"`
	Error    string `json:"error,omitempty"`
}

func (s ExitStatus) Summary() string {
	if !s.Started {
		if s.Error != "" {
			return "not started: " + s.Error
		}
		return "not started"
	}
	if s.TimedOut {
		return "timed out"
	}
	if s.Canceled {
		return "canceled"
	}
	if s.Success {
		return "success"
	}
	if s.Exited {
		return "exit code " + strconv.Itoa(s.ExitCode)
	}
	if s.Error != "" {
		return s.Error
	}
	return "failed"
}
