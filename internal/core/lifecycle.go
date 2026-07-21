package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/r314tive/pgdrill/internal/finalize"
	"github.com/r314tive/pgdrill/internal/model"
)

type runLifecycle struct {
	result              *model.DrillResult
	reportSink          EvidenceSink
	events              *eventEmitter
	clock               func() time.Time
	finalizationTimeout time.Duration
	activeStage         model.DrillStage
	started             bool
	eventStreamStarted  bool
	terminal            bool
}

func newRunLifecycle(
	result *model.DrillResult,
	attemptID string,
	reportSink EvidenceSink,
	eventSink EventSink,
	clock func() time.Time,
	finalizationTimeout time.Duration,
) (*runLifecycle, error) {
	if result == nil {
		return nil, fmt.Errorf("lifecycle result is required")
	}
	if strings.TrimSpace(result.ID) == "" {
		return nil, fmt.Errorf("lifecycle result id is required")
	}
	if result.StartedAt.IsZero() {
		return nil, fmt.Errorf("lifecycle result started_at is required")
	}
	if result.Status != model.DrillStatusUnknown {
		return nil, fmt.Errorf("lifecycle result must start with status %q", model.DrillStatusUnknown)
	}
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	attemptID = strings.TrimSpace(attemptID)
	if attemptID == "" {
		attemptID = derivedAttemptID(result.ID, result.StartedAt)
	}
	result.AttemptID = attemptID
	emitter, err := newEventEmitter(eventSink, result.ID, attemptID, result.SpecDigest, clock)
	if err != nil {
		return nil, err
	}
	return &runLifecycle{
		result:              result,
		reportSink:          reportSink,
		events:              emitter,
		clock:               clock,
		finalizationTimeout: finalizationTimeout,
	}, nil
}

func (l *runLifecycle) Start(ctx context.Context) error {
	if l == nil {
		return fmt.Errorf("lifecycle is required")
	}
	if l.started {
		return fmt.Errorf("lifecycle already started")
	}
	if l.terminal {
		return fmt.Errorf("lifecycle already finished")
	}
	l.started = true
	attributes := map[string]string{
		"report_schema":   l.result.SchemaVersion,
		"pgdrill_version": l.result.PGDrillVersion,
	}
	if l.result.Spec != nil {
		attributes["drill_spec_schema"] = l.result.Spec.SchemaVersion
	}
	if l.result.SpecDigest != "" {
		attributes["spec_digest"] = l.result.SpecDigest
	}
	err := l.writeEvent(ctx, func(eventCtx context.Context) error {
		return l.events.runStarted(eventCtx, attributes)
	})
	if err == nil {
		l.eventStreamStarted = true
	}
	return err
}

func (l *runLifecycle) RunStage(ctx context.Context, stage model.DrillStage, operation func() error) error {
	return l.runStage(ctx, stage, operation, false)
}

// RunFinalizationStage always executes the operation even when the configured
// event sink cannot accept the stage_started event. Cleanup must not be skipped
// merely because its journal is temporarily unavailable.
func (l *runLifecycle) RunFinalizationStage(ctx context.Context, stage model.DrillStage, operation func() error) error {
	return l.runStage(ctx, stage, operation, true)
}

func (l *runLifecycle) runStage(ctx context.Context, stage model.DrillStage, operation func() error, alwaysRun bool) error {
	if l == nil {
		return fmt.Errorf("lifecycle is required")
	}
	if !l.started {
		return fmt.Errorf("lifecycle is not started")
	}
	if l.terminal {
		return fmt.Errorf("lifecycle already finished")
	}
	if !stage.IsKnown() {
		return fmt.Errorf("unsupported lifecycle stage %q", stage)
	}
	if l.activeStage != "" {
		return fmt.Errorf("lifecycle stage %q is already active", l.activeStage)
	}
	if operation == nil {
		return fmt.Errorf("lifecycle stage %q operation is required", stage)
	}

	l.activeStage = stage
	startEventErr := l.writeEvent(ctx, func(eventCtx context.Context) error {
		return l.events.stageStarted(eventCtx, stage)
	})
	if startEventErr != nil && !alwaysRun {
		l.activeStage = ""
		return startEventErr
	}

	operationErr := operation()
	if startEventErr != nil {
		l.activeStage = ""
		return errors.Join(startEventErr, operationErr)
	}
	outcome := model.StageOutcomeSucceeded
	message := ""
	if operationErr != nil {
		outcome = model.StageOutcomeFailed
		message = operationErr.Error()
		if contextTerminated(ctx) {
			outcome = model.StageOutcomeAborted
		}
	}
	l.activeStage = ""
	eventErr := l.writeEvent(ctx, func(eventCtx context.Context) error {
		return l.events.stageCompleted(eventCtx, stage, outcome, message)
	})
	return errors.Join(startEventErr, operationErr, eventErr)
}

func (l *runLifecycle) Finish(ctx context.Context, status model.DrillStatus, runErr error) (model.DrillResult, error) {
	if l == nil {
		return model.DrillResult{}, fmt.Errorf("lifecycle is required")
	}
	if !l.started {
		return *l.result, fmt.Errorf("lifecycle is not started")
	}
	if l.terminal {
		return *l.result, fmt.Errorf("lifecycle already finished")
	}
	if l.activeStage != "" {
		return *l.result, fmt.Errorf("cannot finish lifecycle while stage %q is active", l.activeStage)
	}
	if !status.IsTerminal() {
		return *l.result, fmt.Errorf("lifecycle requires a terminal status, got %q", status)
	}

	l.terminal = true
	l.result.FinishedAt = l.clock().UTC()
	l.result.Status = status
	finalErr := runErr

	reportWritten := false
	if l.reportSink != nil {
		if err := l.writeReport(ctx); err != nil {
			writeErr := fmt.Errorf("write evidence: %w", err)
			finalErr = errors.Join(finalErr, writeErr)
			l.markFinalizationFailure(writeErr)
		} else {
			reportWritten = true
		}
	}

	message := ""
	if finalErr != nil {
		message = finalErr.Error()
	}
	if l.eventStreamStarted {
		l.finishEvent(ctx, message, reportWritten, &finalErr)
	}

	return *l.result, finalErr
}

func (l *runLifecycle) finishEvent(ctx context.Context, message string, reportWritten bool, finalErr *error) {
	err := l.writeEvent(ctx, func(eventCtx context.Context) error {
		return l.events.runFinished(eventCtx, l.result.Status, message)
	})
	if err != nil {
		eventErr := fmt.Errorf("write terminal run event: %w", err)
		*finalErr = errors.Join(*finalErr, eventErr)
		wasPassed := l.result.Status == model.DrillStatusPassed
		l.markFinalizationFailure(eventErr)
		if wasPassed && reportWritten && l.reportSink != nil {
			if rewriteErr := l.writeReport(ctx); rewriteErr != nil {
				*finalErr = errors.Join(*finalErr, fmt.Errorf("rewrite evidence after terminal event failure: %w", rewriteErr))
			}
		}
		fallbackMessage := (*finalErr).Error()
		if fallbackErr := l.writeEvent(ctx, func(eventCtx context.Context) error {
			return l.events.runFinished(eventCtx, l.result.Status, fallbackMessage)
		}); fallbackErr != nil {
			*finalErr = errors.Join(*finalErr, fmt.Errorf("write fallback terminal run event: %w", fallbackErr))
		}
	}
}

func (l *runLifecycle) markFinalizationFailure(err error) {
	if l.result.Failure == nil {
		l.result.Failure = model.NewDrillFailure(model.DrillStageReportWrite, err, l.result.Evidence)
	}
	if l.result.Status == model.DrillStatusPassed {
		l.result.Status = model.DrillStatusFailed
	}
}

func (l *runLifecycle) writeReport(ctx context.Context) error {
	writeCtx, cancel := finalize.Context(ctx, l.finalizationTimeout)
	err := l.reportSink.Write(writeCtx, *l.result)
	cancel()
	return err
}

func (l *runLifecycle) writeEvent(ctx context.Context, write func(context.Context) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() == nil {
		return write(ctx)
	}
	eventCtx, cancel := finalize.Context(ctx, l.finalizationTimeout)
	err := write(eventCtx)
	cancel()
	return err
}

func contextTerminated(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	return errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded)
}
