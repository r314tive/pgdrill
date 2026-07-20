package barman

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/r314tive/pgdrill/internal/command"
	"github.com/r314tive/pgdrill/internal/model"
	"github.com/r314tive/pgdrill/internal/restorechecks/pgverifybackup"
)

const defaultBinary = "barman"

type Config struct {
	Binary         string
	ConfigPath     string
	Server         string
	Env            map[string]string
	WorkDir        string
	Timeout        time.Duration
	RestoreTimeout time.Duration
	RedactValues   []string
	Manifest       ManifestConfig
	BarmanVerify   BarmanVerifyConfig
	VerifyBackup   pgverifybackup.Config
}

type ManifestConfig struct {
	Enabled      bool
	Timeout      time.Duration
	RedactValues []string
}

type BarmanVerifyConfig struct {
	Enabled      bool
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
	return model.ProviderBarman
}

func (a *Adapter) DiscoverBackups(ctx context.Context) (model.BackupCatalog, error) {
	if a.cfg.Server == "" {
		return model.BackupCatalog{Provider: model.ProviderBarman}, fmt.Errorf("barman server is required")
	}

	result, err := a.runner.Run(ctx, command.Invocation{
		Path:         a.binary(),
		Args:         a.listBackupsArgs(),
		Env:          a.cfg.Env,
		WorkDir:      a.cfg.WorkDir,
		Timeout:      a.cfg.Timeout,
		RedactValues: a.cfg.RedactValues,
	})

	catalog := model.BackupCatalog{
		Provider: model.ProviderBarman,
		Evidence: []model.EvidenceRecord{
			commandEvidence(model.ProviderBarman, "list-backups", result.Evidence),
		},
	}
	if err != nil {
		return catalog, fmt.Errorf("run barman list-backups: %w", err)
	}
	if !result.Evidence.ExitStatus.Success {
		return catalog, fmt.Errorf("barman list-backups failed: %s", result.Evidence.ExitStatus.Summary())
	}

	backups, err := ParseBackupList(result.Raw.Stdout, a.cfg.Server)
	if err != nil {
		return catalog, err
	}
	catalog.Backups = backups
	return catalog, nil
}

func (a *Adapter) ValidateCatalog(ctx context.Context, _ model.BackupCatalog, backup model.Backup, _ model.RecoveryTarget) (model.CheckReport, error) {
	if a.cfg.Server == "" {
		return model.CheckReport{}, fmt.Errorf("barman server is required")
	}
	backupID, err := a.backupID(backup)
	if err != nil {
		return model.CheckReport{}, err
	}

	report := model.CheckReport{}
	check, evidence, _ := a.runValidationCommand(ctx, "barman-check", a.checkArgs())
	report.Checks = append(report.Checks, check)
	report.Evidence = append(report.Evidence, evidence)

	check, evidence, _ = a.runValidationCommand(ctx, "barman-check-backup", a.checkBackupArgs(backupID))
	report.Checks = append(report.Checks, check)
	report.Evidence = append(report.Evidence, evidence)

	check, evidence, result := a.runValidationCommand(ctx, "barman-show-backup", a.showBackupArgs(backupID))
	check = enrichShowBackupCheck(check, result.Raw.Stdout, a.cfg.Server)
	report.Checks = append(report.Checks, check)
	report.Evidence = append(report.Evidence, evidence)

	if a.cfg.Manifest.Enabled {
		check, evidence, _ = a.runValidationCommandWith(ctx, "barman-generate-manifest", a.generateManifestArgs(backupID), a.manifestTimeout(), a.manifestRedactions())
		report.Checks = append(report.Checks, check)
		report.Evidence = append(report.Evidence, evidence)
	} else {
		report.Checks = append(report.Checks, model.Check{
			Name:    "barman-generate-manifest",
			Status:  model.CheckStatusSkipped,
			Message: "Barman generate-manifest is not enabled; backup_manifest generation was not run.",
			Attributes: map[string]string{
				"operation": "barman-generate-manifest",
			},
		})
	}

	if a.cfg.BarmanVerify.Enabled {
		check, evidence, _ = a.runValidationCommandWith(ctx, "barman-verify-backup", a.verifyBackupArgs(backupID), a.barmanVerifyTimeout(), a.barmanVerifyRedactions())
		report.Checks = append(report.Checks, check)
		report.Evidence = append(report.Evidence, evidence)
	} else {
		report.Checks = append(report.Checks, model.Check{
			Name:    "barman-verify-backup",
			Status:  model.CheckStatusSkipped,
			Message: "Barman verify-backup is not enabled; manifest-level provider verification was not run.",
			Attributes: map[string]string{
				"operation": "barman-verify-backup",
			},
		})
	}
	return report, nil
}

func (a *Adapter) PlanRestore(_ context.Context, backup model.Backup, target model.RecoveryTarget, spec model.TargetSpec) (model.RestorePlan, error) {
	target = target.Normalized()
	if backup.Provider != "" && backup.Provider != model.ProviderBarman {
		return model.RestorePlan{}, fmt.Errorf("barman cannot restore backup from provider %q", backup.Provider)
	}
	if a.cfg.Server == "" {
		return model.RestorePlan{}, fmt.Errorf("barman server is required")
	}
	if spec.Type != model.RestoreTargetLocal {
		return model.RestorePlan{}, fmt.Errorf("barman restore planning currently supports only local targets")
	}
	if spec.WorkDir == "" {
		return model.RestorePlan{}, fmt.Errorf("target work_dir is required")
	}

	backupID, err := a.backupID(backup)
	if err != nil {
		return model.RestorePlan{}, err
	}
	restoreArgs, err := a.restoreArgs(target, backupID, filepath.Join(spec.WorkDir, "data"))
	if err != nil {
		return model.RestorePlan{}, err
	}

	dataDir := filepath.Join(spec.WorkDir, "data")
	steps := []model.RestoreStep{{
		Name:        "barman-restore",
		Description: "Restore the selected Barman backup into the local target data directory.",
		Command: &model.CommandSpec{
			Tool:       model.ToolBarman,
			Path:       a.binary(),
			Args:       restoreArgs,
			Env:        copyStringMap(a.cfg.Env),
			WorkDir:    a.cfg.WorkDir,
			Timeout:    durationString(a.restoreTimeout()),
			Redactions: append([]string{}, a.cfg.RedactValues...),
		},
		Inputs: map[string]string{
			"backup_id":          backup.ID,
			"provider_backup_id": backup.ProviderID,
			"server":             a.cfg.Server,
		},
		Outputs: map[string]string{
			"data_directory": dataDir,
		},
	}}
	verifyStep, err := a.cfg.VerifyBackup.Step(dataDir)
	if err != nil {
		return model.RestorePlan{}, err
	}
	if verifyStep != nil {
		steps = append(steps, *verifyStep)
	}

	return model.RestorePlan{
		Provider:       model.ProviderBarman,
		BackupID:       backup.ID,
		Target:         spec,
		RecoveryTarget: target,
		Runtime: model.RuntimeConfig{
			DataDirectory: dataDir,
			Environment:   copyStringMap(a.cfg.Env),
		},
		Steps:    steps,
		Evidence: []model.EvidenceRecord{planEvidence("restore-plan")},
	}, nil
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

func (a *Adapter) listBackupsArgs() []string {
	args := []string{}
	if a.cfg.ConfigPath != "" {
		args = append(args, "--config", a.cfg.ConfigPath)
	}
	args = append(args, "--format", "json", "list-backups", a.cfg.Server)
	return args
}

func (a *Adapter) checkArgs() []string {
	args := a.globalArgs()
	args = append(args, "check", a.cfg.Server)
	return args
}

func (a *Adapter) checkBackupArgs(backupID string) []string {
	args := a.globalArgs()
	args = append(args, "check-backup", a.cfg.Server, backupID)
	return args
}

func (a *Adapter) showBackupArgs(backupID string) []string {
	args := a.globalArgs()
	args = append(args, "--format", "json", "show-backup", a.cfg.Server, backupID)
	return args
}

func (a *Adapter) generateManifestArgs(backupID string) []string {
	args := a.globalArgs()
	args = append(args, "generate-manifest", a.cfg.Server, backupID)
	return args
}

func (a *Adapter) verifyBackupArgs(backupID string) []string {
	args := a.globalArgs()
	args = append(args, "verify-backup", a.cfg.Server, backupID)
	return args
}

func (a *Adapter) globalArgs() []string {
	args := []string{}
	if a.cfg.ConfigPath != "" {
		args = append(args, "--config", a.cfg.ConfigPath)
	}
	return args
}

func (a *Adapter) restoreArgs(target model.RecoveryTarget, backupID string, dataDir string) ([]string, error) {
	args := a.globalArgs()
	args = append(args, "restore", "--get-wal")

	targetArgs, err := barmanRecoveryArgs(target)
	if err != nil {
		return nil, err
	}
	args = append(args, targetArgs...)
	args = append(args, a.cfg.Server, backupID, dataDir)
	return args, nil
}

func (a *Adapter) runValidationCommand(ctx context.Context, name string, args []string) (model.Check, model.EvidenceRecord, command.Result) {
	return a.runValidationCommandWith(ctx, name, args, a.cfg.Timeout, a.cfg.RedactValues)
}

func (a *Adapter) runValidationCommandWith(ctx context.Context, name string, args []string, timeout time.Duration, redactions []string) (model.Check, model.EvidenceRecord, command.Result) {
	result, err := a.runner.Run(ctx, command.Invocation{
		Path:         a.binary(),
		Args:         args,
		Env:          a.cfg.Env,
		WorkDir:      a.cfg.WorkDir,
		Timeout:      timeout,
		RedactValues: redactions,
	})
	evidence := commandEvidence(model.ProviderBarman, name, result.Evidence)
	check := model.Check{
		Name:        name,
		Status:      model.CheckStatusPassed,
		EvidenceIDs: []string{evidence.ID},
		Attributes: map[string]string{
			"operation": name,
		},
	}
	if err != nil {
		check.Status = model.CheckStatusFailed
		check.Message = fmt.Sprintf("run %s: %v", name, err)
		return check, evidence, result
	}
	if !result.Evidence.ExitStatus.Success {
		check.Status = model.CheckStatusFailed
		check.Message = fmt.Sprintf("%s failed: %s", name, result.Evidence.ExitStatus.Summary())
	}
	return check, evidence, result
}

func (a *Adapter) barmanVerifyTimeout() time.Duration {
	if a.cfg.BarmanVerify.Timeout > 0 {
		return a.cfg.BarmanVerify.Timeout
	}
	return a.cfg.Timeout
}

func (a *Adapter) barmanVerifyRedactions() []string {
	return append(append([]string{}, a.cfg.RedactValues...), a.cfg.BarmanVerify.RedactValues...)
}

func (a *Adapter) manifestTimeout() time.Duration {
	if a.cfg.Manifest.Timeout > 0 {
		return a.cfg.Manifest.Timeout
	}
	return a.cfg.Timeout
}

func (a *Adapter) manifestRedactions() []string {
	return append(append([]string{}, a.cfg.RedactValues...), a.cfg.Manifest.RedactValues...)
}

func enrichShowBackupCheck(check model.Check, data []byte, defaultServer string) model.Check {
	if check.Status == model.CheckStatusFailed {
		return check
	}
	attributes, err := showBackupAttributes(data, defaultServer)
	if err != nil {
		check.Status = model.CheckStatusWarning
		check.Message = err.Error()
		return check
	}
	for key, value := range attributes {
		check.Attributes[key] = value
	}
	return check
}

func showBackupAttributes(data []byte, defaultServer string) (map[string]string, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("barman show-backup produced no JSON output")
	}

	var root any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil {
		return nil, fmt.Errorf("parse barman show-backup json: %w", err)
	}

	var objects []map[string]any
	collectBackupObjects(root, "", &objects)
	if len(objects) == 0 {
		return nil, fmt.Errorf("barman show-backup JSON did not contain backup metadata")
	}

	object := objects[0]
	attributes := map[string]string{
		"operation": "barman-show-backup",
	}
	addAttribute(attributes, "backup_id", getString(object, "backup_id", "id", "backupId"))
	addAttribute(attributes, "server", firstNonEmpty(getString(object, "server_name", "server", "serverName"), defaultServer))
	addAttribute(attributes, "status", getString(object, "status"))
	addAttribute(attributes, "backup_type", getString(object, "backup_type", "type", "kind"))
	addAttribute(attributes, "begin_wal", getString(object, "begin_wal", "start_wal", "begin_wal_segment"))
	addAttribute(attributes, "end_wal", getString(object, "end_wal", "finish_wal", "end_wal_segment"))
	addAttribute(attributes, "begin_lsn", getString(object, "begin_xlog", "begin_lsn", "start_lsn"))
	addAttribute(attributes, "end_lsn", getString(object, "end_xlog", "end_lsn", "finish_lsn"))
	addAttribute(attributes, "begin_time", getString(object, "begin_time", "start_time", "started_at"))
	addAttribute(attributes, "end_time", getString(object, "end_time", "finish_time", "finished_at"))
	addAttribute(attributes, "postgres_version", getString(object, "postgres_version", "pg_version"))
	addAttribute(attributes, "backup_method", getString(object, "backup_method"))
	addAttribute(attributes, "system_identifier", getString(object, "system_identifier", "systemid"))
	return attributes, nil
}

func barmanRecoveryArgs(target model.RecoveryTarget) ([]string, error) {
	target = target.Normalized()
	if err := target.Validate(); err != nil {
		return nil, err
	}
	args := []string{}
	targeted := false
	switch target.Type {
	case "", model.RecoveryTargetLatest:
	case model.RecoveryTargetImmediate:
		args = append(args, "--target-immediate")
		targeted = true
	case model.RecoveryTargetTimestamp:
		if target.Value == "" {
			return nil, fmt.Errorf("timestamp recovery target requires value")
		}
		args = append(args, "--target-time", target.Value)
		targeted = true
	case model.RecoveryTargetLSN:
		if target.Value == "" {
			return nil, fmt.Errorf("lsn recovery target requires value")
		}
		args = append(args, "--target-lsn", target.Value)
		targeted = true
	case model.RecoveryTargetXID:
		if target.Value == "" {
			return nil, fmt.Errorf("xid recovery target requires value")
		}
		args = append(args, "--target-xid", target.Value)
		targeted = true
	case model.RecoveryTargetRestorePoint:
		if target.Value == "" {
			return nil, fmt.Errorf("restore point recovery target requires value")
		}
		args = append(args, "--target-name", target.Value)
		targeted = true
	default:
		return nil, fmt.Errorf("unsupported recovery target %q", target.Type)
	}
	if target.Timeline != "" {
		args = append(args, "--target-tli", target.Timeline)
	}
	if targeted && target.Inclusive != nil && !*target.Inclusive {
		args = append(args, "--exclusive")
	}
	if targeted {
		args = append(args, "--target-action", "promote")
	}
	return args, nil
}

func (a *Adapter) backupID(backup model.Backup) (string, error) {
	if backup.ProviderID == "" {
		return "", fmt.Errorf("barman backup provider_id is required")
	}
	if backup.ClusterName != "" && backup.ClusterName != a.cfg.Server {
		return "", fmt.Errorf("barman backup belongs to server %q, adapter is configured for %q", backup.ClusterName, a.cfg.Server)
	}
	prefix := a.cfg.Server + "/"
	if strings.HasPrefix(backup.ProviderID, prefix) {
		return strings.TrimPrefix(backup.ProviderID, prefix), nil
	}
	if strings.Contains(backup.ProviderID, "/") {
		return "", fmt.Errorf("barman backup provider_id %q does not match server %q", backup.ProviderID, a.cfg.Server)
	}
	return backup.ProviderID, nil
}

func ParseBackupList(data []byte, defaultServer string) ([]model.Backup, error) {
	var root any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil {
		return nil, fmt.Errorf("parse barman list-backups json: %w", err)
	}

	var objects []map[string]any
	collectBackupObjects(root, "", &objects)

	backups := make([]model.Backup, 0, len(objects))
	for i, object := range objects {
		backup, err := mapBackup(object, defaultServer)
		if err != nil {
			return nil, fmt.Errorf("parse barman backup entry %d: %w", i, err)
		}
		backups = append(backups, backup)
	}
	return backups, nil
}

func mapBackup(object map[string]any, defaultServer string) (model.Backup, error) {
	backupID := getString(object, "backup_id", "id", "backupId")
	if backupID == "" {
		return model.Backup{}, fmt.Errorf("missing backup id")
	}

	server := firstNonEmpty(getString(object, "server_name", "server", "serverName"), defaultServer)
	providerID := backupID
	if server != "" {
		providerID = server + "/" + backupID
	}

	startedAt := getTime(object, "begin_time", "start_time", "started_at")
	finishedAt := getTime(object, "end_time", "finish_time", "finished_at")
	lastModifiedAt := getTime(object, "last_modified", "updated_at")
	parentID := getString(object, "parent_backup_id", "parent_id", "deduplicated_from")

	return model.Backup{
		ID:             model.ProviderScopedID(model.ProviderBarman, providerID),
		Provider:       model.ProviderBarman,
		ProviderID:     providerID,
		ClusterName:    server,
		ParentID:       parentID,
		Kind:           inferBarmanKind(getString(object, "backup_type", "type", "kind"), parentID),
		Status:         mapBarmanStatus(getString(object, "status")),
		StartedAt:      startedAt,
		FinishedAt:     finishedAt,
		LastModifiedAt: lastModifiedAt,
		WALRange: model.WALRange{
			StartSegment: getString(object, "begin_wal", "start_wal", "begin_wal_segment"),
			EndSegment:   getString(object, "end_wal", "finish_wal", "end_wal_segment"),
			StartLSN:     getString(object, "begin_xlog", "begin_lsn", "start_lsn"),
			EndLSN:       getString(object, "end_xlog", "end_lsn", "finish_lsn"),
		},
		PostgreSQLVersion: getString(object, "postgres_version", "pg_version"),
		DataDirectory:     getString(object, "pgdata", "data_directory", "data_dir"),
		Permanent:         getBool(object, "is_permanent", "permanent") || isKept(getString(object, "keep", "keep_status")),
		Metadata:          metadata(object, "backup_name", "system_identifier", "systemid", "backup_method", "retention_policy_status"),
	}, nil
}

func collectBackupObjects(value any, candidateID string, out *[]map[string]any) {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			collectBackupObjects(item, "", out)
		}
	case map[string]any:
		if isBackupObject(typed) {
			object := typed
			if getString(object, "backup_id", "id", "backupId") == "" && candidateID != "" {
				object = copyMap(object)
				object["backup_id"] = candidateID
			}
			*out = append(*out, object)
			return
		}
		for key, item := range typed {
			collectBackupObjects(item, key, out)
		}
	}
}

func isBackupObject(object map[string]any) bool {
	if getString(object, "backup_id", "id", "backupId") != "" {
		return true
	}
	return getString(object, "status") != "" &&
		(getString(object, "begin_time", "start_time", "started_at") != "" ||
			getString(object, "end_time", "finish_time", "finished_at") != "")
}

func mapBarmanStatus(status string) model.BackupStatus {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "DONE", "AVAILABLE":
		return model.BackupStatusAvailable
	case "WAITING_FOR_WALS", "WAITING_FOR_WAL":
		return model.BackupStatusWaitingForWAL
	case "STARTED", "RUNNING":
		return model.BackupStatusRunning
	case "FAILED":
		return model.BackupStatusFailed
	case "":
		return model.BackupStatusUnknown
	default:
		return model.BackupStatusUnknown
	}
}

func inferBarmanKind(kind string, parentID string) model.BackupKind {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "full", "rsync", "snapshot":
		return model.BackupKindFull
	case "incremental", "incr":
		return model.BackupKindIncremental
	case "differential", "diff":
		return model.BackupKindDifferential
	case "logical":
		return model.BackupKindLogical
	}
	if parentID != "" {
		return model.BackupKindIncremental
	}
	return model.BackupKindUnknown
}

func commandEvidence(provider model.ProviderType, operation string, evidence model.CommandEvidence) model.EvidenceRecord {
	collectedAt := evidence.FinishedAt
	if collectedAt.IsZero() {
		collectedAt = time.Now().UTC()
	}

	return model.EvidenceRecord{
		ID:          string(provider) + ":" + operation + ":" + collectedAt.Format(time.RFC3339Nano),
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
		ID:          string(model.ProviderBarman) + ":" + operation + ":" + now.Format(time.RFC3339Nano),
		Kind:        model.EvidencePlan,
		Source:      string(model.ProviderBarman),
		CollectedAt: now,
		Attributes: map[string]string{
			"operation": operation,
		},
	}
}

func getString(object map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := object[key]
		if !ok || value == nil {
			continue
		}
		switch typed := value.(type) {
		case string:
			if typed != "" {
				return typed
			}
		case json.Number:
			return typed.String()
		case float64:
			return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%f", typed), "0"), ".")
		case bool:
			if typed {
				return "true"
			}
			return "false"
		}
	}
	return ""
}

func getBool(object map[string]any, keys ...string) bool {
	for _, key := range keys {
		value, ok := object[key]
		if !ok || value == nil {
			continue
		}
		switch typed := value.(type) {
		case bool:
			return typed
		case string:
			switch strings.ToLower(strings.TrimSpace(typed)) {
			case "true", "yes", "1":
				return true
			case "false", "no", "0":
				return false
			}
		}
	}
	return false
}

func getTime(object map[string]any, keys ...string) *time.Time {
	value := getString(object, keys...)
	if value == "" {
		return nil
	}
	parsed, err := parseTime(value)
	if err != nil {
		return nil
	}
	return &parsed
}

func parseTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05Z07:00",
		"2006-01-02 15:04:05",
	} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported time format %q", value)
}

func isKept(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value != "" && value != "nokeep" && value != "false" && value != "none"
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

func addAttribute(attributes map[string]string, key string, value string) {
	if value != "" {
		attributes[key] = value
	}
}

func copyMap(input map[string]any) map[string]any {
	result := make(map[string]any, len(input)+1)
	for key, value := range input {
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

func durationString(value time.Duration) string {
	if value == 0 {
		return ""
	}
	return value.String()
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
