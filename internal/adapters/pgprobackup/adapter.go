package pgprobackup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/r314tive/pgdrill/internal/adapterutil"
	"github.com/r314tive/pgdrill/internal/command"
	"github.com/r314tive/pgdrill/internal/jsonutil"
	"github.com/r314tive/pgdrill/internal/model"
	"github.com/r314tive/pgdrill/internal/restorechecks/pgverifybackup"
)

const defaultBinary = "pg_probackup"

type Config struct {
	Binary         string
	BackupDir      string
	Instance       string
	Env            map[string]string
	WorkDir        string
	Timeout        time.Duration
	RestoreTimeout time.Duration
	RedactValues   []string
	Validate       ValidateConfig
	VerifyBackup   pgverifybackup.Config
}

type ValidateConfig struct {
	Enabled             bool
	Timeout             time.Duration
	WAL                 bool
	SkipBlockValidation bool
	Threads             int
	RedactValues        []string
}

type Adapter struct {
	cfg    Config
	runner command.Runner
}

func New(cfg Config, runner command.Runner) *Adapter {
	if runner == nil {
		runner = command.NewRunner(command.Options{DefaultTimeout: cfg.Timeout})
	}
	return &Adapter{cfg: cfg, runner: runner}
}

func (a *Adapter) Type() model.ProviderType {
	return model.ProviderPGProbackup
}

func (a *Adapter) DiscoverBackups(ctx context.Context) (model.BackupCatalog, error) {
	catalog := model.BackupCatalog{Provider: model.ProviderPGProbackup}
	args, err := a.showArgs()
	if err != nil {
		return catalog, err
	}

	result, runErr := a.runner.Run(ctx, command.Invocation{
		Path:         a.binary(),
		Args:         args,
		Env:          a.cfg.Env,
		WorkDir:      a.cfg.WorkDir,
		Timeout:      a.cfg.Timeout,
		RedactValues: a.cfg.RedactValues,
	})
	catalog.Evidence = []model.EvidenceRecord{
		adapterutil.CommandEvidence(model.ProviderPGProbackup, "show", result.Evidence),
	}
	if runErr != nil {
		return catalog, fmt.Errorf("run pg_probackup show: %w", runErr)
	}
	if !result.Evidence.ExitStatus.Success {
		return catalog, fmt.Errorf("pg_probackup show failed: %s", result.Evidence.ExitStatus.Summary())
	}

	backups, err := ParseShow(result.Raw.Stdout, a.cfg.Instance)
	if err != nil {
		return catalog, result.RedactError(err)
	}
	backups, err = adapterutil.RedactBackups(backups, result)
	if err != nil {
		return catalog, fmt.Errorf("redact pg_probackup catalog: %w", err)
	}
	catalog.Backups = backups
	return catalog, nil
}

func (a *Adapter) ValidateCatalog(ctx context.Context, _ model.BackupCatalog, backup model.Backup, target model.RecoveryTarget) (model.CheckReport, error) {
	if !a.cfg.Validate.Enabled {
		return model.CheckReport{Checks: []model.Check{{
			Name:    "pg-probackup-validate",
			Status:  model.CheckStatusSkipped,
			Message: "pg_probackup validate is not enabled; restore will retain pg_probackup's default pre-restore validation.",
			Attributes: map[string]string{
				"operation": "validate",
			},
		}}}, nil
	}

	args, err := a.validateArgs(backup, target)
	if err != nil {
		return model.CheckReport{}, err
	}
	result, runErr := a.runner.Run(ctx, command.Invocation{
		Path:         a.binary(),
		Args:         args,
		Env:          a.cfg.Env,
		WorkDir:      a.cfg.WorkDir,
		Timeout:      a.validateTimeout(),
		RedactValues: append(append([]string{}, a.cfg.RedactValues...), a.cfg.Validate.RedactValues...),
	})
	evidence := adapterutil.CommandEvidence(model.ProviderPGProbackup, "validate", result.Evidence)
	check := model.Check{
		Name:        "pg-probackup-validate",
		Status:      model.CheckStatusPassed,
		EvidenceIDs: []string{evidence.ID},
		Attributes: map[string]string{
			"operation": "validate",
		},
	}
	if runErr != nil {
		check.Status = model.CheckStatusFailed
		check.Message = "run pg_probackup validate: " + result.RedactError(runErr).Error()
	} else if !result.Evidence.ExitStatus.Success {
		check.Status = model.CheckStatusFailed
		check.Message = "pg_probackup validate failed: " + result.Evidence.ExitStatus.Summary()
	}
	check, err = adapterutil.RedactCheck(check, result)
	if err != nil {
		return model.CheckReport{
			Evidence: []model.EvidenceRecord{evidence},
		}, fmt.Errorf("redact pg_probackup validate result: %w", result.RedactError(err))
	}
	return model.CheckReport{
		Checks:   []model.Check{check},
		Evidence: []model.EvidenceRecord{evidence},
	}, nil
}

func (a *Adapter) PlanRestore(_ context.Context, backup model.Backup, target model.RecoveryTarget, spec model.TargetSpec) (model.RestorePlan, error) {
	target = target.Normalized()
	if backup.Provider != "" && backup.Provider != model.ProviderPGProbackup {
		return model.RestorePlan{}, fmt.Errorf("pg_probackup cannot restore backup from provider %q", backup.Provider)
	}
	if spec.Type != model.RestoreTargetLocal {
		return model.RestorePlan{}, fmt.Errorf("pg_probackup restore planning currently supports only local targets")
	}
	if strings.TrimSpace(spec.WorkDir) == "" {
		return model.RestorePlan{}, fmt.Errorf("target work_dir is required")
	}

	dataDir := filepath.Join(spec.WorkDir, "data")
	args, instance, backupID, err := a.restoreArgs(backup, target, dataDir)
	if err != nil {
		return model.RestorePlan{}, err
	}
	step := model.RestoreStep{
		Name:        "pg-probackup-restore",
		Description: "Restore the selected pg_probackup backup into the local target data directory.",
		Command: &model.CommandSpec{
			Tool:       model.ToolPGProbackup,
			Path:       a.binary(),
			Args:       args,
			Env:        adapterutil.CloneStringMap(a.cfg.Env),
			WorkDir:    a.cfg.WorkDir,
			Timeout:    adapterutil.DurationString(a.restoreTimeout()),
			Redactions: append([]string{}, a.cfg.RedactValues...),
		},
		Inputs: map[string]string{
			"backup_id":          backup.ID,
			"provider_backup_id": backup.ProviderID,
			"instance":           instance,
			"native_backup_id":   backupID,
		},
		Outputs: map[string]string{
			"data_directory": dataDir,
		},
	}

	steps := []model.RestoreStep{step}
	verifyStep, err := a.cfg.VerifyBackup.Step(dataDir)
	if err != nil {
		return model.RestorePlan{}, err
	}
	if verifyStep != nil {
		steps = append(steps, *verifyStep)
	}

	return model.RestorePlan{
		Provider:       model.ProviderPGProbackup,
		BackupID:       backup.ID,
		Target:         spec,
		RecoveryTarget: target,
		Steps:          steps,
		Runtime: model.RuntimeConfig{
			DataDirectory: dataDir,
			Environment:   adapterutil.CloneStringMap(a.cfg.Env),
		},
		Evidence: []model.EvidenceRecord{adapterutil.PlanEvidence(model.ProviderPGProbackup, "restore-plan")},
	}, nil
}

func (a *Adapter) binary() string {
	if strings.TrimSpace(a.cfg.Binary) != "" {
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

func (a *Adapter) showArgs() ([]string, error) {
	if strings.TrimSpace(a.cfg.BackupDir) == "" {
		return nil, fmt.Errorf("pg_probackup backup_dir is required")
	}
	args := []string{"show", "-B", a.cfg.BackupDir}
	if a.cfg.Instance != "" {
		args = append(args, "--instance="+a.cfg.Instance)
	}
	return append(args, "--format=json"), nil
}

func (a *Adapter) validateArgs(backup model.Backup, target model.RecoveryTarget) ([]string, error) {
	instance, backupID, err := a.backupIdentity(backup)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(a.cfg.BackupDir) == "" {
		return nil, fmt.Errorf("pg_probackup backup_dir is required")
	}
	if a.cfg.Validate.Threads < 0 {
		return nil, fmt.Errorf("pg_probackup validate threads must not be negative")
	}

	args := []string{"validate", "-B", a.cfg.BackupDir, "--instance=" + instance, "-i", backupID}
	if a.cfg.Validate.Threads > 0 {
		args = append(args, "-j", strconv.Itoa(a.cfg.Validate.Threads))
	}
	if a.cfg.Validate.WAL {
		args = append(args, "--wal")
	}
	if a.cfg.Validate.SkipBlockValidation {
		args = append(args, "--skip-block-validation")
	}
	recoveryArgs, err := recoveryArgs(target, false)
	if err != nil {
		return nil, err
	}
	return append(args, recoveryArgs...), nil
}

func (a *Adapter) restoreArgs(backup model.Backup, target model.RecoveryTarget, dataDir string) ([]string, string, string, error) {
	instance, backupID, err := a.backupIdentity(backup)
	if err != nil {
		return nil, "", "", err
	}
	if strings.TrimSpace(a.cfg.BackupDir) == "" {
		return nil, "", "", fmt.Errorf("pg_probackup backup_dir is required")
	}
	recoveryArgs, err := recoveryArgs(target, true)
	if err != nil {
		return nil, "", "", err
	}
	args := []string{"restore", "-B", a.cfg.BackupDir, "--instance=" + instance, "-i", backupID, "-D", dataDir}
	return append(args, recoveryArgs...), instance, backupID, nil
}

func (a *Adapter) backupIdentity(backup model.Backup) (instance string, backupID string, err error) {
	if backup.Provider != "" && backup.Provider != model.ProviderPGProbackup {
		return "", "", fmt.Errorf("pg_probackup backup belongs to provider %q", backup.Provider)
	}
	providerID := strings.TrimSpace(backup.ProviderID)
	if providerID == "" {
		return "", "", fmt.Errorf("pg_probackup backup provider_id is required")
	}

	instance = strings.TrimSpace(backup.ClusterName)
	backupID = providerID
	if before, after, ok := strings.Cut(providerID, "/"); ok {
		if before == "" || after == "" || strings.Contains(after, "/") {
			return "", "", fmt.Errorf("invalid pg_probackup provider_id %q", providerID)
		}
		if instance != "" && instance != before {
			return "", "", fmt.Errorf("pg_probackup backup cluster %q does not match provider_id instance %q", instance, before)
		}
		instance = before
		backupID = after
	}
	if a.cfg.Instance != "" {
		if instance != "" && instance != a.cfg.Instance {
			return "", "", fmt.Errorf("pg_probackup backup belongs to instance %q, adapter is configured for %q", instance, a.cfg.Instance)
		}
		instance = a.cfg.Instance
	}
	if instance == "" {
		return "", "", fmt.Errorf("pg_probackup instance is required in provider_id, backup cluster_name, or adapter config")
	}
	return instance, backupID, nil
}

func (a *Adapter) validateTimeout() time.Duration {
	if a.cfg.Validate.Timeout > 0 {
		return a.cfg.Validate.Timeout
	}
	return a.cfg.Timeout
}

func recoveryArgs(target model.RecoveryTarget, includeAction bool) ([]string, error) {
	target = target.Normalized()
	if err := target.Validate(); err != nil {
		return nil, err
	}
	args := []string{}
	targeted := target.Type != model.RecoveryTargetLatest
	switch target.Type {
	case "":
	case model.RecoveryTargetLatest:
		args = append(args, "--recovery-target=latest")
	case model.RecoveryTargetImmediate:
		args = append(args, "--recovery-target=immediate")
	case model.RecoveryTargetTimestamp:
		if target.Value == "" {
			return nil, fmt.Errorf("timestamp recovery target requires value")
		}
		timestamp, err := target.Timestamp()
		if err != nil {
			return nil, err
		}
		if timestamp.Nanosecond() != 0 {
			return nil, fmt.Errorf("pg_probackup recovery target timestamp requires whole-second precision")
		}
		nativeTimestamp := timestamp.UTC().Format("2006-01-02 15:04:05-07:00")
		args = append(args, "--recovery-target-time="+nativeTimestamp)
	case model.RecoveryTargetLSN:
		if target.Value == "" {
			return nil, fmt.Errorf("lsn recovery target requires value")
		}
		args = append(args, "--recovery-target-lsn="+target.Value)
	case model.RecoveryTargetXID:
		if target.Value == "" {
			return nil, fmt.Errorf("xid recovery target requires value")
		}
		args = append(args, "--recovery-target-xid="+target.Value)
	case model.RecoveryTargetRestorePoint:
		if target.Value == "" {
			return nil, fmt.Errorf("restore point recovery target requires value")
		}
		args = append(args, "--recovery-target-name="+target.Value)
	default:
		return nil, fmt.Errorf("unsupported recovery target %q", target.Type)
	}
	if target.Timeline != "" {
		args = append(args, "--recovery-target-timeline="+target.Timeline)
	}
	if target.Inclusive != nil {
		switch target.Type {
		case model.RecoveryTargetTimestamp, model.RecoveryTargetLSN, model.RecoveryTargetXID:
			args = append(args, "--recovery-target-inclusive="+strconv.FormatBool(*target.Inclusive))
		default:
			return nil, fmt.Errorf("recovery target %q does not support inclusive", target.Type)
		}
	}
	if includeAction && targeted {
		args = append(args, "--recovery-target-action=pause")
	}
	return args, nil
}

func ParseShow(data []byte, defaultInstance string) ([]model.Backup, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("pg_probackup show produced no JSON output")
	}
	var root any
	if err := jsonutil.DecodeOne(data, &root); err != nil {
		return nil, fmt.Errorf("parse pg_probackup show json: %w", err)
	}
	instances, err := instanceObjects(root)
	if err != nil {
		return nil, err
	}

	backups := []model.Backup{}
	for i, object := range instances {
		instanceValue, _, err := adapterutil.OptionalTrimmedStringAlias(object, "instance name", "instance")
		if err != nil {
			return nil, fmt.Errorf("parse pg_probackup instance %d: %w", i, err)
		}
		instance := firstNonEmpty(instanceValue, defaultInstance)
		if instance == "" {
			return nil, fmt.Errorf("parse pg_probackup instance %d: missing instance name", i)
		}
		value, ok := object["backups"]
		if !ok {
			return nil, fmt.Errorf("parse pg_probackup instance %q: missing backups array", instance)
		}
		entries, ok := value.([]any)
		if !ok {
			return nil, fmt.Errorf("parse pg_probackup instance %q: backups must be an array", instance)
		}
		if len(entries) > model.MaxBackupsPerCatalog-len(backups) {
			return nil, fmt.Errorf(
				"parse pg_probackup show: backups exceed maximum count %d",
				model.MaxBackupsPerCatalog,
			)
		}
		for j, value := range entries {
			entry, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("parse pg_probackup instance %q backup %d: expected object", instance, j)
			}
			backup, err := mapBackup(entry, instance)
			if err != nil {
				return nil, fmt.Errorf("parse pg_probackup instance %q backup %d: %w", instance, j, err)
			}
			backups = append(backups, backup)
		}
	}
	return backups, nil
}

func instanceObjects(root any) ([]map[string]any, error) {
	switch typed := root.(type) {
	case []any:
		if len(typed) > model.MaxBackupsPerCatalog {
			return nil, fmt.Errorf(
				"parse pg_probackup show: instances exceed maximum count %d",
				model.MaxBackupsPerCatalog,
			)
		}
		objects := make([]map[string]any, 0, len(typed))
		for i, value := range typed {
			object, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("parse pg_probackup instance %d: expected object", i)
			}
			objects = append(objects, object)
		}
		return objects, nil
	case map[string]any:
		return []map[string]any{typed}, nil
	default:
		return nil, fmt.Errorf("pg_probackup show JSON must be an object or array")
	}
}

func mapBackup(object map[string]any, instance string) (model.Backup, error) {
	backupID, err := adapterutil.RequiredTrimmedStringAlias(object, "backup id", "id", "backup-id", "backup_id")
	if err != nil {
		return model.Backup{}, err
	}
	parentID, _, err := adapterutil.OptionalTrimmedStringAlias(
		object,
		"parent backup id",
		"parent-backup-id",
		"parent_backup_id",
	)
	if err != nil {
		return model.Backup{}, err
	}
	status, _, err := adapterutil.OptionalTrimmedStringAlias(object, "backup status", "status")
	if err != nil {
		return model.Backup{}, err
	}
	backupMode, _, err := adapterutil.OptionalTrimmedStringAlias(object, "backup mode", "backup-mode", "backup_mode")
	if err != nil {
		return model.Backup{}, err
	}
	startedAt, err := optionalTime(object, "start-time", "start_time")
	if err != nil {
		return model.Backup{}, err
	}
	finishedAt, err := optionalTime(object, "end-time", "end_time")
	if err != nil {
		return model.Backup{}, err
	}
	validatedAt, err := optionalTime(object, "end-validation-time", "end_validation_time")
	if err != nil {
		return model.Backup{}, err
	}
	startLSN, err := consistentString(object, "start LSN", "start-lsn", "start_lsn")
	if err != nil {
		return model.Backup{}, err
	}
	endLSN, err := consistentString(
		object,
		"end LSN",
		"stop-lsn",
		"stop_lsn",
		"end-lsn",
		"end_lsn",
	)
	if err != nil {
		return model.Backup{}, err
	}
	timeline, err := consistentString(
		object,
		"timeline",
		"current-tli",
		"current_tli",
		"timelineid",
	)
	if err != nil {
		return model.Backup{}, err
	}
	postgresVersion, err := consistentString(
		object,
		"server version",
		"server-version",
		"server_version",
	)
	if err != nil {
		return model.Backup{}, err
	}
	lastModifiedAt := validatedAt
	if lastModifiedAt == nil {
		lastModifiedAt = finishedAt
	}

	providerID := instance + "/" + backupID
	return model.Backup{
		ID:             model.ProviderScopedID(model.ProviderPGProbackup, providerID),
		Provider:       model.ProviderPGProbackup,
		ProviderID:     providerID,
		ClusterName:    instance,
		ParentID:       parentID,
		Kind:           mapBackupKind(backupMode),
		Status:         mapBackupStatus(status),
		StartedAt:      startedAt,
		FinishedAt:     finishedAt,
		LastModifiedAt: lastModifiedAt,
		WALRange: model.WALRange{
			StartLSN: startLSN,
			EndLSN:   endLSN,
			Timeline: timeline,
		},
		PostgreSQLVersion: postgresVersion,
		Metadata: metadata(object,
			"status", "backup-mode", "wal", "program-version", "parent-tli",
			"compress-alg", "compress-level", "from-replica", "checksum-version",
			"recovery-xid", "recovery-time", "data-bytes", "wal-bytes",
			"uncompressed-bytes", "pgdata-bytes", "content-crc", "expire-time",
		),
	}, nil
}

func mapBackupKind(value string) model.BackupKind {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "FULL":
		return model.BackupKindFull
	case "DELTA":
		return model.BackupKindDelta
	case "PAGE", "PTRACK":
		return model.BackupKindIncremental
	default:
		return model.BackupKindUnknown
	}
}

func mapBackupStatus(value string) model.BackupStatus {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "OK", "DONE":
		return model.BackupStatusAvailable
	case "RUNNING", "MERGING", "MERGED", "DELETING":
		return model.BackupStatusRunning
	case "CORRUPT", "ORPHAN":
		return model.BackupStatusInvalid
	case "ERROR", "HIDDEN_FOR_TEST":
		return model.BackupStatusFailed
	default:
		return model.BackupStatusUnknown
	}
}

func optionalTime(object map[string]any, keys ...string) (*time.Time, error) {
	var (
		selectedKey  string
		selectedTime *time.Time
	)
	for _, key := range keys {
		if _, present := object[key]; !present || object[key] == nil {
			continue
		}
		value := getString(object, key)
		if value == "" {
			continue
		}
		parsed, err := parseTime(value)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", key, err)
		}
		if selectedTime != nil && !selectedTime.Equal(parsed) {
			return nil, fmt.Errorf(
				"time aliases %q and %q conflict",
				selectedKey,
				key,
			)
		}
		if selectedTime == nil {
			selectedKey = key
			selectedTime = &parsed
		}
	}
	return selectedTime, nil
}

func consistentString(
	object map[string]any,
	name string,
	keys ...string,
) (string, error) {
	var (
		selectedKey   string
		selectedValue string
	)
	for _, key := range keys {
		if _, present := object[key]; !present || object[key] == nil {
			continue
		}
		value := getString(object, key)
		if value == "" {
			continue
		}
		if selectedValue != "" && value != selectedValue {
			return "", fmt.Errorf(
				"%s aliases %q and %q conflict",
				name,
				selectedKey,
				key,
			)
		}
		if selectedValue == "" {
			selectedKey = key
			selectedValue = value
		}
	}
	return selectedValue, nil
}

func parseTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05.999999999Z07",
		"2006-01-02 15:04:05Z07:00",
		"2006-01-02 15:04:05Z07",
		"2006-01-02 15:04:05",
	} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported time format %q", value)
}

func getString(object map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := object[key]
		if !ok || value == nil {
			continue
		}
		switch typed := value.(type) {
		case string:
			if strings.TrimSpace(typed) != "" {
				return strings.TrimSpace(typed)
			}
		case json.Number:
			return typed.String()
		case bool:
			return strconv.FormatBool(typed)
		case float64:
			return strconv.FormatFloat(typed, 'f', -1, 64)
		}
	}
	return ""
}

func metadata(object map[string]any, keys ...string) map[string]string {
	result := map[string]string{}
	for _, key := range keys {
		if value := getString(object, key); value != "" {
			result[key] = value
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
