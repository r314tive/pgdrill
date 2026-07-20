package walg

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/r314tive/pgdrill/internal/command"
	"github.com/r314tive/pgdrill/internal/model"
	"github.com/r314tive/pgdrill/internal/restorechecks/pgverifybackup"
)

const defaultBinary = "wal-g"

type Config struct {
	Binary       string
	Env          map[string]string
	WorkDir      string
	Timeout      time.Duration
	RedactValues []string
	WALVerify    WALVerifyConfig
	VerifyBackup pgverifybackup.Config
}

type WALVerifyConfig struct {
	Enabled      bool
	Checks       []string
	BackupName   string
	LSN          string
	Timeline     string
	Timeout      time.Duration
	RedactValues []string
}

type Adapter struct {
	cfg    Config
	runner command.Runner
}

func New(cfg Config, runner command.Runner) *Adapter {
	if runner == nil {
		runner = command.NewRunner(command.Options{DefaultTimeout: cfg.Timeout})
	}
	return &Adapter{
		cfg:    cfg,
		runner: runner,
	}
}

func (a *Adapter) Type() model.ProviderType {
	return model.ProviderWALG
}

func (a *Adapter) DiscoverBackups(ctx context.Context) (model.BackupCatalog, error) {
	result, err := a.runner.Run(ctx, command.Invocation{
		Path:         a.binary(),
		Args:         []string{"backup-list", "--detail", "--json"},
		Env:          a.cfg.Env,
		WorkDir:      a.cfg.WorkDir,
		Timeout:      a.cfg.Timeout,
		RedactValues: a.cfg.RedactValues,
	})

	catalog := model.BackupCatalog{
		Provider: model.ProviderWALG,
		Evidence: []model.EvidenceRecord{
			commandEvidence(model.ProviderWALG, "backup-list", result.Evidence),
		},
	}
	if err != nil {
		return catalog, fmt.Errorf("run wal-g backup-list: %w", err)
	}
	if !result.Evidence.ExitStatus.Success {
		return catalog, fmt.Errorf("wal-g backup-list failed: %s", result.Evidence.ExitStatus.Summary())
	}

	backups, err := ParseBackupList(result.Raw.Stdout)
	if err != nil {
		return catalog, err
	}
	catalog.Backups = backups
	return catalog, nil
}

func (a *Adapter) ValidateCatalog(ctx context.Context, _ model.BackupCatalog, backup model.Backup, _ model.RecoveryTarget) (model.CheckReport, error) {
	if !a.cfg.WALVerify.Enabled {
		return model.CheckReport{
			Checks: []model.Check{{
				Name:    "wal-g-catalog-validation",
				Status:  model.CheckStatusSkipped,
				Message: "WAL-G wal-verify is not enabled; restore drill will continue with command evidence and post-restore probes.",
			}},
		}, nil
	}
	if backup.Provider != "" && backup.Provider != model.ProviderWALG {
		return model.CheckReport{}, fmt.Errorf("wal-g cannot validate backup from provider %q", backup.Provider)
	}

	args, err := a.walVerifyArgs(backup)
	if err != nil {
		return model.CheckReport{}, err
	}
	result, runErr := a.runner.Run(ctx, command.Invocation{
		Path:         a.binary(),
		Args:         args,
		Env:          a.cfg.Env,
		WorkDir:      a.cfg.WorkDir,
		Timeout:      a.walVerifyTimeout(),
		RedactValues: append(append([]string{}, a.cfg.RedactValues...), a.cfg.WALVerify.RedactValues...),
	})
	evidence := commandEvidence(model.ProviderWALG, "wal-verify", result.Evidence)
	return model.CheckReport{
		Checks:   walVerifyChecks(result.Raw.Stdout, a.walVerifyChecks(), evidence.ID, result.Evidence.ExitStatus, runErr),
		Evidence: []model.EvidenceRecord{evidence},
	}, nil
}

func (a *Adapter) walVerifyArgs(backup model.Backup) ([]string, error) {
	checks := a.walVerifyChecks()
	backupName := firstNonEmpty(a.cfg.WALVerify.BackupName, backup.ProviderID)
	if containsString(checks, "integrity") && backupName == "" {
		return nil, fmt.Errorf("wal-g wal_verify integrity check requires selected backup provider_id or provider.wal_verify.backup_name")
	}

	args := []string{"wal-verify", "--json"}
	if backupName != "" {
		args = append(args, "--backup-name", backupName)
	}
	if a.cfg.WALVerify.Timeline != "" {
		args = append(args, "--timeline", a.cfg.WALVerify.Timeline)
	}
	if a.cfg.WALVerify.LSN != "" {
		args = append(args, "--lsn", a.cfg.WALVerify.LSN)
	}
	args = append(args, checks...)
	return args, nil
}

func (a *Adapter) walVerifyChecks() []string {
	checks := make([]string, 0, len(a.cfg.WALVerify.Checks))
	for _, check := range a.cfg.WALVerify.Checks {
		check = strings.ToLower(strings.TrimSpace(check))
		if check != "" {
			checks = append(checks, check)
		}
	}
	if len(checks) == 0 {
		return []string{"integrity"}
	}
	return checks
}

func (a *Adapter) walVerifyTimeout() time.Duration {
	if a.cfg.WALVerify.Timeout > 0 {
		return a.cfg.WALVerify.Timeout
	}
	return a.cfg.Timeout
}

type walVerifyCheckOutput struct {
	Status string `json:"status"`
}

func walVerifyChecks(data []byte, requested []string, evidenceID string, exitStatus model.ExitStatus, runErr error) []model.Check {
	if len(data) == 0 {
		return []model.Check{walVerifyCommandCheck(model.CheckStatusFailed, evidenceID, "wal-g wal-verify did not produce JSON output", exitStatus, runErr)}
	}

	var output map[string]walVerifyCheckOutput
	if err := json.Unmarshal(data, &output); err != nil {
		return []model.Check{{
			Name:        "wal-g-wal-verify",
			Status:      model.CheckStatusFailed,
			Message:     "parse wal-g wal-verify JSON output: " + err.Error(),
			EvidenceIDs: []string{evidenceID},
			Attributes: map[string]string{
				"operation": "wal-verify",
			},
		}}
	}
	if len(output) == 0 {
		return []model.Check{walVerifyCommandCheck(model.CheckStatusFailed, evidenceID, "wal-g wal-verify JSON output contained no checks", exitStatus, runErr)}
	}

	keys := append([]string{}, requested...)
	seen := map[string]bool{}
	for _, key := range keys {
		seen[key] = true
	}
	for key := range output {
		if !seen[key] {
			keys = append(keys, key)
		}
	}
	if len(keys) > len(requested) {
		sort.Strings(keys[len(requested):])
	}

	checks := make([]model.Check, 0, len(keys)+1)
	for _, key := range keys {
		value, ok := output[key]
		if !ok {
			checks = append(checks, model.Check{
				Name:        "wal-g-wal-verify-" + key,
				Status:      model.CheckStatusFailed,
				Message:     "wal-g wal-verify output did not include requested check " + key,
				EvidenceIDs: []string{evidenceID},
				Attributes: map[string]string{
					"operation": "wal-verify",
					"check":     key,
				},
			})
			continue
		}
		checks = append(checks, walVerifyStatusCheck(key, value.Status, evidenceID))
	}
	if runErr != nil || !exitStatus.Success {
		checks = append(checks, walVerifyCommandCheck(model.CheckStatusFailed, evidenceID, "wal-g wal-verify command failed", exitStatus, runErr))
	}
	return checks
}

func walVerifyStatusCheck(name string, status string, evidenceID string) model.Check {
	status = strings.ToUpper(strings.TrimSpace(status))
	check := model.Check{
		Name:        "wal-g-wal-verify-" + name,
		Status:      model.CheckStatusWarning,
		EvidenceIDs: []string{evidenceID},
		Attributes: map[string]string{
			"operation":         "wal-verify",
			"check":             name,
			"wal_verify_status": status,
		},
	}
	switch status {
	case "OK":
		check.Status = model.CheckStatusPassed
	case "WARNING":
		check.Status = model.CheckStatusWarning
		check.Message = "wal-g wal-verify " + name + " reported WARNING"
	case "FAILURE", "FAILED":
		check.Status = model.CheckStatusFailed
		check.Message = "wal-g wal-verify " + name + " reported " + status
	default:
		check.Message = "wal-g wal-verify " + name + " reported unknown status " + status
	}
	return check
}

func walVerifyCommandCheck(status model.CheckStatus, evidenceID string, message string, exitStatus model.ExitStatus, runErr error) model.Check {
	if runErr != nil {
		message += ": " + runErr.Error()
	} else if !exitStatus.Success {
		message += ": " + exitStatus.Summary()
	}
	return model.Check{
		Name:        "wal-g-wal-verify-command",
		Status:      status,
		Message:     message,
		EvidenceIDs: []string{evidenceID},
		Attributes: map[string]string{
			"operation": "wal-verify",
		},
	}
}

func (a *Adapter) PlanRestore(_ context.Context, backup model.Backup, target model.RecoveryTarget, spec model.TargetSpec) (model.RestorePlan, error) {
	if backup.Provider != "" && backup.Provider != model.ProviderWALG {
		return model.RestorePlan{}, fmt.Errorf("wal-g cannot restore backup from provider %q", backup.Provider)
	}
	if backup.ProviderID == "" {
		return model.RestorePlan{}, fmt.Errorf("wal-g backup provider_id is required")
	}
	if spec.Type != model.RestoreTargetLocal {
		return model.RestorePlan{}, fmt.Errorf("wal-g restore planning currently supports only local targets")
	}
	if spec.WorkDir == "" {
		return model.RestorePlan{}, fmt.Errorf("target work_dir is required")
	}

	dataDir := filepath.Join(spec.WorkDir, "data")
	recoveryConfig, err := a.recoveryConfig(target)
	if err != nil {
		return model.RestorePlan{}, err
	}
	steps := []model.RestoreStep{
		{
			Name:        "wal-g-backup-fetch",
			Description: "Fetch the selected WAL-G base backup into the local target data directory.",
			Command: &model.CommandSpec{
				Tool:       model.ToolWALG,
				Path:       a.binary(),
				Args:       []string{"backup-fetch", dataDir, backup.ProviderID},
				Env:        copyStringMap(a.cfg.Env),
				WorkDir:    a.cfg.WorkDir,
				Timeout:    durationString(a.cfg.Timeout),
				Redactions: append([]string{}, a.cfg.RedactValues...),
			},
			Inputs: map[string]string{
				"backup_id":          backup.ID,
				"provider_backup_id": backup.ProviderID,
			},
			Outputs: map[string]string{
				"data_directory": dataDir,
			},
		},
	}
	verifyStep, err := a.cfg.VerifyBackup.Step(dataDir)
	if err != nil {
		return model.RestorePlan{}, err
	}
	if verifyStep != nil {
		steps = append(steps, *verifyStep)
	}
	steps = append(steps, model.RestoreStep{
		Name:        "wal-g-recovery-config",
		Description: "Configure PostgreSQL archive recovery using WAL-G wal-fetch.",
		Files: []model.FileSpec{
			{
				Path:    filepath.Join(dataDir, "postgresql.auto.conf"),
				Content: recoveryConfig,
				Mode:    "0600",
				Append:  true,
			},
			{
				Path:    filepath.Join(dataDir, "recovery.signal"),
				Content: "",
				Mode:    "0600",
			},
		},
		Inputs: map[string]string{
			"recovery_target": string(target.Type),
		},
		Outputs: map[string]string{
			"postgresql_auto_conf": filepath.Join(dataDir, "postgresql.auto.conf"),
			"recovery_signal":      filepath.Join(dataDir, "recovery.signal"),
		},
	})

	plan := model.RestorePlan{
		Provider:       model.ProviderWALG,
		BackupID:       backup.ID,
		Target:         spec,
		RecoveryTarget: target,
		Runtime: model.RuntimeConfig{
			DataDirectory: dataDir,
			Environment:   copyStringMap(a.cfg.Env),
		},
		Steps:    steps,
		Evidence: []model.EvidenceRecord{planEvidence("restore-plan")},
	}
	return plan, nil
}

func (a *Adapter) binary() string {
	if a.cfg.Binary != "" {
		return a.cfg.Binary
	}
	return defaultBinary
}

func (a *Adapter) recoveryConfig(target model.RecoveryTarget) (string, error) {
	if err := target.Validate(); err != nil {
		return "", err
	}
	lines := []string{
		"restore_command = " + postgresString(shellQuote(a.binary())+` wal-fetch "%f" "%p"`),
	}
	switch target.Type {
	case "", model.RecoveryTargetLatest:
	case model.RecoveryTargetImmediate:
		lines = append(lines, "recovery_target = 'immediate'")
	case model.RecoveryTargetTimestamp:
		if target.Value == "" {
			return "", fmt.Errorf("timestamp recovery target requires value")
		}
		lines = append(lines, "recovery_target_time = "+postgresString(target.Value))
	case model.RecoveryTargetLSN:
		if target.Value == "" {
			return "", fmt.Errorf("lsn recovery target requires value")
		}
		lines = append(lines, "recovery_target_lsn = "+postgresString(target.Value))
	case model.RecoveryTargetXID:
		if target.Value == "" {
			return "", fmt.Errorf("xid recovery target requires value")
		}
		lines = append(lines, "recovery_target_xid = "+postgresString(target.Value))
	case model.RecoveryTargetRestorePoint:
		if target.Value == "" {
			return "", fmt.Errorf("restore point recovery target requires value")
		}
		lines = append(lines, "recovery_target_name = "+postgresString(target.Value))
	default:
		return "", fmt.Errorf("unsupported recovery target %q", target.Type)
	}
	if target.Timeline != "" {
		lines = append(lines, "recovery_target_timeline = "+postgresString(target.Timeline))
	}
	if target.Inclusive != nil {
		lines = append(lines, "recovery_target_inclusive = "+boolString(*target.Inclusive))
	}
	return strings.Join(lines, "\n") + "\n", nil
}

func ParseBackupList(data []byte) ([]model.Backup, error) {
	var entries []backupListEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parse wal-g backup-list json: %w", err)
	}

	backups := make([]model.Backup, 0, len(entries))
	for i, entry := range entries {
		backup, err := entry.toBackup()
		if err != nil {
			return nil, fmt.Errorf("parse wal-g backup-list entry %d: %w", i, err)
		}
		backups = append(backups, backup)
	}
	return backups, nil
}

type backupListEntry struct {
	Name                  string       `json:"name"`
	BackupName            string       `json:"backup_name"`
	LastModified          optionalTime `json:"last_modified"`
	Modified              optionalTime `json:"modified"`
	Time                  optionalTime `json:"time"`
	WALSegmentBackupStart string       `json:"wal_segment_backup_start"`
	StartTime             optionalTime `json:"start_time"`
	FinishTime            optionalTime `json:"finish_time"`
	Hostname              string       `json:"hostname"`
	DataDir               string       `json:"data_dir"`
	PGVersion             stringValue  `json:"pg_version"`
	PostgresVersion       stringValue  `json:"postgres_version"`
	StartLSN              stringValue  `json:"start_lsn"`
	FinishLSN             stringValue  `json:"finish_lsn"`
	IsPermanent           bool         `json:"is_permanent"`
	UserData              any          `json:"user_data"`
}

func (e backupListEntry) toBackup() (model.Backup, error) {
	name := firstNonEmpty(e.Name, e.BackupName)
	if name == "" {
		return model.Backup{}, fmt.Errorf("missing backup name")
	}

	metadata := map[string]string{}
	if e.UserData != nil {
		metadata["has_user_data"] = "true"
	}

	return model.Backup{
		ID:                model.ProviderScopedID(model.ProviderWALG, name),
		Provider:          model.ProviderWALG,
		ProviderID:        name,
		Kind:              inferWALGKind(name),
		Status:            model.BackupStatusAvailable,
		StartedAt:         e.StartTime.ptr(),
		FinishedAt:        e.FinishTime.ptr(),
		LastModifiedAt:    firstTime(e.LastModified, e.Modified, e.Time).ptr(),
		WALRange:          model.WALRange{StartSegment: e.WALSegmentBackupStart, StartLSN: e.StartLSN.Value, EndLSN: e.FinishLSN.Value},
		PostgreSQLVersion: firstNonEmpty(e.PGVersion.Value, e.PostgresVersion.Value),
		DataDirectory:     e.DataDir,
		Hostname:          e.Hostname,
		Permanent:         e.IsPermanent,
		Metadata:          metadataOrNil(metadata),
	}, nil
}

func inferWALGKind(name string) model.BackupKind {
	if strings.Contains(name, "_D_") {
		return model.BackupKindDelta
	}
	if strings.HasPrefix(name, "base_") {
		return model.BackupKindFull
	}
	return model.BackupKindUnknown
}

func commandEvidence(provider model.ProviderType, operation string, evidence model.CommandEvidence) model.EvidenceRecord {
	collectedAt := evidence.FinishedAt
	if collectedAt.IsZero() {
		collectedAt = time.Now().UTC()
	}

	idTime := collectedAt.Format(time.RFC3339Nano)
	return model.EvidenceRecord{
		ID:          string(provider) + ":" + operation + ":" + idTime,
		Kind:        model.EvidenceCommand,
		Source:      string(provider),
		CollectedAt: collectedAt,
		Command:     &evidence,
		Attributes: map[string]string{
			"operation": operation,
		},
	}
}

func planEvidence(operation string) model.EvidenceRecord {
	now := time.Now().UTC()
	return model.EvidenceRecord{
		ID:          string(model.ProviderWALG) + ":" + operation + ":" + now.Format(time.RFC3339Nano),
		Kind:        model.EvidencePlan,
		Source:      string(model.ProviderWALG),
		CollectedAt: now,
		Attributes: map[string]string{
			"operation": operation,
		},
	}
}

type optionalTime struct {
	Time  time.Time
	Valid bool
}

func (t *optionalTime) UnmarshalJSON(data []byte) error {
	raw := strings.TrimSpace(string(data))
	if raw == "" || raw == "null" || raw == `""` {
		return nil
	}

	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return err
	}
	parsed, err := parseTime(text)
	if err != nil {
		return err
	}
	t.Time = parsed
	t.Valid = true
	return nil
}

func (t optionalTime) ptr() *time.Time {
	if !t.Valid {
		return nil
	}
	value := t.Time
	return &value
}

type stringValue struct {
	Value string
	Valid bool
}

func (v *stringValue) UnmarshalJSON(data []byte) error {
	raw := strings.TrimSpace(string(data))
	if raw == "" || raw == "null" {
		return nil
	}

	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		v.Value = text
		v.Valid = true
		return nil
	}

	var number json.Number
	if err := json.Unmarshal(data, &number); err == nil {
		v.Value = number.String()
		v.Valid = true
		return nil
	}

	var boolean bool
	if err := json.Unmarshal(data, &boolean); err == nil {
		if boolean {
			v.Value = "true"
		} else {
			v.Value = "false"
		}
		v.Valid = true
		return nil
	}
	return fmt.Errorf("unsupported json scalar %s", raw)
}

func parseTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05Z07:00",
		"2006-01-02 15:04:05",
		"Monday, 02-Jan-06 15:04:05 MST",
	} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported time format %q", value)
}

func firstTime(values ...optionalTime) optionalTime {
	for _, value := range values {
		if value.Valid {
			return value
		}
	}
	return optionalTime{}
}

func durationString(value time.Duration) string {
	if value == 0 {
		return ""
	}
	return value.String()
}

func postgresString(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func copyStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func metadataOrNil(metadata map[string]string) map[string]string {
	if len(metadata) == 0 {
		return nil
	}
	return metadata
}
