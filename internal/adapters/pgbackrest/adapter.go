package pgbackrest

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
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

const defaultBinary = "pgbackrest"

type Config struct {
	Binary         string
	ConfigPath     string
	Stanza         string
	Repo           string
	Env            map[string]string
	WorkDir        string
	Timeout        time.Duration
	RestoreTimeout time.Duration
	RedactValues   []string
	Check          CheckConfig
	Verify         VerifyConfig
	VerifyBackup   pgverifybackup.Config
}

type CheckConfig struct {
	Enabled            bool
	Timeout            time.Duration
	NoArchiveCheck     bool
	NoArchiveModeCheck bool
	ArchiveTimeout     time.Duration
	RedactValues       []string
}

type VerifyConfig struct {
	Enabled      bool
	Timeout      time.Duration
	Output       string
	Verbose      bool
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
	return model.ProviderPGBackRest
}

func (a *Adapter) DiscoverBackups(ctx context.Context) (model.BackupCatalog, error) {
	result, err := a.runner.Run(ctx, command.Invocation{
		Path:         a.binary(),
		Args:         a.infoArgs(),
		Env:          a.cfg.Env,
		WorkDir:      a.cfg.WorkDir,
		Timeout:      a.cfg.Timeout,
		RedactValues: a.cfg.RedactValues,
	})

	catalog := model.BackupCatalog{
		Provider: model.ProviderPGBackRest,
		Evidence: []model.EvidenceRecord{
			adapterutil.CommandEvidence(model.ProviderPGBackRest, "info", result.Evidence),
		},
	}
	if err != nil {
		return catalog, fmt.Errorf("run pgbackrest info: %w", err)
	}
	if !result.Evidence.ExitStatus.Success {
		return catalog, fmt.Errorf("pgbackrest info failed: %s", result.Evidence.ExitStatus.Summary())
	}

	backups, err := ParseInfo(result.Raw.Stdout, a.cfg.Stanza)
	if err != nil {
		return catalog, result.RedactError(err)
	}
	backups, err = adapterutil.RedactBackups(backups, result)
	if err != nil {
		return catalog, fmt.Errorf("redact pgbackrest backup catalog: %w", err)
	}
	catalog.Backups = backups
	return catalog, nil
}

func (a *Adapter) ValidateCatalog(ctx context.Context, _ model.BackupCatalog, backup model.Backup, _ model.RecoveryTarget) (model.CheckReport, error) {
	report := model.CheckReport{}
	if a.cfg.Check.Enabled {
		check, evidence, result := a.runValidationCommandWith(ctx, "pgbackrest-check", "check", a.checkArgs(), a.checkTimeout(), a.checkRedactions())
		report.Evidence = append(report.Evidence, evidence)
		check, err := adapterutil.RedactCheck(check, result)
		if err != nil {
			return report, fmt.Errorf("redact pgbackrest-check result: %w", result.RedactError(err))
		}
		report.Checks = append(report.Checks, check)
	} else {
		report.Checks = append(report.Checks, model.Check{
			Name:    "pgbackrest-check",
			Status:  model.CheckStatusSkipped,
			Message: "pgBackRest check is not enabled; archive configuration validation was not run.",
			Attributes: map[string]string{
				"operation": "pgbackrest-check",
			},
		})
	}

	if a.cfg.Verify.Enabled {
		label, stanza, err := a.backupLabel(backup)
		if err != nil {
			return report, err
		}
		args, err := a.verifyArgs(label, stanza)
		if err != nil {
			return report, err
		}
		check, evidence, result := a.runValidationCommandWith(ctx, "pgbackrest-verify", "verify", args, a.verifyTimeout(), a.verifyRedactions())
		report.Evidence = append(report.Evidence, evidence)
		check, err = adapterutil.RedactCheck(check, result)
		if err != nil {
			return report, fmt.Errorf("redact pgbackrest-verify result: %w", result.RedactError(err))
		}
		report.Checks = append(report.Checks, check)
	} else {
		report.Checks = append(report.Checks, model.Check{
			Name:    "pgbackrest-verify",
			Status:  model.CheckStatusSkipped,
			Message: "pgBackRest verify is not enabled; repository-level backup verification was not run.",
			Attributes: map[string]string{
				"operation": "pgbackrest-verify",
			},
		})
	}

	return report, nil
}

func (a *Adapter) PlanRestore(_ context.Context, backup model.Backup, target model.RecoveryTarget, spec model.TargetSpec) (model.RestorePlan, error) {
	target = target.Normalized()
	if backup.Provider != "" && backup.Provider != model.ProviderPGBackRest {
		return model.RestorePlan{}, fmt.Errorf("pgbackrest cannot restore backup from provider %q", backup.Provider)
	}
	if spec.Type != model.RestoreTargetLocal {
		return model.RestorePlan{}, fmt.Errorf("pgbackrest restore planning currently supports only local targets")
	}
	if spec.WorkDir == "" {
		return model.RestorePlan{}, fmt.Errorf("target work_dir is required")
	}

	label, stanza, err := a.backupLabel(backup)
	if err != nil {
		return model.RestorePlan{}, err
	}

	dataDir := filepath.Join(spec.WorkDir, "data")
	restoreArgs, err := a.restoreArgs(target, label, stanza, dataDir)
	if err != nil {
		return model.RestorePlan{}, err
	}

	steps := []model.RestoreStep{{
		Name:        "pgbackrest-restore",
		Description: "Restore the selected pgBackRest backup into the local target data directory.",
		Command: &model.CommandSpec{
			Tool:       model.ToolPGBackRest,
			Path:       a.binary(),
			Args:       restoreArgs,
			Env:        adapterutil.CloneStringMap(a.cfg.Env),
			WorkDir:    a.cfg.WorkDir,
			Timeout:    adapterutil.DurationString(a.restoreTimeout()),
			Redactions: append([]string{}, a.cfg.RedactValues...),
		},
		Inputs: map[string]string{
			"backup_id":          backup.ID,
			"provider_backup_id": backup.ProviderID,
			"backup_label":       label,
			"stanza":             stanza,
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
		Provider:       model.ProviderPGBackRest,
		BackupID:       backup.ID,
		Target:         spec,
		RecoveryTarget: target,
		Runtime: model.RuntimeConfig{
			DataDirectory: dataDir,
			Environment:   adapterutil.CloneStringMap(a.cfg.Env),
		},
		Steps:    steps,
		Evidence: []model.EvidenceRecord{adapterutil.PlanEvidence(model.ProviderPGBackRest, "restore-plan")},
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

func (a *Adapter) infoArgs() []string {
	args := a.globalArgs(a.cfg.Stanza)
	args = append(args, "info", "--output=json")
	return args
}

func (a *Adapter) checkArgs() []string {
	args := a.globalArgs(a.cfg.Stanza)
	args = append(args, "check")
	if a.cfg.Check.NoArchiveCheck {
		args = append(args, "--no-archive-check")
	}
	if a.cfg.Check.NoArchiveModeCheck {
		args = append(args, "--no-archive-mode-check")
	}
	if a.cfg.Check.ArchiveTimeout > 0 {
		args = append(args, "--archive-timeout="+durationSeconds(a.cfg.Check.ArchiveTimeout))
	}
	return args
}

func (a *Adapter) verifyArgs(label string, stanza string) ([]string, error) {
	output := strings.TrimSpace(a.cfg.Verify.Output)
	if output == "" {
		output = "text"
	}
	switch output {
	case "none", "text":
	default:
		return nil, fmt.Errorf("unsupported pgbackrest verify output %q", output)
	}

	args := a.globalArgs(stanza)
	args = append(args, "verify", "--set="+label, "--output="+output)
	if a.cfg.Verify.Verbose {
		args = append(args, "--verbose")
	}
	return args, nil
}

func (a *Adapter) checkTimeout() time.Duration {
	if a.cfg.Check.Timeout > 0 {
		return a.cfg.Check.Timeout
	}
	return a.cfg.Timeout
}

func (a *Adapter) checkRedactions() []string {
	return append(append([]string{}, a.cfg.RedactValues...), a.cfg.Check.RedactValues...)
}

func (a *Adapter) verifyTimeout() time.Duration {
	if a.cfg.Verify.Timeout > 0 {
		return a.cfg.Verify.Timeout
	}
	return a.cfg.Timeout
}

func (a *Adapter) verifyRedactions() []string {
	return append(append([]string{}, a.cfg.RedactValues...), a.cfg.Verify.RedactValues...)
}

func (a *Adapter) runValidationCommandWith(ctx context.Context, name string, operation string, args []string, timeout time.Duration, redactions []string) (model.Check, model.EvidenceRecord, command.Result) {
	result, err := a.runner.Run(ctx, command.Invocation{
		Path:         a.binary(),
		Args:         args,
		Env:          a.cfg.Env,
		WorkDir:      a.cfg.WorkDir,
		Timeout:      timeout,
		RedactValues: redactions,
	})
	evidence := adapterutil.CommandEvidence(model.ProviderPGBackRest, operation, result.Evidence)
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
		check.Message = fmt.Sprintf("run %s: %v", name, result.RedactError(err))
		return check, evidence, result
	}
	if !result.Evidence.ExitStatus.Success {
		check.Status = model.CheckStatusFailed
		check.Message = fmt.Sprintf("%s failed: %s", name, result.Evidence.ExitStatus.Summary())
	}
	return check, evidence, result
}

func (a *Adapter) globalArgs(stanza string) []string {
	args := []string{}
	if a.cfg.ConfigPath != "" {
		args = append(args, "--config="+a.cfg.ConfigPath)
	}
	if stanza != "" {
		args = append(args, "--stanza="+stanza)
	}
	if a.cfg.Repo != "" {
		args = append(args, "--repo="+a.cfg.Repo)
	}
	return args
}

func (a *Adapter) restoreArgs(target model.RecoveryTarget, label string, stanza string, dataDir string) ([]string, error) {
	args := a.globalArgs(stanza)
	args = append(args, "restore", "--set="+label, "--pg1-path="+dataDir, "--reset-pg1-host")

	targetArgs, err := pgBackRestRecoveryArgs(target)
	if err != nil {
		return nil, err
	}
	args = append(args, targetArgs...)
	return args, nil
}

func pgBackRestRecoveryArgs(target model.RecoveryTarget) ([]string, error) {
	target = target.Normalized()
	if err := target.Validate(); err != nil {
		return nil, err
	}
	args := []string{}
	targeted := false
	switch target.Type {
	case "", model.RecoveryTargetLatest:
	case model.RecoveryTargetImmediate:
		args = append(args, "--type=immediate")
		targeted = true
	case model.RecoveryTargetTimestamp:
		if target.Value == "" {
			return nil, fmt.Errorf("timestamp recovery target requires value")
		}
		timestamp, err := target.Timestamp()
		if err != nil {
			return nil, err
		}
		nativeTimestamp := timestamp.UTC().Format("2006-01-02 15:04:05.999999999-07:00")
		args = append(args, "--type=time", "--target="+nativeTimestamp)
		targeted = true
	case model.RecoveryTargetLSN:
		if target.Value == "" {
			return nil, fmt.Errorf("lsn recovery target requires value")
		}
		args = append(args, "--type=lsn", "--target="+target.Value)
		targeted = true
	case model.RecoveryTargetXID:
		if target.Value == "" {
			return nil, fmt.Errorf("xid recovery target requires value")
		}
		args = append(args, "--type=xid", "--target="+target.Value)
		targeted = true
	case model.RecoveryTargetRestorePoint:
		if target.Value == "" {
			return nil, fmt.Errorf("restore point recovery target requires value")
		}
		args = append(args, "--type=name", "--target="+target.Value)
		targeted = true
	default:
		return nil, fmt.Errorf("unsupported recovery target %q", target.Type)
	}
	if target.Timeline != "" {
		args = append(args, "--target-timeline="+target.Timeline)
	}
	if targeted && target.Inclusive != nil && !*target.Inclusive {
		args = append(args, "--target-exclusive")
	}
	if targeted {
		args = append(args, "--target-action=pause")
	}
	return args, nil
}

func (a *Adapter) backupLabel(backup model.Backup) (label string, stanza string, err error) {
	if backup.Provider != "" && backup.Provider != model.ProviderPGBackRest {
		return "", "", fmt.Errorf("pgbackrest backup belongs to provider %q", backup.Provider)
	}
	if backup.ProviderID == "" {
		return "", "", fmt.Errorf("pgbackrest backup provider_id is required")
	}

	label = backup.ProviderID
	stanza = backup.ClusterName
	if before, after, ok := strings.Cut(backup.ProviderID, "/"); ok {
		if after == "" {
			return "", "", fmt.Errorf("pgbackrest backup provider_id %q is missing backup label", backup.ProviderID)
		}
		if before != "" {
			if stanza != "" && stanza != before {
				return "", "", fmt.Errorf("pgbackrest backup cluster %q does not match provider_id stanza %q", stanza, before)
			}
			stanza = before
		}
		label = after
	}
	if label == "" {
		return "", "", fmt.Errorf("pgbackrest backup provider_id is missing backup label")
	}
	if a.cfg.Stanza != "" {
		if stanza != "" && stanza != a.cfg.Stanza {
			return "", "", fmt.Errorf("pgbackrest backup belongs to stanza %q, adapter is configured for %q", stanza, a.cfg.Stanza)
		}
		stanza = a.cfg.Stanza
	}
	if stanza == "" {
		return "", "", fmt.Errorf("pgbackrest stanza is required")
	}
	return label, stanza, nil
}

func durationSeconds(duration time.Duration) string {
	seconds := int64(duration / time.Second)
	if seconds <= 0 {
		seconds = 1
	}
	return strconv.FormatInt(seconds, 10)
}

func ParseInfo(data []byte, defaultStanza string) ([]model.Backup, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("pgbackrest info produced no JSON output")
	}

	var root any
	if err := jsonutil.DecodeOne(data, &root); err != nil {
		return nil, fmt.Errorf("parse pgbackrest info json: %w", err)
	}

	stanzas, err := collectStanzas(root)
	if err != nil {
		return nil, err
	}

	backups := []model.Backup{}
	for i, stanza := range stanzas {
		stanzaNameValue, _, err := adapterutil.OptionalStringAlias(stanza, "stanza name", "name", "stanza")
		if err != nil {
			return nil, fmt.Errorf("parse pgbackrest stanza %d: %w", i, err)
		}
		stanzaName := firstNonEmpty(stanzaNameValue, defaultStanza)
		dbVersions := databaseVersions(stanza)
		dbSystemIDs := databaseSystemIDs(stanza)
		stanzaStatus, stanzaHealthy, err := statusMessage(stanza["status"])
		if err != nil {
			return nil, fmt.Errorf("parse pgbackrest stanza %d: %w", i, err)
		}

		backupValue, found := stanza["backup"]
		if !found {
			return nil, fmt.Errorf("parse pgbackrest stanza %d: missing backup array", i)
		}
		backupValues, ok := backupValue.([]any)
		if !ok {
			return nil, fmt.Errorf("parse pgbackrest stanza %d: backup must be an array", i)
		}
		if len(backupValues) > model.MaxBackupsPerCatalog-len(backups) {
			return nil, fmt.Errorf(
				"parse pgbackrest info: backups exceed maximum count %d",
				model.MaxBackupsPerCatalog,
			)
		}
		for j, value := range backupValues {
			object, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("parse pgbackrest stanza %d backup %d: expected object", i, j)
			}
			backup, err := mapBackup(
				object,
				stanzaName,
				stanzaStatus,
				stanzaHealthy,
				dbVersions,
				dbSystemIDs,
			)
			if err != nil {
				return nil, fmt.Errorf("parse pgbackrest stanza %d backup %d: %w", i, j, err)
			}
			backups = append(backups, backup)
		}
	}
	return backups, nil
}

func collectStanzas(root any) ([]map[string]any, error) {
	switch typed := root.(type) {
	case []any:
		if len(typed) > model.MaxBackupsPerCatalog {
			return nil, fmt.Errorf(
				"parse pgbackrest info: stanzas exceed maximum count %d",
				model.MaxBackupsPerCatalog,
			)
		}
		stanzas := make([]map[string]any, 0, len(typed))
		for i, value := range typed {
			object, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("parse pgbackrest info stanza %d: expected object", i)
			}
			stanzas = append(stanzas, object)
		}
		return stanzas, nil
	case map[string]any:
		return []map[string]any{typed}, nil
	default:
		return nil, fmt.Errorf("pgbackrest info JSON must be an object or array")
	}
}

func mapBackup(
	object map[string]any,
	stanzaName string,
	stanzaStatus string,
	stanzaHealthy bool,
	dbVersions map[string]string,
	dbSystemIDs map[string]string,
) (model.Backup, error) {
	label, err := adapterutil.RequiredStringAlias(object, "backup label", "label", "set")
	if err != nil {
		return model.Backup{}, err
	}
	prior, err := optionalNullableStringField(object, "prior backup label", "prior")
	if err != nil {
		return model.Backup{}, err
	}
	backupType, _, err := adapterutil.OptionalStringAlias(object, "backup type", "type")
	if err != nil {
		return model.Backup{}, err
	}
	failed, errorPresent, err := adapterutil.OptionalBoolAlias(object, "backup error", "error")
	if err != nil {
		return model.Backup{}, err
	}

	providerID := label
	if stanzaName != "" {
		providerID = stanzaName + "/" + label
	}

	dbID := nestedString(object, "database", "id")
	startedAt, err := nestedTime(object, "timestamp", "start")
	if err != nil {
		return model.Backup{}, fmt.Errorf("backup start time: %w", err)
	}
	finishedAt, err := nestedTime(object, "timestamp", "stop")
	if err != nil {
		return model.Backup{}, fmt.Errorf("backup finish time: %w", err)
	}
	status := model.BackupStatusFailed
	if stanzaHealthy && errorPresent && !failed {
		status = model.BackupStatusAvailable
	}

	metadata := map[string]string{}
	addMetadata(metadata, "stanza_status", stanzaStatus)
	addMetadata(metadata, "repo_key", nestedString(object, "database", "repo-key"))
	addMetadata(metadata, "database_id", dbID)
	addMetadata(metadata, "reference_total", numberString(object["reference-total"]))
	addMetadata(metadata, "repository_size", nestedString(object, "info", "repository", "size"))
	addMetadata(metadata, "backup_size", nestedString(object, "info", "size"))

	return model.Backup{
		ID:             model.ProviderScopedID(model.ProviderPGBackRest, providerID),
		Provider:       model.ProviderPGBackRest,
		ProviderID:     providerID,
		ClusterName:    stanzaName,
		ParentID:       prior,
		Kind:           mapBackupKind(backupType),
		Status:         status,
		StartedAt:      startedAt,
		FinishedAt:     finishedAt,
		LastModifiedAt: finishedAt,
		WALRange: model.WALRange{
			StartSegment: nestedString(object, "archive", "start"),
			EndSegment:   nestedString(object, "archive", "stop"),
			StartLSN:     nestedString(object, "lsn", "start"),
			EndLSN:       nestedString(object, "lsn", "stop"),
		},
		PostgreSQLVersion: databaseValue(dbVersions, dbID),
		Permanent:         false,
		Metadata:          adapterutil.StringMapOrNil(withSystemID(metadata, databaseValue(dbSystemIDs, dbID))),
	}, nil
}

func optionalNullableStringField(
	object map[string]any,
	name string,
	key string,
) (string, error) {
	value, found := object[key]
	if !found || value == nil {
		return "", nil
	}
	typed, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s field %q must be a string or null", name, key)
	}
	return typed, nil
}

func databaseVersions(stanza map[string]any) map[string]string {
	return databaseField(stanza, "version")
}

func databaseSystemIDs(stanza map[string]any) map[string]string {
	return databaseField(stanza, "system-id")
}

func databaseField(stanza map[string]any, field string) map[string]string {
	values := map[string]string{}
	dbValues, ok := stanza["db"].([]any)
	if !ok {
		return values
	}
	for _, value := range dbValues {
		object, ok := value.(map[string]any)
		if !ok {
			continue
		}
		id := getString(object, "id")
		fieldValue := getString(object, field)
		if id != "" && fieldValue != "" {
			values[id] = fieldValue
		}
	}
	return values
}

func statusMessage(value any) (string, bool, error) {
	if value == nil {
		return "", false, nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return "", false, fmt.Errorf("stanza status must be an object")
	}
	message, found, err := adapterutil.OptionalStringAlias(object, "stanza status message", "message")
	if err != nil {
		return "", false, err
	}
	code, found := object["code"]
	if !found || code == nil {
		return strings.TrimSpace(message), false, nil
	}
	switch code.(type) {
	case json.Number, float64:
		codeText := numberString(code)
		codeValue, err := strconv.ParseInt(codeText, 10, 64)
		if err != nil {
			return "", false, fmt.Errorf("stanza status code must be an integer: %w", err)
		}
		if strings.TrimSpace(message) == "" {
			message = codeText
		}
		return message, codeValue == 0, nil
	default:
		return "", false, fmt.Errorf("stanza status code must be a number")
	}
}

func mapBackupKind(kind string) model.BackupKind {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "full":
		return model.BackupKindFull
	case "diff", "differential":
		return model.BackupKindDifferential
	case "incr", "incremental":
		return model.BackupKindIncremental
	default:
		return model.BackupKindUnknown
	}
}

func nestedString(object map[string]any, keys ...string) string {
	value := nestedValue(object, keys...)
	return numberString(value)
}

func nestedTime(object map[string]any, keys ...string) (*time.Time, error) {
	value := nestedValue(object, keys...)
	parsed, ok, err := parseTimeValue(value)
	if err != nil {
		return nil, fmt.Errorf("field %q: %w", strings.Join(keys, "."), err)
	}
	if !ok {
		return nil, nil
	}
	return &parsed, nil
}

func nestedValue(object map[string]any, keys ...string) any {
	var value any = object
	for _, key := range keys {
		current, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		value = current[key]
	}
	return value
}

func getString(object map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := numberString(object[key]); value != "" {
			return value
		}
	}
	return ""
}

func numberString(value any) string {
	if value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	case float64:
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%f", typed), "0"), ".")
	case bool:
		if typed {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}

func parseTimeValue(value any) (time.Time, bool, error) {
	switch typed := value.(type) {
	case nil:
		return time.Time{}, false, nil
	case json.Number:
		seconds, err := strconv.ParseInt(typed.String(), 10, 64)
		if err != nil || seconds < 0 {
			return time.Time{}, false, fmt.Errorf("invalid epoch timestamp %q", typed)
		}
		return time.Unix(seconds, 0).UTC(), true, nil
	case float64:
		if typed < 0 || math.Trunc(typed) != typed {
			return time.Time{}, false, fmt.Errorf("invalid epoch timestamp %v", typed)
		}
		return time.Unix(int64(typed), 0).UTC(), true, nil
	case string:
		typed = strings.TrimSpace(typed)
		if typed == "" {
			return time.Time{}, false, nil
		}
		if seconds, err := strconv.ParseInt(typed, 10, 64); err == nil {
			if seconds < 0 {
				return time.Time{}, false, fmt.Errorf("invalid epoch timestamp %q", typed)
			}
			return time.Unix(seconds, 0).UTC(), true, nil
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05Z07:00", "2006-01-02 15:04:05"} {
			if parsed, err := time.Parse(layout, typed); err == nil {
				return parsed.UTC(), true, nil
			}
		}
		return time.Time{}, false, fmt.Errorf("unsupported time format %q", typed)
	default:
		return time.Time{}, false, fmt.Errorf("timestamp must be a number or string")
	}
}

func addMetadata(metadata map[string]string, key, value string) {
	if value != "" {
		metadata[key] = value
	}
}

func withSystemID(metadata map[string]string, systemID string) map[string]string {
	addMetadata(metadata, "system_identifier", systemID)
	return metadata
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func databaseValue(values map[string]string, databaseID string) string {
	if databaseID != "" {
		return values[databaseID]
	}
	if len(values) != 1 {
		return ""
	}
	for _, value := range values {
		return value
	}
	return ""
}
