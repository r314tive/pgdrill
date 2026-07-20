package cnpg

import (
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
)

func TestPollEvidenceCompactsRepeatedStatesPerOperation(t *testing.T) {
	startedAt := time.Date(2026, 7, 20, 17, 0, 0, 0, time.UTC)
	buffer := newPollEvidence()
	buffer.Add(
		pollCommandRecord("recovery", "running", startedAt),
		pollCommandRecord("instance", "not-found", startedAt.Add(time.Second)),
		pollCommandRecord("recovery", "running", startedAt.Add(5*time.Second)),
		pollCommandRecord("instance", "not-found", startedAt.Add(6*time.Second)),
		pollCommandRecord("recovery", "running", startedAt.Add(10*time.Second)),
		pollCommandRecord("instance", "ready", startedAt.Add(11*time.Second)),
	)

	records := buffer.Records()
	if len(records) != 3 {
		t.Fatalf("expected three unique operation states, got %#v", records)
	}
	recovery := records[0]
	if recovery.Attributes[pollObservationsAttribute] != "3" {
		t.Fatalf("unexpected recovery observation count %#v", recovery.Attributes)
	}
	if recovery.Attributes[pollFirstObservedAttribute] != startedAt.Format(time.RFC3339Nano) ||
		recovery.Attributes[pollLastObservedAttribute] != startedAt.Add(10*time.Second).Format(time.RFC3339Nano) {
		t.Fatalf("unexpected recovery observation range %#v", recovery.Attributes)
	}
	if records[1].Attributes[pollObservationsAttribute] != "2" || records[2].Command.Stdout != "ready" {
		t.Fatalf("unexpected instance state compaction %#v", records)
	}
}

func TestPollEvidenceDoesNotMergeDifferentExitStatus(t *testing.T) {
	startedAt := time.Date(2026, 7, 20, 17, 0, 0, 0, time.UTC)
	first := pollCommandRecord("instance", "", startedAt)
	first.Command.ExitStatus = model.ExitStatus{Started: true, Exited: true, ExitCode: 1}
	second := pollCommandRecord("instance", "", startedAt.Add(time.Second))
	second.Command.ExitStatus = model.ExitStatus{Started: true, Exited: true, Success: true, ExitCode: 0}
	buffer := newPollEvidence()
	buffer.Add(first, second)

	if records := buffer.Records(); len(records) != 2 {
		t.Fatalf("different command states were compacted: %#v", records)
	}
}

func pollCommandRecord(operation, stdout string, collectedAt time.Time) model.EvidenceRecord {
	return model.EvidenceRecord{
		ID:          operation + ":" + collectedAt.Format(time.RFC3339Nano),
		Kind:        model.EvidenceCommand,
		Source:      string(model.RestoreTargetKubernetes),
		CollectedAt: collectedAt,
		Command: &model.CommandEvidence{
			Path:            "kubectl",
			Args:            []string{"get", operation},
			StartedAt:       collectedAt.Add(-100 * time.Millisecond),
			FinishedAt:      collectedAt,
			DurationMillis:  100,
			ExitStatus:      model.ExitStatus{Started: true, Exited: true, Success: true, ExitCode: 0},
			Stdout:          stdout,
			StdoutBytes:     int64(len(stdout)),
			StderrTruncated: false,
		},
		Attributes: map[string]string{"operation": operation},
	}
}
