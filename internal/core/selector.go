package core

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
)

type LatestAvailableSelector struct{}

func (LatestAvailableSelector) Select(catalog model.BackupCatalog, target model.RecoveryTarget) (model.Backup, error) {
	target = target.Normalized()
	if err := target.Validate(); err != nil {
		return model.Backup{}, fmt.Errorf("validate recovery target: %w", err)
	}

	var timestampTarget *time.Time
	if target.Type == model.RecoveryTargetTimestamp {
		value, err := target.Timestamp()
		if err != nil {
			return model.Backup{}, err
		}
		timestampTarget = &value
	}

	candidates := make([]model.Backup, 0, len(catalog.Backups))
	for _, backup := range catalog.Backups {
		if backup.Status != model.BackupStatusAvailable {
			continue
		}
		if timestampTarget != nil && (backup.FinishedAt == nil || !backup.FinishedAt.Before(*timestampTarget)) {
			continue
		}
		candidates = append(candidates, backup)
	}
	if len(candidates) == 0 {
		if timestampTarget != nil {
			return model.Backup{}, fmt.Errorf("no available backups finished before recovery target timestamp %s in %s catalog", timestampTarget.Format(time.RFC3339Nano), catalog.Provider)
		}
		return model.Backup{}, fmt.Errorf("no available backups in %s catalog", catalog.Provider)
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		return backupSortTime(candidates[i]).After(backupSortTime(candidates[j]))
	})
	return candidates[0], nil
}

func SelectBackup(selection model.BackupSelection, catalog model.BackupCatalog, target model.RecoveryTarget) (model.Backup, error) {
	selection.Type = model.BackupSelectionType(strings.TrimSpace(string(selection.Type)))
	if selection.Type == "" {
		selection.Type = model.BackupSelectionLatestAvailable
	}
	selection.BackupID = strings.TrimSpace(selection.BackupID)

	switch selection.Type {
	case model.BackupSelectionLatestAvailable:
		if selection.BackupID != "" {
			return model.Backup{}, fmt.Errorf("latest_available backup selection does not accept backup_id")
		}
		return (LatestAvailableSelector{}).Select(catalog, target)
	case model.BackupSelectionByID:
		if selection.BackupID == "" {
			return model.Backup{}, fmt.Errorf("backup_id selection requires backup_id")
		}
		return selectBackupByID(selection.BackupID, catalog, target)
	default:
		return model.Backup{}, fmt.Errorf("unsupported backup selection type %q", selection.Type)
	}
}

func selectBackupByID(id string, catalog model.BackupCatalog, target model.RecoveryTarget) (model.Backup, error) {
	target = target.Normalized()
	if err := target.Validate(); err != nil {
		return model.Backup{}, fmt.Errorf("validate recovery target: %w", err)
	}

	for _, backup := range catalog.Backups {
		if backup.ID != id {
			continue
		}
		if backup.Status != model.BackupStatusAvailable {
			return model.Backup{}, fmt.Errorf("selected backup %q is not available", id)
		}
		if target.Type == model.RecoveryTargetTimestamp {
			timestamp, err := target.Timestamp()
			if err != nil {
				return model.Backup{}, err
			}
			if backup.FinishedAt == nil || !backup.FinishedAt.Before(timestamp) {
				return model.Backup{}, fmt.Errorf("selected backup %q did not finish before recovery target timestamp %s", id, timestamp.Format(time.RFC3339Nano))
			}
		}
		return backup, nil
	}
	return model.Backup{}, fmt.Errorf("selected backup %q is not in the %s catalog", id, catalog.Provider)
}

func backupSortTime(backup model.Backup) time.Time {
	for _, value := range []*time.Time{
		backup.FinishedAt,
		backup.StartedAt,
		backup.LastModifiedAt,
	} {
		if value != nil {
			return *value
		}
	}
	return time.Time{}
}
