package model

import (
	"strings"
	"testing"
	"time"
)

func TestValidateIdentityRejectsUnboundedOrUnsafeText(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "empty", value: "", want: "required"},
		{name: "surrounding whitespace", value: " run-1", want: "surrounding whitespace"},
		{name: "control", value: "run\n1", want: "control characters"},
		{name: "invalid utf8", value: string([]byte{0xff}), want: "valid UTF-8"},
		{name: "oversized", value: strings.Repeat("x", maxIdentityBytes+1), want: "exceeds"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateIdentity("run_id", test.value); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateIdentity() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateIdentityAcceptsMaximumCanonicalValue(t *testing.T) {
	if err := ValidateIdentity("run_id", strings.Repeat("x", maxIdentityBytes)); err != nil {
		t.Fatalf("ValidateIdentity() rejected maximum canonical value: %v", err)
	}
}

func TestOperationIdentityIsDeterministicAndAttemptScoped(t *testing.T) {
	identity := AttemptIdentity{
		RunID:      "run-1",
		AttemptID:  "attempt-1",
		SpecDigest: "sha256:" + strings.Repeat("a", 64),
	}
	first, err := NewOperation(identity, DrillStageRestoreExecution, OperationRestoreStep, "fetch", 0)
	if err != nil {
		t.Fatalf("NewOperation() error = %v", err)
	}
	second, err := NewOperation(identity, DrillStageRestoreExecution, OperationRestoreStep, "fetch", 0)
	if err != nil {
		t.Fatalf("NewOperation() second error = %v", err)
	}
	if first.Key != second.Key || !IsSHA256Digest(first.Key) {
		t.Fatalf("operation keys are not deterministic sha256 digests: %#v %#v", first, second)
	}

	identity.AttemptID = "attempt-2"
	third, err := NewOperation(identity, DrillStageRestoreExecution, OperationRestoreStep, "fetch", 0)
	if err != nil {
		t.Fatalf("NewOperation() third error = %v", err)
	}
	if third.Key == first.Key {
		t.Fatalf("distinct attempts share operation key %q", first.Key)
	}
}

func TestAttemptOwnershipIDIsStableAndOpaque(t *testing.T) {
	identity := AttemptIdentity{
		RunID:      "run-1",
		AttemptID:  "attempt-1",
		SpecDigest: "sha256:" + strings.Repeat("b", 64),
	}
	first, err := identity.OwnershipID()
	if err != nil {
		t.Fatalf("OwnershipID() error = %v", err)
	}
	second, err := identity.OwnershipID()
	if err != nil {
		t.Fatalf("OwnershipID() second error = %v", err)
	}
	if first != second || len(first) != 32 {
		t.Fatalf("unexpected ownership ids %q and %q", first, second)
	}
	if strings.Contains(first, identity.RunID) || strings.Contains(first, identity.AttemptID) {
		t.Fatalf("ownership id leaks logical identity: %q", first)
	}
}

func TestOperationCheckpointValidationRejectsTamperedKey(t *testing.T) {
	identity := AttemptIdentity{
		RunID:      "run-1",
		AttemptID:  "attempt-1",
		SpecDigest: "sha256:" + strings.Repeat("c", 64),
	}
	operation, err := NewOperation(identity, DrillStageTargetPreparation, OperationTargetPrepare, "prepare", 0)
	if err != nil {
		t.Fatalf("NewOperation() error = %v", err)
	}
	operation.Key = "sha256:" + strings.Repeat("d", 64)
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	checkpoint := OperationCheckpoint{
		SchemaVersion: CurrentOperationCheckpointSchemaVersion,
		Operation:     operation,
		State:         OperationStateIntent,
		StartedAt:     now,
		UpdatedAt:     now,
	}
	if err := checkpoint.Validate(); err == nil || !strings.Contains(err.Error(), "does not match canonical operation key") {
		t.Fatalf("Validate() error = %v, want tampered key error", err)
	}
}

func TestAttemptContextAndOperationValidationMatrix(t *testing.T) {
	identity := AttemptIdentity{
		RunID:      "run-1",
		AttemptID:  "attempt-1",
		SpecDigest: "sha256:" + strings.Repeat("a", 64),
	}
	validContext := AttemptContext{
		Identity:       identity,
		Target:         TargetSpec{Type: RestoreTargetLocal},
		RecoveryTarget: RecoveryTarget{Type: RecoveryTargetLatest},
	}
	if err := validContext.Validate(); err != nil {
		t.Fatalf("AttemptContext.Validate() error = %v", err)
	}

	contextTests := []struct {
		name   string
		mutate func(*AttemptContext)
		want   string
	}{
		{
			name: "identity",
			mutate: func(value *AttemptContext) {
				value.Identity.AttemptID = ""
			},
			want: "invalid identity",
		},
		{
			name: "target",
			mutate: func(value *AttemptContext) {
				value.Target.Type = "future"
			},
			want: "target type",
		},
		{
			name: "recovery target",
			mutate: func(value *AttemptContext) {
				value.RecoveryTarget = RecoveryTarget{Type: RecoveryTargetTimestamp}
			},
			want: "invalid recovery target",
		},
	}
	for _, test := range contextTests {
		t.Run("context "+test.name, func(t *testing.T) {
			value := validContext
			test.mutate(&value)
			if err := value.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("AttemptContext.Validate() error = %v, want %q", err, test.want)
			}
		})
	}

	operationTests := []struct {
		name    string
		stage   DrillStage
		kind    OperationKind
		opName  string
		ordinal int
		want    string
	}{
		{
			name:   "unknown stage",
			stage:  "future",
			kind:   OperationRestoreStep,
			opName: "restore",
			want:   "stage",
		},
		{
			name:   "unknown kind",
			stage:  DrillStageRestoreExecution,
			kind:   "future",
			opName: "restore",
			want:   "kind",
		},
		{
			name:   "empty name",
			stage:  DrillStageRestoreExecution,
			kind:   OperationRestoreStep,
			opName: " ",
			want:   "name is required",
		},
		{
			name:    "negative ordinal",
			stage:   DrillStageRestoreExecution,
			kind:    OperationRestoreStep,
			opName:  "restore",
			ordinal: -1,
			want:    "ordinal",
		},
		{
			name:   "stage kind mismatch",
			stage:  DrillStageTargetCleanup,
			kind:   OperationRestoreStep,
			opName: "restore",
			want:   "requires stage",
		},
	}
	for _, test := range operationTests {
		t.Run("operation "+test.name, func(t *testing.T) {
			if _, err := NewOperation(
				identity,
				test.stage,
				test.kind,
				test.opName,
				test.ordinal,
			); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NewOperation() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestOperationCheckpointAndReconciliationValidationMatrix(t *testing.T) {
	identity := AttemptIdentity{
		RunID:      "run-1",
		AttemptID:  "attempt-1",
		SpecDigest: "sha256:" + strings.Repeat("a", 64),
	}
	operation, err := NewOperation(
		identity,
		DrillStageRestoreExecution,
		OperationRestoreStep,
		"restore",
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	valid := OperationCheckpoint{
		SchemaVersion: CurrentOperationCheckpointSchemaVersion,
		Operation:     operation,
		State:         OperationStateIntent,
		StartedAt:     now,
		UpdatedAt:     now,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("OperationCheckpoint.Validate() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*OperationCheckpoint)
		want   string
	}{
		{name: "schema", mutate: func(value *OperationCheckpoint) { value.SchemaVersion = "future" }, want: "schema_version"},
		{name: "state", mutate: func(value *OperationCheckpoint) { value.State = "future" }, want: "state"},
		{name: "start", mutate: func(value *OperationCheckpoint) { value.StartedAt = time.Time{} }, want: "started_at"},
		{name: "update", mutate: func(value *OperationCheckpoint) { value.UpdatedAt = time.Time{} }, want: "updated_at"},
		{name: "time order", mutate: func(value *OperationCheckpoint) { value.UpdatedAt = value.StartedAt.Add(-time.Nanosecond) }, want: "earlier"},
		{name: "message", mutate: func(value *OperationCheckpoint) { value.Message = strings.Repeat("x", maxOperationMessageBytes+1) }, want: "message exceeds"},
		{name: "message utf8", mutate: func(value *OperationCheckpoint) { value.Message = string([]byte{0xff}) }, want: "valid UTF-8"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := valid
			test.mutate(&value)
			if err := value.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("OperationCheckpoint.Validate() error = %v, want %q", err, test.want)
			}
		})
	}

	for _, disposition := range []ReconciliationDisposition{
		ReconciliationCompleted,
		ReconciliationNotApplied,
		ReconciliationUnknown,
		ReconciliationConflict,
	} {
		if err := (OperationReconciliation{Disposition: disposition}).Validate(); err != nil {
			t.Fatalf("valid disposition %q rejected: %v", disposition, err)
		}
	}
	if err := (OperationReconciliation{Disposition: "future"}).Validate(); err == nil {
		t.Fatal("unknown reconciliation disposition was accepted")
	}
	if err := (OperationReconciliation{
		Disposition: ReconciliationUnknown,
		Message:     strings.Repeat("x", maxOperationMessageBytes+1),
	}).Validate(); err == nil || !strings.Contains(err.Error(), "message exceeds") {
		t.Fatalf("oversized reconciliation message error = %v", err)
	}
}

func TestOperationStatePredicates(t *testing.T) {
	for _, state := range []OperationState{
		OperationStateIntent,
		OperationStateSucceeded,
		OperationStateFailed,
		OperationStateUnknown,
	} {
		if !state.IsKnown() {
			t.Fatalf("known state %q was rejected", state)
		}
		if got, want := state.IsTerminal(), state != OperationStateIntent; got != want {
			t.Fatalf("state %q terminal = %t, want %t", state, got, want)
		}
	}
	if OperationState("future").IsKnown() || OperationState("future").IsTerminal() {
		t.Fatal("unknown operation state was accepted")
	}
}
