package core

import (
	"strings"
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

func TestLatestAvailableSelectorUsesBackupFinishedBeforeTimestampTarget(t *testing.T) {
	targetTime := mustTime(t, "2025-01-03T12:00:00Z")
	backup, err := (LatestAvailableSelector{}).Select(model.BackupCatalog{
		Provider: model.ProviderWALG,
		Backups: []model.Backup{
			{ID: "after-target", Status: model.BackupStatusAvailable, FinishedAt: timePtr(t, "2025-01-04T00:00:00Z")},
			{ID: "at-target", Status: model.BackupStatusAvailable, FinishedAt: &targetTime},
			{ID: "unknown-finish", Status: model.BackupStatusAvailable},
			{ID: "eligible", Status: model.BackupStatusAvailable, FinishedAt: timePtr(t, "2025-01-03T11:59:59Z")},
			{ID: "older", Status: model.BackupStatusAvailable, FinishedAt: timePtr(t, "2025-01-02T00:00:00Z")},
		},
	}, model.RecoveryTarget{Type: model.RecoveryTargetTimestamp, Value: targetTime.Format(time.RFC3339)})

	if err != nil {
		t.Fatalf("select timestamp-compatible backup: %v", err)
	}
	if backup.ID != "eligible" {
		t.Fatalf("expected newest backup finished before target, got %q", backup.ID)
	}
}

func TestLatestAvailableSelectorRejectsTimestampWithoutEligibleBackup(t *testing.T) {
	_, err := (LatestAvailableSelector{}).Select(model.BackupCatalog{
		Provider: model.ProviderBarman,
		Backups: []model.Backup{
			{ID: "unknown-finish", Status: model.BackupStatusAvailable},
			{ID: "after-target", Status: model.BackupStatusAvailable, FinishedAt: timePtr(t, "2025-01-04T00:00:00Z")},
		},
	}, model.RecoveryTarget{Type: model.RecoveryTargetTimestamp, Value: "2025-01-03T12:00:00Z"})

	if err == nil || !strings.Contains(err.Error(), "no available backups finished before recovery target timestamp") {
		t.Fatalf("expected timestamp eligibility error, got %v", err)
	}
}

func TestSelectBackupByCanonicalID(t *testing.T) {
	selected, err := SelectBackup(
		model.BackupSelection{Type: model.BackupSelectionByID, BackupID: " wal-g:base_1 "},
		model.BackupCatalog{
			Provider: model.ProviderWALG,
			Backups: []model.Backup{
				{ID: "wal-g:base_2", Status: model.BackupStatusAvailable},
				{ID: "wal-g:base_1", Status: model.BackupStatusAvailable},
			},
		},
		model.RecoveryTarget{Type: model.RecoveryTargetLatest},
	)
	if err != nil {
		t.Fatalf("SelectBackup() error = %v", err)
	}
	if selected.ID != "wal-g:base_1" {
		t.Fatalf("SelectBackup() = %#v", selected)
	}
}

func TestSelectBackupByIDEnforcesTimestampBoundary(t *testing.T) {
	_, err := SelectBackup(
		model.BackupSelection{Type: model.BackupSelectionByID, BackupID: "wal-g:base_1"},
		model.BackupCatalog{
			Provider: model.ProviderWALG,
			Backups: []model.Backup{{
				ID:         "wal-g:base_1",
				Status:     model.BackupStatusAvailable,
				FinishedAt: timePtr(t, "2025-01-03T12:00:00Z"),
			}},
		},
		model.RecoveryTarget{Type: model.RecoveryTargetTimestamp, Value: "2025-01-03T12:00:00Z"},
	)
	if err == nil || !strings.Contains(err.Error(), "did not finish before") {
		t.Fatalf("SelectBackup() error = %v", err)
	}
}

func TestSelectBackupRejectsUnknownOrInconsistentIntent(t *testing.T) {
	tests := []struct {
		name      string
		selection model.BackupSelection
		want      string
	}{
		{name: "unknown", selection: model.BackupSelection{Type: "future"}, want: "unsupported backup selection"},
		{name: "latest with id", selection: model.BackupSelection{Type: model.BackupSelectionLatestAvailable, BackupID: "base_1"}, want: "does not accept"},
		{name: "id without value", selection: model.BackupSelection{Type: model.BackupSelectionByID}, want: "requires backup_id"},
		{name: "missing id", selection: model.BackupSelection{Type: model.BackupSelectionByID, BackupID: "wal-g:missing"}, want: "not in the wal-g catalog"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := SelectBackup(tt.selection, model.BackupCatalog{Provider: model.ProviderWALG}, model.RecoveryTarget{Type: model.RecoveryTargetLatest})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("SelectBackup() error = %v, want %q", err, tt.want)
			}
		})
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
