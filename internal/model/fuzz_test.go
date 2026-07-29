package model

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func FuzzCanonicalValidators(f *testing.F) {
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
		f.Fatal(err)
	}
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	addValidatorSeed(f, 0, OperationCheckpoint{
		SchemaVersion: CurrentOperationCheckpointSchemaVersion,
		Operation:     operation,
		State:         OperationStateIntent,
		StartedAt:     now,
		UpdatedAt:     now,
	})
	addValidatorSeed(f, 1, ArtifactRef{
		SchemaVersion:  CurrentArtifactReferenceSchemaVersion,
		ID:             "sha256:" + strings.Repeat("b", 64),
		URI:            "artifacts/sha256/bb/" + strings.Repeat("b", 64),
		SizeBytes:      1,
		MediaType:      "application/json",
		RetentionClass: ArtifactRetentionRun,
		RedactionState: ArtifactRedactionRedacted,
	})
	addValidatorSeed(f, 2, RecoveryPolicy{
		MaximumRTO:            "1h",
		MaximumRPO:            "5m",
		MaximumBackupAge:      "24h",
		RequireRecoveryTarget: true,
		RequireCleanup:        true,
	})
	command := CommandEvidence{
		Path:           "tool",
		StartedAt:      now,
		FinishedAt:     now.Add(time.Second),
		DurationMillis: 1000,
		ExitStatus: ExitStatus{
			Started:  true,
			Exited:   true,
			Success:  true,
			ExitCode: 0,
		},
	}
	addValidatorSeed(f, 3, command)
	addValidatorSeed(f, 4, EvidenceRecord{
		ID:          "command",
		Kind:        EvidenceCommand,
		Source:      "test",
		CollectedAt: now.Add(time.Second),
		Command:     &command,
	})
	addValidatorSeed(f, 5, Check{
		Name:   "sql",
		Probe:  ProbeSQL,
		Status: CheckStatusPassed,
	})
	f.Add(uint8(0), []byte(`{}`))
	f.Add(uint8(1), []byte(`null`))
	f.Add(uint8(2), []byte(`{"maximum_rto":"-1s"}`))
	f.Add(uint8(3), []byte(`{"path":"tool"}`))
	f.Add(uint8(4), []byte(`{"id":"evidence"}`))
	f.Add(uint8(5), []byte(`{"name":"check"}`))

	f.Fuzz(func(t *testing.T, kind uint8, data []byte) {
		var validate func() error
		switch kind % 6 {
		case 0:
			var value OperationCheckpoint
			if err := json.Unmarshal(data, &value); err != nil {
				return
			}
			validate = value.Validate
		case 1:
			var value ArtifactRef
			if err := json.Unmarshal(data, &value); err != nil {
				return
			}
			validate = value.Validate
		case 2:
			var value RecoveryPolicy
			if err := json.Unmarshal(data, &value); err != nil {
				return
			}
			validate = value.Validate
		case 3:
			var value CommandEvidence
			if err := json.Unmarshal(data, &value); err != nil {
				return
			}
			validate = value.Validate
		case 4:
			var value EvidenceRecord
			if err := json.Unmarshal(data, &value); err != nil {
				return
			}
			validate = value.Validate
		case 5:
			var value Check
			if err := json.Unmarshal(data, &value); err != nil {
				return
			}
			validate = value.Validate
		}

		first := validate()
		second := validate()
		if (first == nil) != (second == nil) {
			t.Fatalf("validator acceptance is not deterministic: first=%v second=%v", first, second)
		}
		if first != nil && first.Error() != second.Error() {
			t.Fatalf("validator error is not deterministic: first=%q second=%q", first, second)
		}
	})
}

func addValidatorSeed(f *testing.F, kind uint8, value any) {
	f.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		f.Fatalf("marshal validator seed: %v", err)
	}
	f.Add(kind, payload)
}
