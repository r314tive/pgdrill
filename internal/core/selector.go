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

func (LatestAvailableSelector) Select(catalog model.BackupCatalog, _ model.RecoveryTarget) (model.Backup, error) {
	candidates := make([]model.Backup, 0, len(catalog.Backups))
	for _, backup := range catalog.Backups {
		if backup.Status == model.BackupStatusAvailable {
			candidates = append(candidates, backup)
		}
	}
	if len(candidates) == 0 {
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
