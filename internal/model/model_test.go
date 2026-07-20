package model

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRecoveryTargetNormalizeDefaultsLatest(t *testing.T) {
	target := (RecoveryTarget{Timeline: " latest "}).Normalized()
	if target.Type != RecoveryTargetLatest || target.Timeline != "latest" {
		t.Fatalf("unexpected normalized target %#v", target)
	}
}

func TestRecoveryTargetValidate(t *testing.T) {
	inclusive := true
	tests := []struct {
		name    string
		target  RecoveryTarget
		wantErr string
	}{
		{name: "latest", target: RecoveryTarget{Type: RecoveryTargetLatest}},
		{name: "timestamp", target: RecoveryTarget{Type: RecoveryTargetTimestamp, Value: "2026-07-20T01:02:03+05:00", Inclusive: &inclusive}},
		{name: "lsn", target: RecoveryTarget{Type: RecoveryTargetLSN, Value: "0/420000C0"}},
		{name: "xid", target: RecoveryTarget{Type: RecoveryTargetXID, Value: "757"}},
		{name: "restore point", target: RecoveryTarget{Type: RecoveryTargetRestorePoint, Value: "before_upgrade"}},
		{name: "missing value", target: RecoveryTarget{Type: RecoveryTargetTimestamp}, wantErr: "requires value"},
		{name: "ambiguous timestamp", target: RecoveryTarget{Type: RecoveryTargetTimestamp, Value: "2026-07-20 01:02:03"}, wantErr: "must be RFC3339 with timezone"},
		{name: "invalid lsn", target: RecoveryTarget{Type: RecoveryTargetLSN, Value: "not-an-lsn"}, wantErr: "X/Y hexadecimal format"},
		{name: "invalid xid", target: RecoveryTarget{Type: RecoveryTargetXID, Value: "-1"}, wantErr: "unsigned 32-bit decimal"},
		{name: "invalid timeline", target: RecoveryTarget{Type: RecoveryTargetLatest, Timeline: "newest"}, wantErr: "positive decimal timeline ID"},
		{name: "latest value", target: RecoveryTarget{Type: RecoveryTargetLatest, Value: "unexpected"}, wantErr: "does not accept value"},
		{name: "restore point inclusive", target: RecoveryTarget{Type: RecoveryTargetRestorePoint, Value: "before_upgrade", Inclusive: &inclusive}, wantErr: "does not support inclusive"},
		{name: "unknown", target: RecoveryTarget{Type: "future"}, wantErr: "unsupported recovery target"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.target.Validate()
			if tt.wantErr == "" && err != nil {
				t.Fatalf("validate recovery target: %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestRecoveryTargetTimestamp(t *testing.T) {
	target := RecoveryTarget{Type: RecoveryTargetTimestamp, Value: "2026-07-20T01:02:03.123+05:00"}
	got, err := target.Timestamp()
	if err != nil {
		t.Fatalf("parse timestamp target: %v", err)
	}
	want := time.Date(2026, 7, 19, 20, 2, 3, 123000000, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("unexpected timestamp: got %s want %s", got, want)
	}
}

func TestNewDrillFailureCollectsUniqueEvidenceIDs(t *testing.T) {
	failure := NewDrillFailure(DrillStageBackupSelection, fmt.Errorf("no eligible backup"), []EvidenceRecord{
		{ID: "catalog"},
		{ID: "catalog"},
		{},
		{ID: "selection"},
	})

	if failure.Stage != DrillStageBackupSelection || failure.Message != "no eligible backup" {
		t.Fatalf("unexpected failure %#v", failure)
	}
	if got, want := failure.EvidenceIDs, []string{"catalog", "selection"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected evidence ids: got %#v want %#v", got, want)
	}
}

func TestDrillStageIsKnown(t *testing.T) {
	known := []DrillStage{
		DrillStageRequestValidation,
		DrillStageBackupDiscovery,
		DrillStageBackupSelection,
		DrillStageCatalogValidation,
		DrillStageRestorePlanning,
		DrillStageTargetPreparation,
		DrillStageRestoreExecution,
		DrillStagePostgresStart,
		DrillStageProbeExecution,
		DrillStageTargetDiscovery,
		DrillStageTargetStart,
		DrillStageTargetCleanup,
		DrillStageReportWrite,
	}
	for _, stage := range known {
		if !stage.IsKnown() {
			t.Errorf("expected stage %q to be known", stage)
		}
	}
	for _, stage := range []DrillStage{"", "future_stage"} {
		if stage.IsKnown() {
			t.Errorf("expected stage %q to be unknown", stage)
		}
	}
}
