package adapterutil

import (
	"fmt"

	"github.com/r314tive/pgdrill/internal/model"
)

type StringRedactor interface {
	RedactString(string) string
}

func RedactBackups(backups []model.Backup, result StringRedactor) ([]model.Backup, error) {
	if len(backups) > model.MaxBackupsPerCatalog {
		return nil, fmt.Errorf("backups exceed maximum count %d", model.MaxBackupsPerCatalog)
	}
	redacted := make([]model.Backup, len(backups))
	for index, backup := range backups {
		if field, sensitive := sensitiveCanonicalBackupField(backup, result); sensitive {
			return nil, fmt.Errorf(
				"backup %d canonical field %q contains a configured redaction value",
				index,
				field,
			)
		}
		metadata, err := redactMap(backup.Metadata, result)
		if err != nil {
			return nil, fmt.Errorf("redact backup %d metadata: %w", index, err)
		}
		backup.Metadata = metadata
		if err := backup.ValidateRecoveryMetadata(); err != nil {
			return nil, fmt.Errorf("validate redacted backup %d: %w", index, err)
		}
		redacted[index] = backup
	}
	return redacted, nil
}

func sensitiveCanonicalBackupField(backup model.Backup, result StringRedactor) (string, bool) {
	fields := []struct {
		name  string
		value string
	}{
		{name: "provider_id", value: backup.ProviderID},
		{name: "id", value: backup.ID},
		{name: "cluster_name", value: backup.ClusterName},
		{name: "parent_id", value: backup.ParentID},
		{name: "wal_start_segment", value: backup.WALRange.StartSegment},
		{name: "wal_end_segment", value: backup.WALRange.EndSegment},
		{name: "wal_start_lsn", value: backup.WALRange.StartLSN},
		{name: "wal_end_lsn", value: backup.WALRange.EndLSN},
		{name: "wal_timeline", value: backup.WALRange.Timeline},
		{name: "postgresql_version", value: backup.PostgreSQLVersion},
		{name: "data_directory", value: backup.DataDirectory},
		{name: "hostname", value: backup.Hostname},
	}
	for _, field := range fields {
		if field.value != result.RedactString(field.value) {
			return field.name, true
		}
	}
	return "", false
}

func RedactChecks(checks []model.Check, result StringRedactor) ([]model.Check, error) {
	redacted := make([]model.Check, len(checks))
	for index, check := range checks {
		safeCheck, err := RedactCheck(check, result)
		if err != nil {
			return nil, fmt.Errorf("redact check %d: %w", index, err)
		}
		redacted[index] = safeCheck
	}
	return redacted, nil
}

func RedactCheck(check model.Check, result StringRedactor) (model.Check, error) {
	if check.Name != result.RedactString(check.Name) {
		return model.Check{}, fmt.Errorf(
			"canonical check name contains a configured redaction value",
		)
	}
	for _, evidenceID := range check.EvidenceIDs {
		if evidenceID != result.RedactString(evidenceID) {
			return model.Check{}, fmt.Errorf(
				"canonical check evidence id contains a configured redaction value",
			)
		}
	}
	check.Message = result.RedactString(check.Message)
	attributes, err := redactMap(check.Attributes, result)
	if err != nil {
		return model.Check{}, fmt.Errorf("redact attributes: %w", err)
	}
	check.Attributes = attributes
	if err := check.Validate(); err != nil {
		return model.Check{}, fmt.Errorf("validate redacted check: %w", err)
	}
	return check, nil
}

func redactMap(values map[string]string, result StringRedactor) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	redacted := make(map[string]string, len(values))
	for key, value := range values {
		if key != result.RedactString(key) {
			return nil, fmt.Errorf(
				"structured attribute key contains a configured redaction value",
			)
		}
		redacted[key] = result.RedactString(value)
	}
	return redacted, nil
}
