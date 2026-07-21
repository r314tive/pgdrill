package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
)

func TestRunLifecycleRecordsStagesAndTerminalResult(t *testing.T) {
	startedAt := time.Date(2026, 7, 21, 1, 0, 0, 0, time.UTC)
	now := startedAt
	clock := func() time.Time {
		now = now.Add(time.Second)
		return now
	}
	result := baseLifecycleResult(startedAt)
	events := []model.RunEvent{}
	reports := []model.DrillResult{}
	lifecycle, err := newRunLifecycle(
		&result,
		"attempt-1",
		evidenceSinkFunc(func(_ context.Context, result model.DrillResult) error {
			reports = append(reports, result)
			return nil
		}),
		EventSinkFunc(func(_ context.Context, event model.RunEvent) error {
			events = append(events, event)
			return nil
		}),
		clock,
		0,
	)
	if err != nil {
		t.Fatalf("newRunLifecycle() error = %v", err)
	}
	if err := lifecycle.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := lifecycle.RunStage(context.Background(), model.DrillStageRequestValidation, func() error { return nil }); err != nil {
		t.Fatalf("RunStage() error = %v", err)
	}
	got, err := lifecycle.Finish(context.Background(), model.DrillStatusPassed, nil)
	if err != nil {
		t.Fatalf("Finish() error = %v", err)
	}

	if got.Status != model.DrillStatusPassed || got.FinishedAt.IsZero() {
		t.Fatalf("unexpected result %#v", got)
	}
	if len(reports) != 1 || reports[0].Status != model.DrillStatusPassed {
		t.Fatalf("unexpected reports %#v", reports)
	}
	if len(events) != 4 {
		t.Fatalf("got %d events, want 4: %#v", len(events), events)
	}
	if events[0].Type != model.RunEventStarted || events[1].Type != model.RunEventStageStarted ||
		events[2].Type != model.RunEventStageCompleted || events[2].Outcome != model.StageOutcomeSucceeded ||
		events[3].Type != model.RunEventFinished || events[3].Status != model.DrillStatusPassed {
		t.Fatalf("unexpected events %#v", events)
	}
}

func TestRunLifecycleRecordsFailedAndAbortedStageOutcomes(t *testing.T) {
	for _, tt := range []struct {
		name    string
		ctx     func() context.Context
		outcome model.StageOutcome
	}{
		{name: "failed", ctx: context.Background, outcome: model.StageOutcomeFailed},
		{name: "aborted", ctx: func() context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		}, outcome: model.StageOutcomeAborted},
	} {
		t.Run(tt.name, func(t *testing.T) {
			result := baseLifecycleResult(time.Now().UTC())
			events := []model.RunEvent{}
			lifecycle, err := newRunLifecycle(&result, "attempt-1", nil, EventSinkFunc(func(_ context.Context, event model.RunEvent) error {
				events = append(events, event)
				return nil
			}), nil, 0)
			if err != nil {
				t.Fatalf("newRunLifecycle() error = %v", err)
			}
			ctx := tt.ctx()
			if err := lifecycle.Start(ctx); err != nil {
				t.Fatalf("Start() error = %v", err)
			}
			wantErr := errors.New("operation failed")
			err = lifecycle.RunStage(ctx, model.DrillStageProbeExecution, func() error { return wantErr })
			if !errors.Is(err, wantErr) {
				t.Fatalf("RunStage() error = %v, want operation failure", err)
			}
			if got := events[len(events)-1].Outcome; got != tt.outcome {
				t.Fatalf("stage outcome = %q, want %q", got, tt.outcome)
			}
		})
	}
}

func TestRunLifecycleRejectsInvalidTransitions(t *testing.T) {
	result := baseLifecycleResult(time.Now().UTC())
	lifecycle, err := newRunLifecycle(&result, "attempt-1", nil, nil, nil, 0)
	if err != nil {
		t.Fatalf("newRunLifecycle() error = %v", err)
	}
	if err := lifecycle.RunStage(context.Background(), model.DrillStagePreflight, func() error { return nil }); err == nil || !strings.Contains(err.Error(), "not started") {
		t.Fatalf("RunStage() before Start error = %v", err)
	}
	if err := lifecycle.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := lifecycle.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "already started") {
		t.Fatalf("second Start() error = %v", err)
	}
	if err := lifecycle.RunStage(context.Background(), "future", func() error { return nil }); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unknown stage error = %v", err)
	}
	if err := lifecycle.RunStage(context.Background(), model.DrillStagePreflight, nil); err == nil || !strings.Contains(err.Error(), "operation") {
		t.Fatalf("nil operation error = %v", err)
	}
	if _, err := lifecycle.Finish(context.Background(), model.DrillStatusUnknown, nil); err == nil || !strings.Contains(err.Error(), "terminal status") {
		t.Fatalf("non-terminal Finish() error = %v", err)
	}
	if _, err := lifecycle.Finish(context.Background(), model.DrillStatusPassed, nil); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
	if _, err := lifecycle.Finish(context.Background(), model.DrillStatusPassed, nil); err == nil || !strings.Contains(err.Error(), "already finished") {
		t.Fatalf("second Finish() error = %v", err)
	}
}

func TestRunLifecycleMarksTerminalEventFailureAndRewritesReport(t *testing.T) {
	result := baseLifecycleResult(time.Now().UTC())
	reports := []model.DrillResult{}
	wantErr := errors.New("event store unavailable")
	lifecycle, err := newRunLifecycle(
		&result,
		"attempt-1",
		evidenceSinkFunc(func(_ context.Context, result model.DrillResult) error {
			reports = append(reports, result)
			return nil
		}),
		EventSinkFunc(func(_ context.Context, event model.RunEvent) error {
			if event.Type == model.RunEventFinished {
				return wantErr
			}
			return nil
		}),
		nil,
		0,
	)
	if err != nil {
		t.Fatalf("newRunLifecycle() error = %v", err)
	}
	if err := lifecycle.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	got, err := lifecycle.Finish(context.Background(), model.DrillStatusPassed, nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Finish() error = %v, want event error", err)
	}
	if got.Status != model.DrillStatusFailed || got.Failure == nil || got.Failure.Stage != model.DrillStageReportWrite {
		t.Fatalf("unexpected final result %#v", got)
	}
	if len(reports) != 2 || reports[0].Status != model.DrillStatusPassed || reports[1].Status != model.DrillStatusFailed {
		t.Fatalf("expected passed report followed by failed rewrite, got %#v", reports)
	}
}

func TestRunLifecycleWritesFailedTerminalFallbackWithSameSequence(t *testing.T) {
	result := baseLifecycleResult(time.Now().UTC())
	wantErr := errors.New("reject passed terminal event")
	attempts := []model.RunEvent{}
	reports := []model.DrillResult{}
	lifecycle, err := newRunLifecycle(
		&result,
		"attempt-1",
		evidenceSinkFunc(func(_ context.Context, result model.DrillResult) error {
			reports = append(reports, result)
			return nil
		}),
		EventSinkFunc(func(_ context.Context, event model.RunEvent) error {
			attempts = append(attempts, event)
			if event.Type == model.RunEventFinished && event.Status == model.DrillStatusPassed {
				return wantErr
			}
			return nil
		}),
		nil,
		0,
	)
	if err != nil {
		t.Fatalf("newRunLifecycle() error = %v", err)
	}
	if err := lifecycle.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	got, err := lifecycle.Finish(context.Background(), model.DrillStatusPassed, nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Finish() error = %v, want terminal event failure", err)
	}
	if got.Status != model.DrillStatusFailed || len(reports) != 2 || reports[1].Status != model.DrillStatusFailed {
		t.Fatalf("unexpected finalization result=%#v reports=%#v", got, reports)
	}
	if len(attempts) != 3 {
		t.Fatalf("terminal attempts = %#v, want run_started plus two terminal attempts", attempts)
	}
	passed, failed := attempts[1], attempts[2]
	if passed.Type != model.RunEventFinished || passed.Status != model.DrillStatusPassed ||
		failed.Type != model.RunEventFinished || failed.Status != model.DrillStatusFailed ||
		passed.Sequence != failed.Sequence {
		t.Fatalf("terminal fallback attempts = %#v and %#v", passed, failed)
	}
}

func TestRunLifecycleSuppressesTerminalEventAfterRejectedRunStart(t *testing.T) {
	result := baseLifecycleResult(time.Now().UTC())
	wantErr := errors.New("reject run start")
	attempts := []model.RunEvent{}
	lifecycle, err := newRunLifecycle(
		&result,
		"attempt-1",
		evidenceSinkFunc(func(context.Context, model.DrillResult) error { return nil }),
		EventSinkFunc(func(_ context.Context, event model.RunEvent) error {
			attempts = append(attempts, event)
			if event.Type == model.RunEventStarted {
				return wantErr
			}
			return nil
		}),
		nil,
		0,
	)
	if err != nil {
		t.Fatalf("newRunLifecycle() error = %v", err)
	}
	if err := lifecycle.Start(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("Start() error = %v, want run start failure", err)
	}
	got, err := lifecycle.Finish(context.Background(), model.DrillStatusFailed, wantErr)
	if !errors.Is(err, wantErr) || got.Status != model.DrillStatusFailed {
		t.Fatalf("Finish() = (%#v, %v)", got, err)
	}
	if len(attempts) != 1 || attempts[0].Type != model.RunEventStarted {
		t.Fatalf("events after rejected run start = %#v", attempts)
	}
}

func TestRunLifecycleRetriesRejectedFailedTerminalEvent(t *testing.T) {
	result := baseLifecycleResult(time.Now().UTC())
	wantRunErr := errors.New("probe failed")
	wantEventErr := errors.New("terminal write unavailable")
	attempts := []model.RunEvent{}
	terminalCalls := 0
	lifecycle, err := newRunLifecycle(
		&result,
		"attempt-1",
		nil,
		EventSinkFunc(func(_ context.Context, event model.RunEvent) error {
			attempts = append(attempts, event)
			if event.Type == model.RunEventFinished {
				terminalCalls++
				if terminalCalls == 1 {
					return wantEventErr
				}
			}
			return nil
		}),
		nil,
		0,
	)
	if err != nil {
		t.Fatalf("newRunLifecycle() error = %v", err)
	}
	if err := lifecycle.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	result.Failure = model.NewDrillFailure(model.DrillStageProbeExecution, wantRunErr, nil)
	got, err := lifecycle.Finish(context.Background(), model.DrillStatusFailed, wantRunErr)
	if !errors.Is(err, wantRunErr) || !errors.Is(err, wantEventErr) {
		t.Fatalf("Finish() error = %v", err)
	}
	if got.Status != model.DrillStatusFailed || terminalCalls != 2 {
		t.Fatalf("result=%#v terminal_calls=%d", got, terminalCalls)
	}
	first, second := attempts[len(attempts)-2], attempts[len(attempts)-1]
	if first.Sequence != second.Sequence || first.Status != model.DrillStatusFailed || second.Status != model.DrillStatusFailed {
		t.Fatalf("terminal attempts = %#v and %#v", first, second)
	}
}

func TestRunLifecycleMarksReportFailureBeforeTerminalEvent(t *testing.T) {
	result := baseLifecycleResult(time.Now().UTC())
	wantErr := errors.New("report store unavailable")
	events := []model.RunEvent{}
	lifecycle, err := newRunLifecycle(
		&result,
		"attempt-1",
		evidenceSinkFunc(func(context.Context, model.DrillResult) error { return wantErr }),
		EventSinkFunc(func(_ context.Context, event model.RunEvent) error {
			events = append(events, event)
			return nil
		}),
		nil,
		0,
	)
	if err != nil {
		t.Fatalf("newRunLifecycle() error = %v", err)
	}
	if err := lifecycle.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	got, err := lifecycle.Finish(context.Background(), model.DrillStatusPassed, nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Finish() error = %v, want report error", err)
	}
	if got.Status != model.DrillStatusFailed || got.Failure == nil || got.Failure.Stage != model.DrillStageReportWrite {
		t.Fatalf("unexpected final result %#v", got)
	}
	if last := events[len(events)-1]; last.Type != model.RunEventFinished || last.Status != model.DrillStatusFailed {
		t.Fatalf("terminal event = %#v, want failed", last)
	}
}

func TestRunLifecycleFinalizationStageRunsWhenStartEventFails(t *testing.T) {
	result := baseLifecycleResult(time.Now().UTC())
	wantErr := errors.New("event store unavailable")
	operations := 0
	events := []model.RunEvent{}
	lifecycle, err := newRunLifecycle(
		&result,
		"attempt-1",
		nil,
		EventSinkFunc(func(_ context.Context, event model.RunEvent) error {
			if event.Type == model.RunEventStageStarted {
				return wantErr
			}
			events = append(events, event)
			return nil
		}),
		nil,
		0,
	)
	if err != nil {
		t.Fatalf("newRunLifecycle() error = %v", err)
	}
	if err := lifecycle.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	err = lifecycle.RunFinalizationStage(context.Background(), model.DrillStageTargetCleanup, func() error {
		operations++
		return nil
	})
	if operations != 1 {
		t.Fatalf("finalization operation ran %d times, want 1", operations)
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("RunFinalizationStage() error = %v, want event error", err)
	}
	for _, event := range events {
		if event.Type == model.RunEventStageCompleted {
			t.Fatalf("accepted stage_completed without stage_started: %#v", events)
		}
	}
}

func TestNewRunLifecycleValidatesBaseResult(t *testing.T) {
	valid := baseLifecycleResult(time.Now().UTC())
	tests := []struct {
		name string
		edit func(*model.DrillResult)
		want string
	}{
		{name: "nil", edit: nil, want: "result"},
		{name: "id", edit: func(r *model.DrillResult) { r.ID = "" }, want: "id"},
		{name: "started at", edit: func(r *model.DrillResult) { r.StartedAt = time.Time{} }, want: "started_at"},
		{name: "status", edit: func(r *model.DrillResult) { r.Status = model.DrillStatusPassed }, want: "must start"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var result *model.DrillResult
			if tt.edit != nil {
				copy := valid
				tt.edit(&copy)
				result = &copy
			}
			_, err := newRunLifecycle(result, "attempt-1", nil, nil, nil, 0)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("newRunLifecycle() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func baseLifecycleResult(startedAt time.Time) model.DrillResult {
	return model.DrillResult{
		SchemaVersion:  model.CurrentReportSchemaVersion,
		PGDrillVersion: "pgdrill test",
		ID:             "run-1",
		Target:         model.TargetSpec{Type: model.RestoreTargetLocal},
		RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetLatest},
		StartedAt:      startedAt,
		Status:         model.DrillStatusUnknown,
	}
}

type evidenceSinkFunc func(context.Context, model.DrillResult) error

func (f evidenceSinkFunc) Write(ctx context.Context, result model.DrillResult) error {
	return f(ctx, result)
}
