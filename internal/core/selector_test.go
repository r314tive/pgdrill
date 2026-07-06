package core

import (
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
)

func TestLatestAvailableSelectorPrefersFinishedAt(t *testing.T) {
	newerFinish := mustTime(t, "2025-01-03T00:00:00Z")
	olderFinish := mustTime(t, "2025-01-02T00:00:00Z")
	newerModified := mustTime(t, "2025-02-01T00:00:00Z")

	backup, err := (LatestAvailableSelector{}).Select(model.BackupCatalog{
		Provider: model.ProviderWALG,
		Backups: []model.Backup{
			{
				ID:             "old-finish-new-modified",
				Status:         model.BackupStatusAvailable,
				FinishedAt:     &olderFinish,
				LastModifiedAt: &newerModified,
			},
			{
				ID:         "new-finish",
				Status:     model.BackupStatusAvailable,
				FinishedAt: &newerFinish,
			},
			{
				ID:         "waiting",
				Status:     model.BackupStatusWaitingForWAL,
				FinishedAt: timePtr(t, "2025-01-04T00:00:00Z"),
			},
		},
	}, model.RecoveryTarget{Type: model.RecoveryTargetLatest})

	if err != nil {
		t.Fatalf("select latest backup: %v", err)
	}
	if backup.ID != "new-finish" {
		t.Fatalf("expected backup with newest finish time, got %q", backup.ID)
	}
}

func TestLatestAvailableSelectorFailsWithoutAvailableBackup(t *testing.T) {
	_, err := (LatestAvailableSelector{}).Select(model.BackupCatalog{
		Provider: model.ProviderBarman,
		Backups: []model.Backup{
			{ID: "waiting", Status: model.BackupStatusWaitingForWAL},
			{ID: "failed", Status: model.BackupStatusFailed},
		},
	}, model.RecoveryTarget{Type: model.RecoveryTargetLatest})

	if err == nil {
		t.Fatal("expected error without available backups")
	}
}

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse time %s: %v", value, err)
	}
	return parsed
}

func timePtr(t *testing.T, value string) *time.Time {
	t.Helper()
	parsed := mustTime(t, value)
	return &parsed
}
