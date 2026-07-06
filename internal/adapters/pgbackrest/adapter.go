package pgbackrest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/r314tive/pgdrill/internal/command"
	"github.com/r314tive/pgdrill/internal/model"
)

const defaultBinary = "pgbackrest"

type Config struct {
	Binary       string
	ConfigPath   string
	Stanza       string
	Repo         string
	Env          map[string]string
	WorkDir      string
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
			commandEvidence("info", result.Evidence),
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
		return catalog, err
	}
	catalog.Backups = backups
	return catalog, nil
}

func (a *Adapter) ValidateCatalog(_ context.Context, _ model.BackupCatalog, _ model.Backup, _ model.RecoveryTarget) (model.CheckReport, error) {
	return model.CheckReport{
		Checks: []model.Check{{
			Name:    "pgbackrest-provider-validation",
			Status:  model.CheckStatusSkipped,
			Message: "pgBackRest check/verify integration is not implemented yet; catalog discovery evidence is still recorded.",
			Attributes: map[string]string{
				"operation": "pgbackrest-validation",
			},
		}},
	}, nil
}

func (a *Adapter) PlanRestore(context.Context, model.Backup, model.RecoveryTarget, model.TargetSpec) (model.RestorePlan, error) {
	return model.RestorePlan{}, fmt.Errorf("pgbackrest restore planning is not implemented yet")
}

func (a *Adapter) binary() string {
	if a.cfg.Binary != "" {
		return a.cfg.Binary
	}
	return defaultBinary
}

func (a *Adapter) infoArgs() []string {
	args := a.globalArgs()
	args = append(args, "info", "--output=json")
	return args
}

func (a *Adapter) globalArgs() []string {
	args := []string{}
	if a.cfg.ConfigPath != "" {
		args = append(args, "--config", a.cfg.ConfigPath)
	}
	if a.cfg.Stanza != "" {
		args = append(args, "--stanza", a.cfg.Stanza)
	}
	if a.cfg.Repo != "" {
		args = append(args, "--repo", a.cfg.Repo)
	}
	return args
}

func ParseInfo(data []byte, defaultStanza string) ([]model.Backup, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("pgbackrest info produced no JSON output")
	}

	var root any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil {
		return nil, fmt.Errorf("parse pgbackrest info json: %w", err)
	}

	stanzas, err := collectStanzas(root)
	if err != nil {
		return nil, err
	}

	backups := []model.Backup{}
	for i, stanza := range stanzas {
		stanzaName := firstNonEmpty(getString(stanza, "name", "stanza"), defaultStanza)
		dbVersions := databaseVersions(stanza)
		dbSystemIDs := databaseSystemIDs(stanza)
		stanzaStatus := statusMessage(stanza["status"])

		backupValues, ok := stanza["backup"].([]any)
		if !ok {
			continue
		}
		for j, value := range backupValues {
			object, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("parse pgbackrest stanza %d backup %d: expected object", i, j)
			}
			backup, err := mapBackup(object, stanzaName, stanzaStatus, dbVersions, dbSystemIDs)
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

func mapBackup(object map[string]any, stanzaName string, stanzaStatus string, dbVersions map[string]string, dbSystemIDs map[string]string) (model.Backup, error) {
	label := getString(object, "label", "set")
	if label == "" {
		return model.Backup{}, fmt.Errorf("missing backup label")
	}

	providerID := label
	if stanzaName != "" {
		providerID = stanzaName + "/" + label
	}

	dbID := nestedString(object, "database", "id")
	startedAt := nestedTime(object, "timestamp", "start")
	finishedAt := nestedTime(object, "timestamp", "stop")
	status := model.BackupStatusAvailable
	if getBool(object, "error") {
		status = model.BackupStatusFailed
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
		ParentID:       getString(object, "prior"),
		Kind:           mapBackupKind(getString(object, "type")),
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
		PostgreSQLVersion: firstNonEmpty(dbVersions[dbID], firstMapValue(dbVersions)),
		Permanent:         false,
		Metadata:          metadataOrNil(withSystemID(metadata, dbSystemIDs[dbID], firstMapValue(dbSystemIDs))),
	}, nil
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

func statusMessage(value any) string {
	object, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	return getString(object, "message", "code")
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

func commandEvidence(operation string, evidence model.CommandEvidence) model.EvidenceRecord {
	collectedAt := evidence.FinishedAt
	if collectedAt.IsZero() {
		collectedAt = time.Now().UTC()
	}

	return model.EvidenceRecord{
		ID:          string(model.ProviderPGBackRest) + ":" + operation + ":" + collectedAt.Format(time.RFC3339Nano),
		Kind:        model.EvidenceCommand,
		Source:      string(model.ProviderPGBackRest),
		CollectedAt: collectedAt,
		Command:     &evidence,
		Attributes: map[string]string{
			"operation": operation,
		},
	}
}

func nestedString(object map[string]any, keys ...string) string {
	value := nestedValue(object, keys...)
	return numberString(value)
}

func nestedTime(object map[string]any, keys ...string) *time.Time {
	value := nestedValue(object, keys...)
	parsed, ok := parseTimeValue(value)
	if !ok {
		return nil
	}
	return &parsed
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
			case "true", "yes", "1", "y":
				return true
			case "false", "no", "0", "n":
				return false
			}
		}
	}
	return false
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

func parseTimeValue(value any) (time.Time, bool) {
	switch typed := value.(type) {
	case json.Number:
		seconds, err := strconv.ParseInt(typed.String(), 10, 64)
		if err == nil {
			return time.Unix(seconds, 0).UTC(), true
		}
	case float64:
		return time.Unix(int64(typed), 0).UTC(), true
	case string:
		if typed == "" {
			return time.Time{}, false
		}
		if seconds, err := strconv.ParseInt(typed, 10, 64); err == nil {
			return time.Unix(seconds, 0).UTC(), true
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05Z07:00", "2006-01-02 15:04:05"} {
			if parsed, err := time.Parse(layout, typed); err == nil {
				return parsed.UTC(), true
			}
		}
	}
	return time.Time{}, false
}

func addMetadata(metadata map[string]string, key, value string) {
	if value != "" {
		metadata[key] = value
	}
}

func withSystemID(metadata map[string]string, systemID string, fallback string) map[string]string {
	addMetadata(metadata, "system_identifier", firstNonEmpty(systemID, fallback))
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

func firstMapValue(values map[string]string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func metadataOrNil(metadata map[string]string) map[string]string {
	if len(metadata) == 0 {
		return nil
	}
	return metadata
}
