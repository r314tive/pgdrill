package core

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/r314tive/pgdrill/internal/model"
)

type EventSinkFunc func(context.Context, model.RunEvent) error

func (f EventSinkFunc) WriteEvent(ctx context.Context, event model.RunEvent) error {
	return f(ctx, event)
}

type eventEmitter struct {
	sink       EventSink
	runID      string
	attemptID  string
	specDigest string
	sequence   uint64
	clock      func() time.Time
}

func newEventEmitter(sink EventSink, runID, attemptID, specDigest string, clock func() time.Time) (*eventEmitter, error) {
	if err := model.ValidateIdentity("event emitter run id", runID); err != nil {
		return nil, err
	}
	if err := model.ValidateIdentity("event emitter attempt id", attemptID); err != nil {
		return nil, err
	}
	if specDigest != "" && !model.IsSHA256Digest(specDigest) {
		return nil, fmt.Errorf("event emitter spec digest must be a sha256 digest")
	}
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	return &eventEmitter{
		sink:       sink,
		runID:      runID,
		attemptID:  attemptID,
		specDigest: specDigest,
		clock:      clock,
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
		Message: boundedEventMessage(message),
	})
}

func (e *eventEmitter) runFinished(ctx context.Context, status model.DrillStatus, message string) error {
	return e.emit(ctx, model.RunEvent{
		Type:    model.RunEventFinished,
		Status:  status,
		Message: boundedEventMessage(message),
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
	event.SpecDigest = e.specDigest
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
	candidate := runID + "@" + startedAt.UTC().Format("20060102T150405.000000000Z")
	if model.ValidateIdentity("attempt_id", candidate) == nil {
		return candidate
	}
	return fmt.Sprintf("attempt-%x", sha256.Sum256([]byte(candidate)))
}

func boundedEventMessage(message string) string {
	message = strings.ToValidUTF8(strings.TrimSpace(message), "?")
	if len(message) <= model.MaxRunEventMessageBytes {
		return message
	}
	message = message[:model.MaxRunEventMessageBytes]
	for !utf8.ValidString(message) {
		message = message[:len(message)-1]
	}
	return message
}
