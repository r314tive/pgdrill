package core

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
)

type EventSinkFunc func(context.Context, model.RunEvent) error

func (f EventSinkFunc) WriteEvent(ctx context.Context, event model.RunEvent) error {
	return f(ctx, event)
}

type eventEmitter struct {
	sink      EventSink
	runID     string
	attemptID string
	sequence  uint64
	clock     func() time.Time
}

func newEventEmitter(sink EventSink, runID, attemptID string, clock func() time.Time) (*eventEmitter, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, fmt.Errorf("event emitter run id is required")
	}
	attemptID = strings.TrimSpace(attemptID)
	if attemptID == "" {
		return nil, fmt.Errorf("event emitter attempt id is required")
	}
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	return &eventEmitter{
		sink:      sink,
		runID:     runID,
		attemptID: attemptID,
		clock:     clock,
	}, nil
}

func (e *eventEmitter) runStarted(ctx context.Context, attributes map[string]string) error {
	return e.emit(ctx, model.RunEvent{
		Type:       model.RunEventStarted,
		Attributes: attributes,
	})
}

func (e *eventEmitter) stageStarted(ctx context.Context, stage model.DrillStage) error {
	return e.emit(ctx, model.RunEvent{
		Type:  model.RunEventStageStarted,
		Stage: stage,
	})
}

func (e *eventEmitter) stageCompleted(ctx context.Context, stage model.DrillStage, outcome model.StageOutcome, message string) error {
	return e.emit(ctx, model.RunEvent{
		Type:    model.RunEventStageCompleted,
		Stage:   stage,
		Outcome: outcome,
		Message: message,
	})
}

func (e *eventEmitter) runFinished(ctx context.Context, status model.DrillStatus, message string) error {
	return e.emit(ctx, model.RunEvent{
		Type:    model.RunEventFinished,
		Status:  status,
		Message: message,
	})
}

func (e *eventEmitter) emit(ctx context.Context, event model.RunEvent) error {
	if e == nil || e.sink == nil {
		return nil
	}

	sequence := e.sequence + 1
	event.SchemaVersion = model.CurrentRunEventSchemaVersion
	event.RunID = e.runID
	event.AttemptID = e.attemptID
	event.Sequence = sequence
	event.OccurredAt = e.clock().UTC()
	event.Attributes = cloneStrings(event.Attributes)
	if err := event.Validate(); err != nil {
		return fmt.Errorf("validate run event %d: %w", event.Sequence, err)
	}
	if err := e.sink.WriteEvent(ctx, event); err != nil {
		return fmt.Errorf("write run event %d (%s): %w", event.Sequence, event.Type, err)
	}
	e.sequence = sequence
	return nil
}

func cloneStrings(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func derivedAttemptID(runID string, startedAt time.Time) string {
	return strings.TrimSpace(runID) + "@" + startedAt.UTC().Format("20060102T150405.000000000Z")
}
