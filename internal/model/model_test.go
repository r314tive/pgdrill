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

func TestIsSHA256DigestRequiresCanonicalLowercase(t *testing.T) {
	lowercase := "sha256:" + strings.Repeat("a", 64)
	if !IsSHA256Digest(lowercase) {
		t.Fatalf("canonical digest %q was rejected", lowercase)
	}
	if IsSHA256Digest("sha256:" + strings.Repeat("A", 64)) {
		t.Fatal("uppercase digest was accepted")
	}
}

func TestProjectOverviewDistinguishesCanonicalTargetsFromCommandCapabilities(t *testing.T) {
	overview := ProjectOverview()

	if got, want := overview.RestoreTargets, []RestoreTargetType{
		RestoreTargetLocal,
		RestoreTargetContainer,
		RestoreTargetKubernetes,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected canonical restore targets: got %#v want %#v", got, want)
	}
	if got, want := overview.TargetCapabilities.Run, []RestoreTargetType{RestoreTargetLocal}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected full-drill targets: got %#v want %#v", got, want)
	}
	if got, want := overview.TargetCapabilities.Manifest, []RestoreTargetType{RestoreTargetKubernetes}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected manifest targets: got %#v want %#v", got, want)
	}
	if got, want := overview.TargetCapabilities.Verify, []RestoreTargetType{RestoreTargetKubernetes}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected verify targets: got %#v want %#v", got, want)
	}
}

func TestCanonicalEnumPredicates(t *testing.T) {
	overview := ProjectOverview()
	for _, provider := range overview.Providers {
		if !provider.IsKnown() {
			t.Errorf("expected provider %q to be known", provider)
		}
	}
	for _, target := range overview.RestoreTargets {
		if !target.IsKnown() {
			t.Errorf("expected target %q to be known", target)
		}
	}
	for _, target := range overview.RecoveryTargets {
		if !target.IsKnown() {
			t.Errorf("expected recovery target %q to be known", target)
		}
	}
	for _, probe := range overview.Probes {
		if !probe.IsKnown() {
			t.Errorf("expected probe %q to be known", probe)
		}
	}
	for _, tool := range overview.Tools {
		if !tool.IsKnown() {
			t.Errorf("expected tool %q to be known", tool)
		}
	}
	if ProviderType("future").IsKnown() || RestoreTargetType("future").IsKnown() ||
		RecoveryTargetType("future").IsKnown() || ProbeType("future").IsKnown() || ToolType("future").IsKnown() ||
		BackupKind("future").IsKnown() || BackupStatus("future").IsKnown() ||
		CheckStatusUnknown.IsTerminal() || DrillStatusUnknown.IsTerminal() || EvidenceKind("future").IsKnown() {
		t.Fatal("unknown canonical enum value was accepted")
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

func TestBackupValidateRecoveryMetadata(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		backup Backup
		want   string
	}{
		{
			name: "valid",
			backup: Backup{
				StartedAt:  &now,
				FinishedAt: timePointer(now.Add(time.Second)),
				WALRange:   WALRange{StartLSN: "0/10", EndLSN: "0/20", Timeline: "1"},
			},
		},
		{
			name: "reversed time",
			backup: Backup{
				StartedAt:  &now,
				FinishedAt: timePointer(now.Add(-time.Second)),
			},
			want: "finished_at must not be earlier",
		},
		{
			name:   "invalid lsn",
			backup: Backup{WALRange: WALRange{StartLSN: "decimal"}},
			want:   "invalid wal_range.start_lsn",
		},
		{
			name:   "invalid timeline",
			backup: Backup{WALRange: WALRange{Timeline: "zero"}},
			want:   "invalid wal_range.timeline",
		},
		{
			name:   "invalid segment",
			backup: Backup{WALRange: WALRange{StartSegment: "not-a-segment"}},
			want:   "invalid wal_range.start_segment",
		},
		{
			name: "reversed lsn",
			backup: Backup{WALRange: WALRange{
				StartLSN: "0/20",
				EndLSN:   "0/10",
			}},
			want: "end_lsn must not be earlier",
		},
		{
			name: "segment timeline mismatch",
			backup: Backup{WALRange: WALRange{
				StartSegment: "000000010000000000000001",
				Timeline:     "2",
			}},
			want: "does not match wal_range.timeline",
		},
		{
			name: "segment range timeline mismatch",
			backup: Backup{WALRange: WALRange{
				StartSegment: "000000010000000000000001",
				EndSegment:   "000000020000000000000002",
			}},
			want: "must use the same timeline",
		},
		{
			name: "reversed segment",
			backup: Backup{WALRange: WALRange{
				StartSegment: "000000010000000000000002",
				EndSegment:   "000000010000000000000001",
			}},
			want: "end_segment must not be earlier",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.backup.ValidateRecoveryMetadata()
			if test.want == "" && err != nil {
				t.Fatalf("ValidateRecoveryMetadata() error = %v", err)
			}
			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("ValidateRecoveryMetadata() error = %v, want %q", err, test.want)
			}
		})
	}
}

func timePointer(value time.Time) *time.Time {
	return &value
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
		DrillStagePreflight,
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

func TestSpecEnumPredicatesAndDefaultProbeNames(t *testing.T) {
	for _, mode := range []DrillMode{DrillModeNative, DrillModeManaged} {
		if !mode.IsKnown() {
			t.Fatalf("known drill mode %q was rejected", mode)
		}
	}
	if DrillMode("future").IsKnown() {
		t.Fatal("unknown drill mode was accepted")
	}
	for _, selection := range []BackupSelectionType{
		BackupSelectionLatestAvailable,
		BackupSelectionByID,
	} {
		if !selection.IsKnown() {
			t.Fatalf("known backup selection %q was rejected", selection)
		}
	}
	if BackupSelectionType("future").IsKnown() {
		t.Fatal("unknown backup selection was accepted")
	}
	for probeType, want := range map[ProbeType]string{
		ProbePGIsReady: "pg_isready",
		ProbeSQL:       "sql",
		ProbeAMCheck:   "pg_amcheck",
		ProbePGDump:    "pg_dump",
		ProbeType("x"): "x",
	} {
		if got := DefaultProbeName(probeType); got != want {
			t.Fatalf("DefaultProbeName(%q) = %q, want %q", probeType, got, want)
		}
	}
}
