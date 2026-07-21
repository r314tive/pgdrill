package model

import (
	"strings"
	"testing"
	"time"
)

func TestRunEventValidate(t *testing.T) {
	now := time.Date(2026, 7, 21, 1, 2, 3, 0, time.UTC)
	tests := []struct {
		name  string
		event RunEvent
	}{
		{
			name: "run started",
			event: RunEvent{
				SchemaVersion: CurrentRunEventSchemaVersion,
				RunID:         "run-1",
				AttemptID:     "attempt-1",
				Sequence:      1,
				Type:          RunEventStarted,
				OccurredAt:    now,
			},
		},
		{
			name: "stage started",
			event: RunEvent{
				SchemaVersion: CurrentRunEventSchemaVersion,
				RunID:         "run-1",
				AttemptID:     "attempt-1",
				Sequence:      2,
				Type:          RunEventStageStarted,
				Stage:         DrillStageRestoreExecution,
				OccurredAt:    now,
			},
		},
		{
			name: "stage completed",
			event: RunEvent{
				SchemaVersion: CurrentRunEventSchemaVersion,
				RunID:         "run-1",
				AttemptID:     "attempt-1",
				Sequence:      3,
				Type:          RunEventStageCompleted,
				Stage:         DrillStageRestoreExecution,
				Outcome:       StageOutcomeSucceeded,
				OccurredAt:    now,
			},
		},
		{
			name: "run finished",
			event: RunEvent{
				SchemaVersion: CurrentRunEventSchemaVersion,
				RunID:         "run-1",
				AttemptID:     "attempt-1",
				Sequence:      4,
				Type:          RunEventFinished,
				Status:        DrillStatusPassed,
				OccurredAt:    now,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.event.Validate(); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestRunEventValidateRejectsMalformedEvents(t *testing.T) {
	now := time.Date(2026, 7, 21, 1, 2, 3, 0, time.UTC)
	valid := RunEvent{
		SchemaVersion: CurrentRunEventSchemaVersion,
		RunID:         "run-1",
		AttemptID:     "attempt-1",
		Sequence:      1,
		Type:          RunEventStageCompleted,
		Stage:         DrillStageProbeExecution,
		Outcome:       StageOutcomeFailed,
		OccurredAt:    now,
	}

	tests := []struct {
		name string
		edit func(*RunEvent)
		want string
	}{
		{name: "schema", edit: func(e *RunEvent) { e.SchemaVersion = "future" }, want: "schema version"},
		{name: "run id", edit: func(e *RunEvent) { e.RunID = " " }, want: "run_id"},
		{name: "run id whitespace", edit: func(e *RunEvent) { e.RunID = " run-1" }, want: "surrounding whitespace"},
		{name: "attempt id", edit: func(e *RunEvent) { e.AttemptID = "" }, want: "attempt_id"},
		{name: "attempt id whitespace", edit: func(e *RunEvent) { e.AttemptID = "attempt-1 " }, want: "surrounding whitespace"},
		{name: "sequence", edit: func(e *RunEvent) { e.Sequence = 0 }, want: "sequence"},
		{name: "type", edit: func(e *RunEvent) { e.Type = "future" }, want: "type"},
		{name: "time", edit: func(e *RunEvent) { e.OccurredAt = time.Time{} }, want: "occurred_at"},
		{name: "stage", edit: func(e *RunEvent) { e.Stage = "future" }, want: "known stage"},
		{name: "outcome", edit: func(e *RunEvent) { e.Outcome = "future" }, want: "terminal outcome"},
		{name: "status on stage", edit: func(e *RunEvent) { e.Status = DrillStatusFailed }, want: "run status"},
		{name: "attribute key", edit: func(e *RunEvent) { e.Attributes = map[string]string{" ": "value"} }, want: "attribute key"},
		{name: "started fields", edit: func(e *RunEvent) { e.Type = RunEventStarted }, want: "cannot contain"},
		{name: "finished status", edit: func(e *RunEvent) {
			e.Type = RunEventFinished
			e.Stage = ""
			e.Outcome = ""
			e.Status = DrillStatusUnknown
		}, want: "terminal status"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := valid
			tt.edit(&event)
			err := event.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestRunEventEnums(t *testing.T) {
	for _, eventType := range []RunEventType{RunEventStarted, RunEventStageStarted, RunEventStageCompleted, RunEventFinished} {
		if !eventType.IsKnown() {
			t.Fatalf("expected known event type %q", eventType)
		}
	}
	if RunEventType("future").IsKnown() {
		t.Fatal("future event type must not be known")
	}

	for _, outcome := range []StageOutcome{StageOutcomeSucceeded, StageOutcomeFailed, StageOutcomeAborted} {
		if !outcome.IsTerminal() {
			t.Fatalf("expected terminal outcome %q", outcome)
		}
	}
	if StageOutcome("future").IsTerminal() {
		t.Fatal("future stage outcome must not be terminal")
	}
}
