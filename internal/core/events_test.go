package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
)

func TestEventEmitterWritesValidatedOrderedEvents(t *testing.T) {
	now := time.Date(2026, 7, 21, 1, 2, 3, 0, time.FixedZone("test", 5*60*60))
	events := []model.RunEvent{}
	emitter, err := newEventEmitter(EventSinkFunc(func(_ context.Context, event model.RunEvent) error {
		if err := event.Validate(); err != nil {
			t.Fatalf("sink received invalid event: %v", err)
		}
		events = append(events, event)
		return nil
	}), " run-1 ", " attempt-1 ", func() time.Time { return now })
	if err != nil {
		t.Fatalf("newEventEmitter() error = %v", err)
	}

	attributes := map[string]string{"source": "catalog-a"}
	if err := emitter.runStarted(context.Background(), attributes); err != nil {
		t.Fatalf("runStarted() error = %v", err)
	}
	attributes["source"] = "mutated"
	if err := emitter.stageStarted(context.Background(), model.DrillStageBackupDiscovery); err != nil {
		t.Fatalf("stageStarted() error = %v", err)
	}
	if err := emitter.stageCompleted(context.Background(), model.DrillStageBackupDiscovery, model.StageOutcomeSucceeded, ""); err != nil {
		t.Fatalf("stageCompleted() error = %v", err)
	}
	if err := emitter.runFinished(context.Background(), model.DrillStatusPassed, ""); err != nil {
		t.Fatalf("runFinished() error = %v", err)
	}

	if len(events) != 4 {
		t.Fatalf("got %d events, want 4", len(events))
	}
	for i, event := range events {
		if event.RunID != "run-1" || event.AttemptID != "attempt-1" {
			t.Fatalf("event %d identity = %q/%q", i, event.RunID, event.AttemptID)
		}
		if event.Sequence != uint64(i+1) {
			t.Fatalf("event %d sequence = %d, want %d", i, event.Sequence, i+1)
		}
		if !event.OccurredAt.Equal(now) || event.OccurredAt.Location() != time.UTC {
			t.Fatalf("event %d occurred_at = %v, want UTC %v", i, event.OccurredAt, now.UTC())
		}
	}
	if got := events[0].Attributes["source"]; got != "catalog-a" {
		t.Fatalf("run event attributes were not cloned, got %q", got)
	}
}

func TestEventEmitterPropagatesSinkFailure(t *testing.T) {
	wantErr := errors.New("journal unavailable")
	emitter, err := newEventEmitter(EventSinkFunc(func(context.Context, model.RunEvent) error {
		return wantErr
	}), "run-1", "attempt-1", func() time.Time { return time.Now().UTC() })
	if err != nil {
		t.Fatalf("newEventEmitter() error = %v", err)
	}

	err = emitter.runStarted(context.Background(), nil)
	if !errors.Is(err, wantErr) || !strings.Contains(err.Error(), "run event 1") {
		t.Fatalf("runStarted() error = %v, want wrapped sink error", err)
	}
}

func TestEventEmitterReusesSequenceAfterRejectedWrite(t *testing.T) {
	wantErr := errors.New("journal unavailable")
	calls := 0
	events := []model.RunEvent{}
	emitter, err := newEventEmitter(EventSinkFunc(func(_ context.Context, event model.RunEvent) error {
		calls++
		if calls == 1 {
			return wantErr
		}
		events = append(events, event)
		return nil
	}), "run-1", "attempt-1", func() time.Time { return time.Now().UTC() })
	if err != nil {
		t.Fatalf("newEventEmitter() error = %v", err)
	}

	if err := emitter.runStarted(context.Background(), nil); !errors.Is(err, wantErr) {
		t.Fatalf("first runStarted() error = %v, want sink failure", err)
	}
	if err := emitter.runStarted(context.Background(), nil); err != nil {
		t.Fatalf("second runStarted() error = %v", err)
	}
	if len(events) != 1 || events[0].Sequence != 1 {
		t.Fatalf("accepted events = %#v, want sequence 1", events)
	}
}

func TestEventEmitterRejectsInvalidConstruction(t *testing.T) {
	for _, tt := range []struct {
		name      string
		runID     string
		attemptID string
		want      string
	}{
		{name: "run id", runID: " ", attemptID: "attempt-1", want: "run id"},
		{name: "attempt id", runID: "run-1", attemptID: "", want: "attempt id"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := newEventEmitter(nil, tt.runID, tt.attemptID, nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("newEventEmitter() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestDerivedAttemptID(t *testing.T) {
	startedAt := time.Date(2026, 7, 21, 1, 2, 3, 456, time.FixedZone("test", 5*60*60))
	if got, want := derivedAttemptID(" run-1 ", startedAt), "run-1@20260720T200203.000000456Z"; got != want {
		t.Fatalf("derivedAttemptID() = %q, want %q", got, want)
	}
}
