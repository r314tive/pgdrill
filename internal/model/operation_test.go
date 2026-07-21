package model

import (
	"strings"
	"testing"
	"time"
)

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
