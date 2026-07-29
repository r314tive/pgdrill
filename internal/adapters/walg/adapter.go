package walg

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/r314tive/pgdrill/internal/adapterutil"
	"github.com/r314tive/pgdrill/internal/command"
	"github.com/r314tive/pgdrill/internal/jsonutil"
	"github.com/r314tive/pgdrill/internal/model"
	"github.com/r314tive/pgdrill/internal/restorechecks/pgverifybackup"
)

const (
	defaultBinary      = "wal-g"
	maxWALVerifyChecks = model.MaxChecksPerReport - 1
)

type Config struct {
	Binary         string
	Env            map[string]string
	WorkDir        string
	Timeout        time.Duration
	RestoreTimeout time.Duration
	RedactValues   []string
	WALVerify      WALVerifyConfig
	VerifyBackup   pgverifybackup.Config
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
			adapterutil.CommandEvidence(model.ProviderWALG, "backup-list", result.Evidence),
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
		return catalog, result.RedactError(err)
	}
	backups, err = adapterutil.RedactBackups(backups, result)
	if err != nil {
		return catalog, fmt.Errorf("redact wal-g backup catalog: %w", err)
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
	evidence := adapterutil.CommandEvidence(model.ProviderWALG, "wal-verify", result.Evidence)
	checks, err := adapterutil.RedactChecks(
		walVerifyChecks(result.Raw.Stdout, a.walVerifyChecks(), evidence.ID, result.Evidence.ExitStatus, runErr),
		result,
	)
	if err != nil {
		return model.CheckReport{
			Evidence: []model.EvidenceRecord{evidence},
		}, fmt.Errorf("redact wal-g wal-verify checks: %w", result.RedactError(err))
	}
	return model.CheckReport{
		Checks:   checks,
		Evidence: []model.EvidenceRecord{evidence},
	}, nil
}

func (a *Adapter) walVerifyArgs(backup model.Backup) ([]string, error) {
	checks := a.walVerifyChecks()
	if len(checks) > maxWALVerifyChecks {
		return nil, fmt.Errorf(
			"wal-g wal_verify checks exceed maximum count %d",
			maxWALVerifyChecks,
		)
	}
	for _, check := range checks {
		if err := validateWALVerifyCheckName(check); err != nil {
			return nil, err
		}
	}
	backupName := adapterutil.FirstNonEmpty(a.cfg.WALVerify.BackupName, backup.ProviderID)
	if slices.Contains(checks, "integrity") && backupName == "" {
		return nil, fmt.Errorf("wal-g wal_verify integrity check requires selected backup provider_id or provider.wal_verify.backup_name")
	}

	args := []string{"wal-verify", "--json"}
	if backupName != "" {
		args = append(args, "--backup-name", backupName)
	}
	configuredTarget := a.cfg.WALVerify.Timeline != "" || a.cfg.WALVerify.LSN != ""
	timeline := adapterutil.FirstNonEmpty(a.cfg.WALVerify.Timeline, backup.WALRange.Timeline)
	lsn := adapterutil.FirstNonEmpty(a.cfg.WALVerify.LSN, backup.WALRange.EndLSN)
	if timeline != "" && lsn != "" {
		args = append(args, "--timeline", timeline, "--lsn", lsn)
	} else if configuredTarget {
		return nil, fmt.Errorf(
			"wal-g wal_verify timeline and lsn must both be available from configuration or selected backup metadata",
		)
	}
	args = append(args, checks...)
	return args, nil
}

func (a *Adapter) walVerifyChecks() []string {
	checks := make([]string, 0, len(a.cfg.WALVerify.Checks))
	seen := make(map[string]struct{}, len(a.cfg.WALVerify.Checks))
	for _, check := range a.cfg.WALVerify.Checks {
		check = strings.ToLower(strings.TrimSpace(check))
		if check != "" {
			if _, duplicate := seen[check]; duplicate {
				continue
			}
			seen[check] = struct{}{}
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
	if err := jsonutil.DecodeOne(data, &output); err != nil {
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
	if len(output) > maxWALVerifyChecks || len(requested) > maxWALVerifyChecks {
		return []model.Check{walVerifyCommandCheck(
			model.CheckStatusFailed,
			evidenceID,
			fmt.Sprintf(
				"wal-g wal-verify checks exceed maximum count %d",
				maxWALVerifyChecks,
			),
			exitStatus,
			runErr,
		)}
	}

	keys := make([]string, 0, min(maxWALVerifyChecks, len(requested)+len(output)))
	seen := make(map[string]bool, len(requested)+len(output))
	for _, key := range requested {
		if seen[key] {
			continue
		}
		if err := validateWALVerifyCheckName(key); err != nil {
			return []model.Check{walVerifyCommandCheck(
				model.CheckStatusFailed,
				evidenceID,
				"wal-g wal-verify requested check name is invalid",
				exitStatus,
				runErr,
			)}
		}
		seen[key] = true
		keys = append(keys, key)
	}
	for key := range output {
		if err := validateWALVerifyCheckName(key); err != nil {
			return []model.Check{walVerifyCommandCheck(
				model.CheckStatusFailed,
				evidenceID,
				"wal-g wal-verify output contains an invalid check name",
				exitStatus,
				runErr,
			)}
		}
		if !seen[key] {
			if len(keys) == maxWALVerifyChecks {
				return []model.Check{walVerifyCommandCheck(
					model.CheckStatusFailed,
					evidenceID,
					fmt.Sprintf(
						"wal-g wal-verify checks exceed maximum count %d",
						maxWALVerifyChecks,
					),
					exitStatus,
					runErr,
				)}
			}
			keys = append(keys, key)
			seen[key] = true
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

func validateWALVerifyCheckName(name string) error {
	if err := model.ValidateIdentity(
		"wal-g wal_verify check name",
		"wal-g-wal-verify-"+name,
	); err != nil {
		return fmt.Errorf("invalid wal-g wal_verify check name: %w", err)
	}
	return nil
}

func walVerifyStatusCheck(name string, status string, evidenceID string) model.Check {
	status = strings.ToUpper(strings.TrimSpace(status))
	check := model.Check{
		Name:        "wal-g-wal-verify-" + name,
		Status:      model.CheckStatusFailed,
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
	target = target.Normalized()
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
				Env:        adapterutil.CloneStringMap(a.cfg.Env),
				WorkDir:    a.cfg.WorkDir,
				Timeout:    adapterutil.DurationString(a.restoreTimeout()),
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
			Environment:   adapterutil.CloneStringMap(a.cfg.Env),
		},
		Steps:    steps,
		Evidence: []model.EvidenceRecord{adapterutil.PlanEvidence(model.ProviderWALG, "restore-plan")},
	}
	return plan, nil
}

func (a *Adapter) binary() string {
	if a.cfg.Binary != "" {
		return a.cfg.Binary
	}
	return defaultBinary
}

func (a *Adapter) restoreTimeout() time.Duration {
	if a.cfg.RestoreTimeout > 0 {
		return a.cfg.RestoreTimeout
	}
	return a.cfg.Timeout
}

func (a *Adapter) recoveryConfig(target model.RecoveryTarget) (string, error) {
	target = target.Normalized()
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
		timestamp, err := target.Timestamp()
		if err != nil {
			return "", err
		}
		postgresTimestamp := timestamp.UTC().Format("2006-01-02 15:04:05.999999999-07:00")
		lines = append(lines, "recovery_target_time = "+postgresString(postgresTimestamp))
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
	if target.Type != model.RecoveryTargetLatest {
		lines = append(lines, "recovery_target_action = 'pause'")
	}
	return strings.Join(lines, "\n") + "\n", nil
}

func ParseBackupList(data []byte) ([]model.Backup, error) {
	var entries []backupListEntry
	if err := jsonutil.DecodeOne(data, &entries); err != nil {
		return nil, fmt.Errorf("parse wal-g backup-list json: %w", err)
	}
	if entries == nil {
		return nil, fmt.Errorf("parse wal-g backup-list json: expected array")
	}
	if len(entries) > model.MaxBackupsPerCatalog {
		return nil, fmt.Errorf(
			"parse wal-g backup-list json: backups exceed maximum count %d",
			model.MaxBackupsPerCatalog,
		)
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
	if err := e.validateAliases(); err != nil {
		return model.Backup{}, err
	}
	name := adapterutil.FirstNonEmpty(e.Name, e.BackupName)
	if name == "" {
		return model.Backup{}, fmt.Errorf("missing backup name")
	}
	startLSN, err := normalizeWALLSN(e.StartLSN.Value)
	if err != nil {
		return model.Backup{}, fmt.Errorf("start_lsn: %w", err)
	}
	finishLSN, err := normalizeWALLSN(e.FinishLSN.Value)
	if err != nil {
		return model.Backup{}, fmt.Errorf("finish_lsn: %w", err)
	}

	metadata := map[string]string{}
	if e.UserData != nil {
		metadata["has_user_data"] = "true"
	}

	return model.Backup{
		ID:             model.ProviderScopedID(model.ProviderWALG, name),
		Provider:       model.ProviderWALG,
		ProviderID:     name,
		Kind:           inferWALGKind(name),
		Status:         model.BackupStatusAvailable,
		StartedAt:      e.StartTime.ptr(),
		FinishedAt:     e.FinishTime.ptr(),
		LastModifiedAt: firstTime(e.LastModified, e.Modified, e.Time).ptr(),
		WALRange: model.WALRange{
			StartSegment: e.WALSegmentBackupStart,
			StartLSN:     startLSN,
			EndLSN:       finishLSN,
			Timeline:     walSegmentTimeline(e.WALSegmentBackupStart),
		},
		PostgreSQLVersion: adapterutil.FirstNonEmpty(e.PGVersion.Value, e.PostgresVersion.Value),
		DataDirectory:     e.DataDir,
		Hostname:          e.Hostname,
		Permanent:         e.IsPermanent,
		Metadata:          adapterutil.StringMapOrNil(metadata),
	}, nil
}

func (e backupListEntry) validateAliases() error {
	if e.Name != "" && e.BackupName != "" && e.Name != e.BackupName {
		return fmt.Errorf(`backup name aliases "name" and "backup_name" conflict`)
	}
	times := []struct {
		key   string
		value optionalTime
	}{
		{key: "last_modified", value: e.LastModified},
		{key: "modified", value: e.Modified},
		{key: "time", value: e.Time},
	}
	var selected *struct {
		key   string
		value optionalTime
	}
	for index := range times {
		if !times[index].value.Valid {
			continue
		}
		if selected != nil && !selected.value.Time.Equal(times[index].value.Time) {
			return fmt.Errorf(
				`last modified time aliases %q and %q conflict`,
				selected.key,
				times[index].key,
			)
		}
		if selected == nil {
			selected = &times[index]
		}
	}
	if e.PGVersion.Valid &&
		e.PostgresVersion.Valid &&
		e.PGVersion.Value != e.PostgresVersion.Value {
		return fmt.Errorf(
			`PostgreSQL version aliases "pg_version" and "postgres_version" conflict`,
		)
	}
	return nil
}

func normalizeWALLSN(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if high, low, ok := strings.Cut(value, "/"); ok {
		if high == "" || low == "" || strings.Contains(low, "/") {
			return "", fmt.Errorf("value %q is not PostgreSQL X/Y notation", value)
		}
		highValue, err := strconv.ParseUint(high, 16, 32)
		if err != nil {
			return "", fmt.Errorf("value %q is not PostgreSQL X/Y notation: %w", value, err)
		}
		lowValue, err := strconv.ParseUint(low, 16, 32)
		if err != nil {
			return "", fmt.Errorf("value %q is not PostgreSQL X/Y notation: %w", value, err)
		}
		return fmt.Sprintf("%X/%X", highValue, lowValue), nil
	}

	location, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return "", fmt.Errorf("value %q is neither an unsigned WAL location nor PostgreSQL X/Y notation: %w", value, err)
	}
	return fmt.Sprintf("%X/%X", location>>32, location&0xffffffff), nil
}

func walSegmentTimeline(segment string) string {
	segment = strings.TrimSpace(segment)
	if len(segment) != 24 {
		return ""
	}
	for offset := 0; offset < len(segment); offset += 8 {
		if _, err := strconv.ParseUint(segment[offset:offset+8], 16, 32); err != nil {
			return ""
		}
	}
	timeline, err := strconv.ParseUint(segment[:8], 16, 32)
	if err != nil || timeline == 0 {
		return ""
	}
	return strconv.FormatUint(timeline, 10)
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
