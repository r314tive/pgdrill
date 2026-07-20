package core

import (
	"fmt"
	"sort"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
)

type BackupSelector interface {
	Select(catalog model.BackupCatalog, target model.RecoveryTarget) (model.Backup, error)
}

type BackupSelectorFunc func(catalog model.BackupCatalog, target model.RecoveryTarget) (model.Backup, error)

func (f BackupSelectorFunc) Select(catalog model.BackupCatalog, target model.RecoveryTarget) (model.Backup, error) {
	return f(catalog, target)
}

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
