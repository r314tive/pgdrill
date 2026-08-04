package barman

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
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
			adapterutil.CommandEvidence(model.ProviderBarman, "list-backups", result.Evidence),
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
		return catalog, result.RedactError(err)
	}
	backups, err = adapterutil.RedactBackups(backups, result)
	if err != nil {
		return catalog, fmt.Errorf("redact barman backup catalog: %w", err)
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
	check, evidence, result := a.runValidationCommand(ctx, "barman-check", a.checkArgs())
	report.Evidence = append(report.Evidence, evidence)
	check, err = redactValidationCheck(check, result)
	if err != nil {
		return report, fmt.Errorf("redact barman-check result: %w", result.RedactError(err))
	}
	report.Checks = append(report.Checks, check)

	check, evidence, result = a.runValidationCommand(ctx, "barman-check-backup", a.checkBackupArgs(backupID))
	report.Evidence = append(report.Evidence, evidence)
	check, err = redactValidationCheck(check, result)
	if err != nil {
		return report, fmt.Errorf("redact barman-check-backup result: %w", result.RedactError(err))
	}
	report.Checks = append(report.Checks, check)

	check, evidence, result = a.runValidationCommand(ctx, "barman-show-backup", a.showBackupArgs(backupID))
	check = enrichShowBackupCheck(check, result.Raw.Stdout, a.cfg.Server, backupID)
	report.Evidence = append(report.Evidence, evidence)
	check, err = redactValidationCheck(check, result)
	if err != nil {
		return report, fmt.Errorf("redact barman-show-backup result: %w", result.RedactError(err))
	}
	report.Checks = append(report.Checks, check)

	if a.cfg.Manifest.Enabled {
		check, evidence, result = a.runValidationCommandWith(ctx, "barman-generate-manifest", a.generateManifestArgs(backupID), a.manifestTimeout(), a.manifestRedactions())
		check = acceptExistingManifest(check, result, a.cfg.BarmanVerify.Enabled)
		report.Evidence = append(report.Evidence, evidence)
		check, err = redactValidationCheck(check, result)
		if err != nil {
			return report, fmt.Errorf("redact barman-generate-manifest result: %w", result.RedactError(err))
		}
		report.Checks = append(report.Checks, check)
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
		check, evidence, result = a.runValidationCommandWith(ctx, "barman-verify-backup", a.verifyBackupArgs(backupID), a.barmanVerifyTimeout(), a.barmanVerifyRedactions())
		report.Evidence = append(report.Evidence, evidence)
		check, err = redactValidationCheck(check, result)
		if err != nil {
			return report, fmt.Errorf("redact barman-verify-backup result: %w", result.RedactError(err))
		}
		report.Checks = append(report.Checks, check)
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

func acceptExistingManifest(check model.Check, result command.Result, verificationEnabled bool) model.Check {
	if check.Status != model.CheckStatusFailed || !verificationEnabled {
		return check
	}
	if result.Evidence.ExitStatus.ExitCode != 1 ||
		!bytes.Contains(result.Raw.Stderr, []byte("backup_manifest already exists")) {
		return check
	}
	check.Status = model.CheckStatusPassed
	check.Message = "backup_manifest already exists; barman-verify-backup must validate the retained manifest"
	check.Attributes["manifest_state"] = "existing"
	return check
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
			Env:        adapterutil.CloneStringMap(a.cfg.Env),
			WorkDir:    a.cfg.WorkDir,
			Timeout:    adapterutil.DurationString(a.restoreTimeout()),
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
			Environment:   adapterutil.CloneStringMap(a.cfg.Env),
		},
		Steps:    steps,
		Evidence: []model.EvidenceRecord{adapterutil.PlanEvidence(model.ProviderBarman, "restore-plan")},
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
	evidence := adapterutil.CommandEvidence(model.ProviderBarman, name, result.Evidence)
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

func redactValidationCheck(check model.Check, result command.Result) (model.Check, error) {
	return adapterutil.RedactCheck(check, result)
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

func enrichShowBackupCheck(
	check model.Check,
	data []byte,
	expectedServer string,
	expectedBackupID string,
) model.Check {
	if check.Status == model.CheckStatusFailed {
		return check
	}
	attributes, err := showBackupAttributes(data)
	if err != nil {
		check.Status = model.CheckStatusFailed
		check.Message = err.Error()
		return check
	}
	for key, value := range attributes {
		check.Attributes[key] = value
	}
	switch {
	case attributes["server"] != expectedServer:
		check.Status = model.CheckStatusFailed
		check.Message = fmt.Sprintf(
			"barman show-backup server %q does not match requested server %q",
			attributes["server"],
			expectedServer,
		)
	case attributes["backup_id"] != expectedBackupID:
		check.Status = model.CheckStatusFailed
		check.Message = fmt.Sprintf(
			"barman show-backup backup id %q does not match requested backup %q",
			attributes["backup_id"],
			expectedBackupID,
		)
	case mapBarmanStatus(attributes["status"]) != model.BackupStatusAvailable:
		check.Status = model.CheckStatusFailed
		check.Message = fmt.Sprintf(
			"barman show-backup status %q is not an available terminal status",
			attributes["status"],
		)
	}
	return check
}

func showBackupAttributes(data []byte) (map[string]string, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("barman show-backup produced no JSON output")
	}

	var root any
	if err := jsonutil.DecodeOne(data, &root); err != nil {
		return nil, fmt.Errorf("parse barman show-backup json: %w", err)
	}
	rootObject, ok := root.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("barman show-backup JSON must be a backup object")
	}
	object, envelopeServer, err := showBackupObject(rootObject)
	if err != nil {
		return nil, err
	}
	backupID, err := adapterutil.RequiredStringAlias(object, "backup id", "backup_id", "id", "backupId")
	if err != nil {
		return nil, err
	}
	server, serverFound, err := adapterutil.OptionalStringAlias(object, "server", "server_name", "server", "serverName")
	if err != nil {
		return nil, err
	}
	if envelopeServer != "" {
		if serverFound && server != envelopeServer {
			return nil, fmt.Errorf(
				"barman show-backup server field %q conflicts with envelope server %q",
				server,
				envelopeServer,
			)
		}
		server = envelopeServer
	}
	if strings.TrimSpace(server) == "" {
		return nil, fmt.Errorf("missing server")
	}
	status, err := adapterutil.RequiredStringAlias(object, "status", "status")
	if err != nil {
		return nil, err
	}
	backupType, _, err := adapterutil.OptionalStringAlias(object, "backup type", "backup_type", "type", "kind")
	if err != nil {
		return nil, err
	}
	baseBackupInformation, err := optionalObject(object, "base_backup_information")
	if err != nil {
		return nil, err
	}

	attributes := map[string]string{
		"operation": "barman-show-backup",
		"backup_id": backupID,
		"server":    server,
		"status":    status,
	}
	addAttribute(attributes, "backup_type", backupType)
	metadata := []struct {
		attribute string
		name      string
		nested    map[string]any
		keys      []string
	}{
		{attribute: "begin_wal", name: "begin WAL", nested: baseBackupInformation, keys: []string{"begin_wal", "start_wal", "begin_wal_segment"}},
		{attribute: "end_wal", name: "end WAL", nested: baseBackupInformation, keys: []string{"end_wal", "finish_wal", "end_wal_segment"}},
		{attribute: "begin_lsn", name: "begin LSN", nested: baseBackupInformation, keys: []string{"begin_xlog", "begin_lsn", "start_lsn"}},
		{attribute: "end_lsn", name: "end LSN", nested: baseBackupInformation, keys: []string{"end_xlog", "end_lsn", "finish_lsn"}},
		{attribute: "begin_time", name: "begin time", nested: baseBackupInformation, keys: []string{"begin_time", "start_time", "started_at"}},
		{attribute: "end_time", name: "end time", nested: baseBackupInformation, keys: []string{"end_time", "finish_time", "finished_at"}},
		{attribute: "backup_method", name: "backup method", nested: baseBackupInformation, keys: []string{"backup_method"}},
		{attribute: "postgres_version", name: "PostgreSQL version", keys: []string{"postgresql_version", "postgres_version", "pg_version"}},
		{attribute: "system_identifier", name: "system identifier", keys: []string{"system_identifier", "system_id", "systemid"}},
	}
	for _, field := range metadata {
		value, err := showBackupValue(object, field.nested, field.name, field.keys)
		if err != nil {
			return nil, err
		}
		addAttribute(attributes, field.attribute, value)
	}
	return attributes, nil
}

func showBackupObject(root map[string]any) (map[string]any, string, error) {
	if hasScalarKey(
		root,
		"backup_id", "id", "backupId",
		"server_name", "server", "serverName",
		"status",
	) {
		return root, "", nil
	}

	serverNames := make([]string, 0, len(root))
	for name, value := range root {
		if name == "_WARNING" || value == nil {
			continue
		}
		if _, ok := value.(map[string]any); ok {
			serverNames = append(serverNames, name)
		}
	}
	sort.Strings(serverNames)
	switch len(serverNames) {
	case 0:
		return root, "", nil
	case 1:
		return root[serverNames[0]].(map[string]any), serverNames[0], nil
	default:
		return nil, "", fmt.Errorf(
			"barman show-backup JSON contains multiple server backup objects: %s",
			strings.Join(serverNames, ", "),
		)
	}
}

func hasScalarKey(object map[string]any, keys ...string) bool {
	for _, key := range keys {
		value, ok := object[key]
		if !ok {
			continue
		}
		if _, nested := value.(map[string]any); !nested {
			return true
		}
	}
	return false
}

func optionalObject(object map[string]any, key string) (map[string]any, error) {
	value, ok := object[key]
	if !ok || value == nil {
		return nil, nil
	}
	typed, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("barman show-backup field %q must be an object", key)
	}
	return typed, nil
}

func showBackupValue(
	object map[string]any,
	baseBackupInformation map[string]any,
	name string,
	keys []string,
) (string, error) {
	topLevel, err := consistentString(object, name, keys...)
	if err != nil {
		return "", err
	}
	nested, err := consistentString(baseBackupInformation, name, keys...)
	if err != nil {
		return "", err
	}
	if topLevel != "" && nested != "" && topLevel != nested {
		return "", fmt.Errorf(
			"barman show-backup %s conflicts with base_backup_information",
			name,
		)
	}
	return adapterutil.FirstNonEmpty(topLevel, nested), nil
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
		args = append(args, "--target-action", "pause")
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
		backupID := strings.TrimPrefix(backup.ProviderID, prefix)
		if backupID == "" || strings.Contains(backupID, "/") {
			return "", fmt.Errorf("invalid barman backup provider_id %q", backup.ProviderID)
		}
		return backupID, nil
	}
	if strings.Contains(backup.ProviderID, "/") {
		return "", fmt.Errorf("barman backup provider_id %q does not match server %q", backup.ProviderID, a.cfg.Server)
	}
	return backup.ProviderID, nil
}

func ParseBackupList(data []byte, defaultServer string) ([]model.Backup, error) {
	var root any
	if err := jsonutil.DecodeOne(data, &root); err != nil {
		return nil, fmt.Errorf("parse barman list-backups json: %w", err)
	}
	rootObject, ok := root.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("parse barman list-backups json: expected server-keyed object")
	}
	objects, server, err := backupListObjects(rootObject, defaultServer)
	if err != nil {
		return nil, fmt.Errorf("parse barman list-backups json: %w", err)
	}

	backups := make([]model.Backup, 0, len(objects))
	for i, object := range objects {
		backup, err := mapBackup(object, server)
		if err != nil {
			return nil, fmt.Errorf("parse barman backup entry %d: %w", i, err)
		}
		backups = append(backups, backup)
	}
	return backups, nil
}

func mapBackup(object map[string]any, defaultServer string) (model.Backup, error) {
	backupID, err := adapterutil.RequiredStringAlias(object, "backup id", "backup_id", "id", "backupId")
	if err != nil {
		return model.Backup{}, err
	}
	serverValue, _, err := adapterutil.OptionalStringAlias(object, "server", "server_name", "server", "serverName")
	if err != nil {
		return model.Backup{}, err
	}
	parentID, _, err := adapterutil.OptionalStringAlias(
		object,
		"parent backup id",
		"parent_backup_id",
		"parent_id",
		"deduplicated_from",
	)
	if err != nil {
		return model.Backup{}, err
	}
	status, err := adapterutil.RequiredStringAlias(object, "status", "status")
	if err != nil {
		return model.Backup{}, err
	}
	backupType, _, err := adapterutil.OptionalStringAlias(object, "backup type", "backup_type", "type", "kind")
	if err != nil {
		return model.Backup{}, err
	}
	permanent, _, err := adapterutil.OptionalBoolAlias(object, "permanent", "is_permanent", "permanent")
	if err != nil {
		return model.Backup{}, err
	}
	keep, _, err := adapterutil.OptionalStringAlias(object, "keep status", "keep", "keep_status")
	if err != nil {
		return model.Backup{}, err
	}

	server := adapterutil.FirstNonEmpty(serverValue, defaultServer)
	providerID := backupID
	if server != "" {
		providerID = server + "/" + backupID
	}

	startedAt, err := getTime(object,
		"begin_time_timestamp", "start_time_timestamp", "started_at_timestamp",
		"begin_time", "start_time", "started_at",
	)
	if err != nil {
		return model.Backup{}, fmt.Errorf("backup start time: %w", err)
	}
	finishedAt, err := getTime(object,
		"end_time_timestamp", "finish_time_timestamp", "finished_at_timestamp",
		"end_time", "finish_time", "finished_at",
	)
	if err != nil {
		return model.Backup{}, fmt.Errorf("backup finish time: %w", err)
	}
	lastModifiedAt, err := getTime(object,
		"last_modified_timestamp", "updated_at_timestamp", "last_modified", "updated_at",
	)
	if err != nil {
		return model.Backup{}, fmt.Errorf("backup last modified time: %w", err)
	}
	startSegment, err := consistentString(
		object,
		"start WAL segment",
		"begin_wal",
		"start_wal",
		"begin_wal_segment",
	)
	if err != nil {
		return model.Backup{}, err
	}
	endSegment, err := consistentString(
		object,
		"end WAL segment",
		"end_wal",
		"finish_wal",
		"end_wal_segment",
	)
	if err != nil {
		return model.Backup{}, err
	}
	startLSN, err := consistentString(
		object,
		"start LSN",
		"begin_xlog",
		"begin_lsn",
		"start_lsn",
	)
	if err != nil {
		return model.Backup{}, err
	}
	endLSN, err := consistentString(
		object,
		"end LSN",
		"end_xlog",
		"end_lsn",
		"finish_lsn",
	)
	if err != nil {
		return model.Backup{}, err
	}
	postgresVersion, err := consistentString(
		object,
		"PostgreSQL version",
		"postgres_version",
		"pg_version",
	)
	if err != nil {
		return model.Backup{}, err
	}
	dataDirectory, err := consistentString(
		object,
		"data directory",
		"pgdata",
		"data_directory",
		"data_dir",
	)
	if err != nil {
		return model.Backup{}, err
	}

	return model.Backup{
		ID:             model.ProviderScopedID(model.ProviderBarman, providerID),
		Provider:       model.ProviderBarman,
		ProviderID:     providerID,
		ClusterName:    server,
		ParentID:       parentID,
		Kind:           inferBarmanKind(backupType, parentID),
		Status:         mapBarmanStatus(status),
		StartedAt:      startedAt,
		FinishedAt:     finishedAt,
		LastModifiedAt: lastModifiedAt,
		WALRange: model.WALRange{
			StartSegment: startSegment,
			EndSegment:   endSegment,
			StartLSN:     startLSN,
			EndLSN:       endLSN,
		},
		PostgreSQLVersion: postgresVersion,
		DataDirectory:     dataDirectory,
		Permanent:         permanent || isKept(keep),
		Metadata:          metadata(object, "backup_name", "system_identifier", "systemid", "backup_method", "retention_policy_status"),
	}, nil
}

func backupListObjects(root map[string]any, defaultServer string) ([]map[string]any, string, error) {
	if len(root) > 2 {
		return nil, "", fmt.Errorf("expected exactly one server entry and optional _WARNING")
	}
	if warning, ok := root["_WARNING"]; ok {
		values, ok := warning.([]any)
		if !ok {
			return nil, "", fmt.Errorf("_WARNING must be an array of strings")
		}
		if len(values) > model.MaxReportAttributes {
			return nil, "", fmt.Errorf(
				"_WARNING exceeds maximum count %d",
				model.MaxReportAttributes,
			)
		}
		for index, value := range values {
			if _, ok := value.(string); !ok {
				return nil, "", fmt.Errorf("_WARNING entry %d must be a string", index)
			}
		}
	}

	serverKeys := make([]string, 0, len(root))
	for key := range root {
		if key != "_WARNING" {
			serverKeys = append(serverKeys, key)
		}
	}
	sort.Strings(serverKeys)
	if len(serverKeys) != 1 {
		return nil, "", fmt.Errorf("expected exactly one server entry")
	}
	server := serverKeys[0]
	if defaultServer != "" && server != defaultServer {
		return nil, "", fmt.Errorf(
			"server entry %q does not match requested server %q",
			server,
			defaultServer,
		)
	}
	value := root[server]
	switch typed := value.(type) {
	case []any:
		if len(typed) > model.MaxBackupsPerCatalog {
			return nil, "", fmt.Errorf(
				"server %q backups exceed maximum count %d",
				server,
				model.MaxBackupsPerCatalog,
			)
		}
		objects := make([]map[string]any, 0, len(typed))
		for index, value := range typed {
			object, ok := value.(map[string]any)
			if !ok {
				return nil, "", fmt.Errorf("server %q backup %d must be an object", server, index)
			}
			objects = append(objects, object)
		}
		return objects, server, nil
	case map[string]any:
		if len(typed) > model.MaxBackupsPerCatalog {
			return nil, "", fmt.Errorf(
				"server %q backups exceed maximum count %d",
				server,
				model.MaxBackupsPerCatalog,
			)
		}
		backupIDs := make([]string, 0, len(typed))
		for backupID := range typed {
			backupIDs = append(backupIDs, backupID)
		}
		sort.Strings(backupIDs)
		objects := make([]map[string]any, 0, len(backupIDs))
		for _, backupID := range backupIDs {
			value := typed[backupID]
			object, ok := value.(map[string]any)
			if !ok {
				return nil, "", fmt.Errorf("server %q backup %q must be an object", server, backupID)
			}
			explicitID, found, err := adapterutil.OptionalStringAlias(
				object,
				"backup id",
				"backup_id",
				"id",
				"backupId",
			)
			if err != nil {
				return nil, "", err
			}
			if found && explicitID != backupID {
				return nil, "", fmt.Errorf(
					"keyed backup id %q does not match explicit backup id %q",
					backupID,
					explicitID,
				)
			}
			if !found {
				object = copyMap(object)
				object["backup_id"] = backupID
			}
			objects = append(objects, object)
		}
		return objects, server, nil
	default:
		return nil, "", fmt.Errorf("server %q backups must be an array or keyed object", server)
	}
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

func getTime(object map[string]any, keys ...string) (*time.Time, error) {
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
			if selectedTime != nil {
				continue
			}
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

func parseTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 {
		return time.Unix(seconds, 0).UTC(), nil
	}
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
	return maps.Clone(input)
}
