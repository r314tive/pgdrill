package model

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestNewDrillFailureBoundsDurableMessage(t *testing.T) {
	message := strings.Repeat("x", MaxFailureMessageBytes) + string([]byte{0xff}) + "tail"
	failure := NewDrillFailure(
		DrillStageProbeExecution,
		errors.New(message),
		[]EvidenceRecord{{ID: "first"}, {ID: "first"}, {ID: ""}},
	)
	if len(failure.Message) > MaxFailureMessageBytes || !utf8.ValidString(failure.Message) {
		t.Fatalf("failure message is not bounded UTF-8: length=%d", len(failure.Message))
	}
	if len(failure.EvidenceIDs) != 1 || failure.EvidenceIDs[0] != "first" {
		t.Fatalf("failure evidence ids = %#v", failure.EvidenceIDs)
	}
}

func TestCheckValidateRejectsUnboundedOrAmbiguousFields(t *testing.T) {
	valid := Check{Name: "sql", Probe: ProbeSQL, Status: CheckStatusPassed}
	tests := []struct {
		name   string
		mutate func(*Check)
		want   string
	}{
		{
			name: "message",
			mutate: func(check *Check) {
				check.Message = strings.Repeat("x", MaxCheckMessageBytes+1)
			},
			want: "message exceeds",
		},
		{
			name: "duplicate evidence",
			mutate: func(check *Check) {
				check.EvidenceIDs = []string{"evidence", "evidence"}
			},
			want: "duplicate reference",
		},
		{
			name: "attribute key",
			mutate: func(check *Check) {
				check.Attributes = map[string]string{" bad": "value"}
			},
			want: "attribute key",
		},
		{
			name: "attribute value",
			mutate: func(check *Check) {
				check.Attributes = map[string]string{
					"key": strings.Repeat("x", MaxReportAttributeValueBytes+1),
				}
			},
			want: "value exceeds",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			check := valid
			test.mutate(&check)
			if err := check.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Check.Validate() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestEvidenceRecordValidateRejectsMalformedPayload(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	valid := EvidenceRecord{
		ID:          "runtime",
		Kind:        EvidenceRuntime,
		Source:      "test",
		CollectedAt: now,
	}
	tests := []struct {
		name   string
		mutate func(*EvidenceRecord)
		want   string
	}{
		{
			name: "source whitespace",
			mutate: func(record *EvidenceRecord) {
				record.Source = " test"
			},
			want: "surrounding whitespace",
		},
		{
			name: "artifact digest",
			mutate: func(record *EvidenceRecord) {
				record.ArtifactIDs = []string{"sha256:not-a-digest"}
			},
			want: "canonical sha256",
		},
		{
			name: "unexpected command",
			mutate: func(record *EvidenceRecord) {
				record.Command = &CommandEvidence{}
			},
			want: "must not contain command",
		},
		{
			name: "too many attributes",
			mutate: func(record *EvidenceRecord) {
				record.Attributes = make(map[string]string, MaxReportAttributes+1)
				for index := 0; index <= MaxReportAttributes; index++ {
					record.Attributes[strings.Repeat("k", index+1)] = "value"
				}
			},
			want: "attributes exceed maximum count",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := valid
			test.mutate(&record)
			if err := record.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("EvidenceRecord.Validate() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestCommandEvidenceExitStatusStateMachine(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	evidence := func(status ExitStatus) CommandEvidence {
		return CommandEvidence{
			Path:           "tool",
			StartedAt:      now,
			FinishedAt:     now,
			DurationMillis: 0,
			ExitStatus:     status,
		}
	}
	valid := []ExitStatus{
		{
			ExitCode: -1,
			Error:    "executable not found",
		},
		{
			ExitCode: -1,
			TimedOut: true,
		},
		{
			Started:  true,
			Exited:   true,
			Success:  true,
			ExitCode: 0,
		},
		{
			Started:  true,
			Exited:   true,
			ExitCode: 2,
		},
		{
			Started:  true,
			Exited:   true,
			ExitCode: -1,
			Canceled: true,
		},
		{
			Started:  true,
			Exited:   true,
			ExitCode: 0,
			Error:    "pipe wait failed",
		},
	}
	for index, status := range valid {
		if err := evidence(status).Validate(); err != nil {
			t.Fatalf("valid exit status %d rejected: %#v: %v", index, status, err)
		}
	}

	invalid := []ExitStatus{
		{Exited: true, ExitCode: 1},
		{Started: true, ExitCode: 1},
		{ExitCode: 0, Error: "not started"},
		{ExitCode: -1},
		{Started: true, Exited: true, Success: true, ExitCode: 1},
		{Started: true, Exited: true, ExitCode: 0},
		{Started: true, Exited: true, ExitCode: -1, TimedOut: true, Canceled: true},
		{Started: true, Exited: true, ExitCode: -2},
	}
	for index, status := range invalid {
		if err := evidence(status).Validate(); err == nil {
			t.Fatalf("invalid exit status %d accepted: %#v", index, status)
		}
	}
}

func TestCommandEvidenceBindsDurationToTimestamps(t *testing.T) {
	startedAt := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	evidence := CommandEvidence{
		Path:           "tool",
		StartedAt:      startedAt,
		FinishedAt:     startedAt.Add(1500 * time.Millisecond),
		DurationMillis: 1499,
		ExitStatus: ExitStatus{
			Started:  true,
			Exited:   true,
			Success:  true,
			ExitCode: 0,
		},
	}
	if err := evidence.Validate(); err == nil || !strings.Contains(err.Error(), "does not match timestamp interval 1500") {
		t.Fatalf("CommandEvidence.Validate() error = %v", err)
	}
	evidence.DurationMillis = 1500
	if err := evidence.Validate(); err != nil {
		t.Fatalf("CommandEvidence.Validate(valid) error = %v", err)
	}
}

func TestDrillFailureValidation(t *testing.T) {
	valid := DrillFailure{
		Stage:       DrillStageProbeExecution,
		Message:     "probe failed",
		EvidenceIDs: []string{"probe-command"},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("DrillFailure.Validate() error = %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*DrillFailure)
		want   string
	}{
		{name: "stage", mutate: func(value *DrillFailure) { value.Stage = "future" }, want: "unsupported stage"},
		{name: "message", mutate: func(value *DrillFailure) { value.Message = " " }, want: "message is required"},
		{name: "message size", mutate: func(value *DrillFailure) {
			value.Message = strings.Repeat("x", MaxFailureMessageBytes+1)
		}, want: "message exceeds"},
		{name: "evidence", mutate: func(value *DrillFailure) {
			value.EvidenceIDs = []string{"probe-command", "probe-command"}
		}, want: "duplicate reference"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := valid
			test.mutate(&value)
			if err := value.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("DrillFailure.Validate() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestExitStatusSummary(t *testing.T) {
	tests := []struct {
		name   string
		status ExitStatus
		want   string
	}{
		{name: "not started error", status: ExitStatus{Error: "missing"}, want: "not started: missing"},
		{name: "not started", status: ExitStatus{}, want: "not started"},
		{name: "timeout", status: ExitStatus{Started: true, TimedOut: true}, want: "timed out"},
		{name: "canceled", status: ExitStatus{Started: true, Canceled: true}, want: "canceled"},
		{name: "success", status: ExitStatus{Started: true, Success: true}, want: "success"},
		{name: "exit", status: ExitStatus{Started: true, Exited: true, ExitCode: 7}, want: "exit code 7"},
		{name: "error", status: ExitStatus{Started: true, Error: "wait failed"}, want: "wait failed"},
		{name: "failed", status: ExitStatus{Started: true}, want: "failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.status.Summary(); got != test.want {
				t.Fatalf("Summary() = %q, want %q", got, test.want)
			}
		})
	}
}
