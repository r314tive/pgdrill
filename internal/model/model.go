package model

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type ProviderType string

const (
	ProviderWALG        ProviderType = "wal-g"
	ProviderBarman      ProviderType = "barman"
	ProviderPGBackRest  ProviderType = "pgbackrest"
	ProviderPGProbackup ProviderType = "pg_probackup"
)

type RestoreTargetType string

const (
	RestoreTargetLocal      RestoreTargetType = "local"
	RestoreTargetContainer  RestoreTargetType = "container"
	RestoreTargetKubernetes RestoreTargetType = "kubernetes"
)

type RecoveryTargetType string

const (
	RecoveryTargetImmediate    RecoveryTargetType = "immediate"
	RecoveryTargetLatest       RecoveryTargetType = "latest"
	RecoveryTargetTimestamp    RecoveryTargetType = "timestamp"
	RecoveryTargetLSN          RecoveryTargetType = "lsn"
	RecoveryTargetXID          RecoveryTargetType = "xid"
	RecoveryTargetRestorePoint RecoveryTargetType = "restore_point"
)

type ProbeType string

const (
	ProbePGIsReady ProbeType = "pg_isready"
	ProbeSQL       ProbeType = "sql"
	ProbeAMCheck   ProbeType = "amcheck"
	ProbePGDump    ProbeType = "pg_dump"
)

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

type Overview struct {
	Providers          []ProviderType       `json:"providers"`
	RestoreTargets     []RestoreTargetType  `json:"restore_targets"`
	TargetCapabilities TargetCapabilities   `json:"target_capabilities"`
	RecoveryTargets    []RecoveryTargetType `json:"recovery_targets"`
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

type BackupStatus string

const (
	BackupStatusUnknown       BackupStatus = "unknown"
	BackupStatusAvailable     BackupStatus = "available"
	BackupStatusWaitingForWAL BackupStatus = "waiting_for_wal"
	BackupStatusRunning       BackupStatus = "running"
	BackupStatusFailed        BackupStatus = "failed"
	BackupStatusInvalid       BackupStatus = "invalid"
)

type WALRange struct {
	StartSegment string `json:"start_segment,omitempty"`
	EndSegment   string `json:"end_segment,omitempty"`
	StartLSN     string `json:"start_lsn,omitempty"`
	EndLSN       string `json:"end_lsn,omitempty"`
	Timeline     string `json:"timeline,omitempty"`
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
	parts := strings.Split(value, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("lsn recovery target value must use PostgreSQL X/Y hexadecimal format")
	}
	for _, part := range parts {
		if _, err := strconv.ParseUint(part, 16, 32); err != nil {
			return fmt.Errorf("lsn recovery target value must use PostgreSQL X/Y hexadecimal format: %w", err)
		}
	}
	return nil
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

type CheckStatus string

const (
	CheckStatusUnknown CheckStatus = "unknown"
	CheckStatusPassed  CheckStatus = "passed"
	CheckStatusFailed  CheckStatus = "failed"
	CheckStatusWarning CheckStatus = "warning"
	CheckStatusSkipped CheckStatus = "skipped"
)

type Check struct {
	Name        string            `json:"name"`
	Probe       ProbeType         `json:"probe,omitempty"`
	Status      CheckStatus       `json:"status"`
	Message     string            `json:"message,omitempty"`
	EvidenceIDs []string          `json:"evidence_ids,omitempty"`
	Attributes  map[string]string `json:"attributes,omitempty"`
}

type CheckReport struct {
	Checks   []Check          `json:"checks"`
	Evidence []EvidenceRecord `json:"evidence,omitempty"`
}

type DrillStatus string

const (
	DrillStatusUnknown DrillStatus = "unknown"
	DrillStatusPassed  DrillStatus = "passed"
	DrillStatusFailed  DrillStatus = "failed"
	DrillStatusAborted DrillStatus = "aborted"
)

const CurrentReportSchemaVersion = "pgdrill.report/v1alpha1"

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
		failure.Message = err.Error()
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

type DrillResult struct {
	SchemaVersion  string           `json:"schema_version"`
	PGDrillVersion string           `json:"pgdrill_version,omitempty"`
	ID             string           `json:"id"`
	Provider       ProviderType     `json:"provider"`
	Backup         Backup           `json:"backup"`
	Target         TargetSpec       `json:"target"`
	RecoveryTarget RecoveryTarget   `json:"recovery_target"`
	StartedAt      time.Time        `json:"started_at"`
	FinishedAt     time.Time        `json:"finished_at"`
	Status         DrillStatus      `json:"status"`
	Failure        *DrillFailure    `json:"failure,omitempty"`
	Checks         []Check          `json:"checks,omitempty"`
	Evidence       []EvidenceRecord `json:"evidence,omitempty"`
}

type EvidenceKind string

const (
	EvidenceCommand EvidenceKind = "command"
	EvidenceCheck   EvidenceKind = "check"
	EvidenceFile    EvidenceKind = "file"
	EvidencePlan    EvidenceKind = "plan"
	EvidenceRuntime EvidenceKind = "runtime"
)

type EvidenceRecord struct {
	ID          string            `json:"id"`
	Kind        EvidenceKind      `json:"kind"`
	Source      string            `json:"source"`
	CollectedAt time.Time         `json:"collected_at"`
	Command     *CommandEvidence  `json:"command,omitempty"`
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
