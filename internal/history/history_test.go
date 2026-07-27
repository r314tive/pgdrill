package history

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
	"github.com/r314tive/pgdrill/internal/runspec"
)

func TestDirectoryStoreRejectsBlankPathWithoutCreatingState(t *testing.T) {
	store := DirectoryStore{Path: " \t "}
	if _, err := store.root(); err == nil || !strings.Contains(err.Error(), "path is required") {
		t.Fatalf("root() error = %v, want required path", err)
	}
	if _, err := store.List(context.Background()); err == nil || !strings.Contains(err.Error(), "path is required") {
		t.Fatalf("List() error = %v, want required path", err)
	}
}

func TestDirectoryStorePersistsOrderedEventsSpecAndReport(t *testing.T) {
	t.Parallel()

	store := DirectoryStore{Path: filepath.Join(t.TempDir(), "history")}
	result := validResult(t, "run-1", "attempt-1", model.DrillStatusPassed)
	events := validEvents(result)
	for _, event := range events {
		if err := store.WriteEvent(context.Background(), event); err != nil {
			t.Fatalf("WriteEvent(%d) error = %v", event.Sequence, err)
		}
	}
	if err := store.SaveReport(context.Background(), result); err != nil {
		t.Fatalf("SaveReport() error = %v", err)
	}
	for _, event := range events {
		if err := store.WriteEvent(context.Background(), event); err != nil {
			t.Fatalf("WriteEvent(exact terminal retry %d) error = %v", event.Sequence, err)
		}
	}

	record, err := store.Show(context.Background(), result.ID)
	if err != nil {
		t.Fatalf("Show() error = %v", err)
	}
	if record.SchemaVersion != CurrentViewSchemaVersion || record.RunID != result.ID || record.Spec == nil {
		t.Fatalf("record = %#v", record)
	}
	if len(record.Attempts) != 1 || len(record.Attempts[0].Events) != 2 || record.Attempts[0].Report == nil {
		t.Fatalf("attempts = %#v", record.Attempts)
	}
	if record.Attempts[0].Report.Status != model.DrillStatusPassed {
		t.Fatalf("report status = %q", record.Attempts[0].Report.Status)
	}

	summaries, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("summaries = %#v", summaries)
	}
	summary := summaries[0]
	if summary.Status != model.DrillStatusPassed || !summary.ReportAvailable || summary.EventCount != 2 {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestDirectoryStoreSupportsImmutableRetries(t *testing.T) {
	t.Parallel()

	store := DirectoryStore{Path: filepath.Join(t.TempDir(), "history")}
	first := validResult(t, "planned-run", "attempt-1", model.DrillStatusFailed)
	second := validResult(t, "planned-run", "attempt-2", model.DrillStatusPassed)
	second.Spec = first.Spec
	second.SpecDigest = first.SpecDigest
	second.Cluster = first.Cluster
	second.StartedAt = first.StartedAt.Add(time.Hour)
	second.FinishedAt = second.StartedAt.Add(time.Minute)

	for _, result := range []model.DrillResult{first, second} {
		for _, event := range validEvents(result) {
			if err := store.WriteEvent(context.Background(), event); err != nil {
				t.Fatalf("WriteEvent(%s/%d) error = %v", result.AttemptID, event.Sequence, err)
			}
		}
		if err := store.SaveReport(context.Background(), result); err != nil {
			t.Fatalf("SaveReport(%s) error = %v", result.AttemptID, err)
		}
	}

	record, err := store.Show(context.Background(), first.ID)
	if err != nil {
		t.Fatalf("Show() error = %v", err)
	}
	if len(record.Attempts) != 2 || record.Attempts[0].AttemptID != "attempt-1" || record.Attempts[1].AttemptID != "attempt-2" {
		t.Fatalf("attempt order = %#v", record.Attempts)
	}
	selected, err := store.ShowAttempt(context.Background(), first.ID, second.AttemptID)
	if err != nil {
		t.Fatalf("ShowAttempt() error = %v", err)
	}
	if len(selected.Attempts) != 1 || selected.Attempts[0].AttemptID != second.AttemptID ||
		selected.Attempts[0].Report == nil || selected.Attempts[0].Report.Status != model.DrillStatusPassed {
		t.Fatalf("selected attempt = %#v", selected.Attempts)
	}
	if _, err := store.ShowAttempt(context.Background(), first.ID, "missing"); err == nil || !strings.Contains(err.Error(), "was not found") {
		t.Fatalf("ShowAttempt(missing) error = %v", err)
	}
	summaries, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(summaries) != 2 || summaries[0].AttemptID != "attempt-2" {
		t.Fatalf("summary order = %#v", summaries)
	}
}

func TestDirectoryStoreShowAttemptDoesNotDecodeUnrelatedReports(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "history")
	store := DirectoryStore{Path: root}
	first := validResult(t, "run-addressed", "attempt-1", model.DrillStatusFailed)
	second := validResult(t, "run-addressed", "attempt-2", model.DrillStatusPassed)
	second.Spec = first.Spec
	second.SpecDigest = first.SpecDigest
	second.Cluster = first.Cluster
	second.StartedAt = first.StartedAt.Add(time.Hour)
	second.FinishedAt = second.StartedAt.Add(time.Minute)
	for _, result := range []model.DrillResult{first, second} {
		for _, event := range validEvents(result) {
			if err := store.WriteEvent(context.Background(), event); err != nil {
				t.Fatal(err)
			}
		}
		if err := store.SaveReport(context.Background(), result); err != nil {
			t.Fatal(err)
		}
	}
	firstReport := filepath.Join(
		root,
		"runs",
		runDirectoryName(first.ID),
		"attempts",
		attemptDirectoryName(first.ID, first.AttemptID),
		"report.json",
	)
	if err := os.WriteFile(firstReport, []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	selected, err := store.ShowAttempt(context.Background(), second.ID, second.AttemptID)
	if err != nil {
		t.Fatalf("ShowAttempt() error = %v", err)
	}
	if len(selected.Attempts) != 1 || selected.Attempts[0].AttemptID != second.AttemptID {
		t.Fatalf("selected attempts = %#v", selected.Attempts)
	}
	if _, err := store.Show(context.Background(), first.ID); err == nil || !strings.Contains(err.Error(), "report") {
		t.Fatalf("Show() error = %v, want unrelated report corruption", err)
	}
}

func TestDirectoryStoreIsIdempotentUnderConcurrentExactRetries(t *testing.T) {
	t.Parallel()

	store := DirectoryStore{Path: filepath.Join(t.TempDir(), "history")}
	result := validResult(t, "run-concurrent", "attempt-1", model.DrillStatusPassed)
	event := validEvents(result)[0]
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- store.WriteEvent(context.Background(), event)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent WriteEvent() error = %v", err)
		}
	}
	record, err := store.Show(context.Background(), result.ID)
	if err != nil {
		t.Fatalf("Show() error = %v", err)
	}
	if len(record.Attempts) != 1 || len(record.Attempts[0].Events) != 1 {
		t.Fatalf("record = %#v", record)
	}
}

func TestDirectoryStoreRejectsEventGapsConflictsAndPostTerminalWrites(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		run  func(t *testing.T, store DirectoryStore, result model.DrillResult) error
		want string
	}{
		{
			name: "gap",
			run: func(t *testing.T, store DirectoryStore, result model.DrillResult) error {
				t.Helper()
				events := validEvents(result)
				return store.WriteEvent(context.Background(), events[1])
			},
			want: "before sequence 1",
		},
		{
			name: "conflict",
			run: func(t *testing.T, store DirectoryStore, result model.DrillResult) error {
				t.Helper()
				event := validEvents(result)[0]
				if err := store.WriteEvent(context.Background(), event); err != nil {
					t.Fatal(err)
				}
				event.Message = "different"
				return store.WriteEvent(context.Background(), event)
			},
			want: "immutable",
		},
		{
			name: "post terminal",
			run: func(t *testing.T, store DirectoryStore, result model.DrillResult) error {
				t.Helper()
				events := validEvents(result)
				for _, event := range events {
					if err := store.WriteEvent(context.Background(), event); err != nil {
						t.Fatal(err)
					}
				}
				event := events[1]
				event.Sequence = 3
				event.Type = model.RunEventStageStarted
				event.Stage = model.DrillStagePreflight
				event.Status = ""
				event.OccurredAt = event.OccurredAt.Add(time.Second)
				return store.WriteEvent(context.Background(), event)
			},
			want: "already terminal",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := DirectoryStore{Path: filepath.Join(t.TempDir(), "history")}
			result := validResult(t, "run-"+tt.name, "attempt-1", model.DrillStatusPassed)
			err := tt.run(t, store, result)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestDirectoryStoreRejectsIdentityAndTerminalStatusChanges(t *testing.T) {
	t.Parallel()

	store := DirectoryStore{Path: filepath.Join(t.TempDir(), "history")}
	passed := validResult(t, "run-immutable", "attempt-1", model.DrillStatusPassed)
	for _, event := range validEvents(passed) {
		if err := store.WriteEvent(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	failed := passed
	failed.Status = model.DrillStatusFailed
	failed.Failure = model.NewDrillFailure(model.DrillStageProbeExecution, errors.New("probe failed"), nil)
	if err := store.SaveReport(context.Background(), failed); err == nil || !strings.Contains(err.Error(), "does not match terminal event") {
		t.Fatalf("SaveReport(status mismatch) error = %v", err)
	}

	other := validResult(t, passed.ID, "attempt-2", model.DrillStatusPassed)
	other.Spec.Source.Ref.Revision = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	spec, err := runspec.New(*other.Spec)
	if err != nil {
		t.Fatal(err)
	}
	document := spec.Document()
	other.Spec = &document
	other.SpecDigest = spec.Digest()
	if err := store.SaveReport(context.Background(), other); err == nil || !strings.Contains(err.Error(), "different content") {
		t.Fatalf("SaveReport(identity change) error = %v", err)
	}
}

func TestDirectoryStoreRejectsOversizedEvents(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "history")
	store := DirectoryStore{Path: root}
	result := validResult(t, "run-large", "attempt-1", model.DrillStatusPassed)
	event := validEvents(result)[0]
	event.Message = strings.Repeat("x", MaxEventBytes)
	if err := store.WriteEvent(context.Background(), event); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("WriteEvent() error = %v", err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("oversized event created history store, stat error = %v", err)
	}
}

func TestHistoryReadersRejectAggregatePayloadsBeforeDecoding(t *testing.T) {
	t.Parallel()

	attemptDir := filepath.Join(t.TempDir(), "attempt")
	eventsDir := filepath.Join(attemptDir, "events")
	if err := os.MkdirAll(eventsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for sequence := uint64(1); sequence <= MaxAttemptEventBytes/MaxEventBytes+1; sequence++ {
		path := filepath.Join(eventsDir, eventFileName(sequence))
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(int64(MaxEventBytes)); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	identity := AttemptIdentity{
		SchemaVersion: CurrentAttemptSchemaVersion,
		RunID:         "aggregate-run",
		AttemptID:     "attempt-1",
		SpecDigest:    "sha256:" + strings.Repeat("a", 64),
	}
	if _, err := readEvents(attemptDir, identity); err == nil || !strings.Contains(err.Error(), "maximum event bytes") {
		t.Fatalf("readEvents() error = %v, want aggregate byte bound", err)
	}

	runDir := filepath.Join(t.TempDir(), "run")
	attemptsDir := filepath.Join(runDir, "attempts")
	if err := os.MkdirAll(attemptsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for index := 1; index <= MaxRunReportBytes/MaxReportBytes+1; index++ {
		reportDir := filepath.Join(attemptsDir, fmt.Sprintf("%064x", index))
		if err := os.Mkdir(reportDir, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(reportDir, "report.json")
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(int64(MaxReportBytes)); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	total, err := countRunReports(runDir)
	if err != nil {
		t.Fatalf("countRunReports() error = %v", err)
	}
	if total <= MaxRunReportBytes {
		t.Fatalf("countRunReports() = %d, want more than %d", total, MaxRunReportBytes)
	}
}

func TestDirectoryStoreRejectsWriterEventBoundBeforeCreatingStore(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "history")
	store := DirectoryStore{Path: root}
	result := validResult(t, "run-event-bound", "attempt-1", model.DrillStatusPassed)
	event := validEvents(result)[0]
	event.Sequence = MaxEventsPerAttempt + 1
	if err := store.WriteEvent(context.Background(), event); err == nil || !strings.Contains(err.Error(), "maximum event count") {
		t.Fatalf("WriteEvent() error = %v, want writer event bound", err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bounded event created history store, stat error = %v", err)
	}
}

func TestDirectoryStoreRejectsLegacyEventBeforeCreatingStore(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "history")
	store := DirectoryStore{Path: root}
	result := validResult(t, "run-legacy-event", "attempt-1", model.DrillStatusPassed)
	event := validEvents(result)[0]
	event.SchemaVersion = model.LegacyRunEventSchemaVersion
	if err := store.WriteEvent(context.Background(), event); err == nil ||
		!strings.Contains(err.Error(), model.CurrentRunEventSchemaVersion) {
		t.Fatalf("WriteEvent() legacy schema error = %v", err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy event created history store, stat error = %v", err)
	}
}

func TestDirectoryStoreRejectsTerminalReportForIncompleteEventStream(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "history")
	store := DirectoryStore{Path: root}
	result := validResult(t, "run-incomplete", "attempt-1", model.DrillStatusPassed)
	if err := store.WriteEvent(context.Background(), validEvents(result)[0]); err != nil {
		t.Fatalf("WriteEvent() error = %v", err)
	}
	if err := store.SaveReport(context.Background(), result); err == nil || !strings.Contains(err.Error(), "requires a run_finished event") {
		t.Fatalf("SaveReport() error = %v, want incomplete event-stream rejection", err)
	}
	reportPath := filepath.Join(
		root,
		"runs",
		runDirectoryName(result.ID),
		"attempts",
		attemptDirectoryName(result.ID, result.AttemptID),
		"report.json",
	)
	if _, err := os.Stat(reportPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("incomplete event stream published a report, stat error = %v", err)
	}
}

func TestDirectoryStoreRejectsGapWithoutCreatingRunIdentity(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "history")
	store := DirectoryStore{Path: root}
	result := validResult(t, "run-gap-state", "attempt-1", model.DrillStatusPassed)
	event := validEvents(result)[1]
	if err := store.WriteEvent(context.Background(), event); err == nil || !strings.Contains(err.Error(), "before sequence 1") {
		t.Fatalf("WriteEvent() error = %v, want event gap", err)
	}
	if _, err := os.Stat(filepath.Join(root, "runs")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("event gap created run state, stat error = %v", err)
	}
}

func TestDirectoryStoreFreezesEventStreamAfterTerminalReport(t *testing.T) {
	t.Parallel()

	store := DirectoryStore{Path: filepath.Join(t.TempDir(), "history")}
	result := validResult(t, "run-frozen", "attempt-1", model.DrillStatusPassed)
	if err := store.SaveReport(context.Background(), result); err != nil {
		t.Fatalf("SaveReport() error = %v", err)
	}
	if err := store.WriteEvent(context.Background(), validEvents(result)[0]); err == nil || !strings.Contains(err.Error(), "terminal report") {
		t.Fatalf("WriteEvent() error = %v, want frozen stream", err)
	}
}

func TestDirectoryStoreListUsesImmutableSummaryAndShowVerifiesFullRecord(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "history")
	store := DirectoryStore{Path: root}
	result := validResult(t, "run-indexed", "attempt-1", model.DrillStatusPassed)
	for _, event := range validEvents(result) {
		if err := store.WriteEvent(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SaveReport(context.Background(), result); err != nil {
		t.Fatalf("SaveReport() error = %v", err)
	}
	attemptDir := filepath.Join(
		root,
		"runs",
		runDirectoryName(result.ID),
		"attempts",
		attemptDirectoryName(result.ID, result.AttemptID),
	)
	index, err := readJSONFile[attemptSummaryIndex](filepath.Join(attemptDir, "summary.json"), MaxIdentityBytes)
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	if err := index.validate(AttemptIdentity{
		SchemaVersion: CurrentAttemptSchemaVersion,
		RunID:         result.ID,
		AttemptID:     result.AttemptID,
		SpecDigest:    result.SpecDigest,
	}); err != nil {
		t.Fatalf("validate summary: %v", err)
	}

	if err := os.WriteFile(filepath.Join(attemptDir, "report.json"), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	summaries, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List() should use summary index, error = %v", err)
	}
	if len(summaries) != 1 || summaries[0].Status != model.DrillStatusPassed {
		t.Fatalf("summaries = %#v", summaries)
	}
	if _, err := store.Show(context.Background(), result.ID); err == nil || !strings.Contains(err.Error(), "report") {
		t.Fatalf("Show() error = %v, want full report verification", err)
	}
}

func TestDirectoryStoreListRejectsSummaryFileSetDrift(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		remove func(root string, result model.DrillResult) string
		want   string
	}{
		{
			name: "missing report",
			remove: func(root string, result model.DrillResult) string {
				return filepath.Join(
					root,
					"runs",
					runDirectoryName(result.ID),
					"attempts",
					attemptDirectoryName(result.ID, result.AttemptID),
					"report.json",
				)
			},
			want: "report availability",
		},
		{
			name: "missing event",
			remove: func(root string, result model.DrillResult) string {
				return filepath.Join(
					root,
					"runs",
					runDirectoryName(result.ID),
					"attempts",
					attemptDirectoryName(result.ID, result.AttemptID),
					"events",
					eventFileName(2),
				)
			},
			want: "event_count",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := filepath.Join(t.TempDir(), "history")
			store := DirectoryStore{Path: root}
			result := validResult(t, "run-list-drift-"+strings.ReplaceAll(test.name, " ", "-"), "attempt-1", model.DrillStatusPassed)
			for _, event := range validEvents(result) {
				if err := store.WriteEvent(context.Background(), event); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.SaveReport(context.Background(), result); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(test.remove(root, result)); err != nil {
				t.Fatal(err)
			}
			if _, err := store.List(context.Background()); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("List() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestDirectoryStoreListFallbackVerifiesTerminalConsistency(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "history")
	store := DirectoryStore{Path: root}
	result := validResult(t, "run-summary-fallback", "attempt-1", model.DrillStatusPassed)
	for _, event := range validEvents(result) {
		if err := store.WriteEvent(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SaveReport(context.Background(), result); err != nil {
		t.Fatal(err)
	}
	attemptDir := filepath.Join(
		root,
		"runs",
		runDirectoryName(result.ID),
		"attempts",
		attemptDirectoryName(result.ID, result.AttemptID),
	)
	if err := os.Remove(filepath.Join(attemptDir, "summary.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(attemptDir, "events", eventFileName(2))); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(context.Background()); err == nil || !strings.Contains(err.Error(), "no terminal event") {
		t.Fatalf("List() error = %v, want terminal-event consistency failure", err)
	}
}

func TestDirectoryStoreExposesInterruptedEventOnlyAttempt(t *testing.T) {
	t.Parallel()

	store := DirectoryStore{Path: filepath.Join(t.TempDir(), "history")}
	result := validResult(t, "run-process-loss", "attempt-1", model.DrillStatusPassed)
	if err := store.WriteEvent(context.Background(), validEvents(result)[0]); err != nil {
		t.Fatalf("WriteEvent() error = %v", err)
	}
	summaries, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(summaries) != 1 ||
		summaries[0].Status != model.DrillStatusUnknown ||
		summaries[0].ReportAvailable ||
		summaries[0].EventCount != 1 {
		t.Fatalf("interrupted attempt summary = %#v", summaries)
	}
	record, err := store.ShowAttempt(context.Background(), result.ID, result.AttemptID)
	if err != nil {
		t.Fatalf("ShowAttempt() error = %v", err)
	}
	if len(record.Attempts) != 1 ||
		record.Attempts[0].Report != nil ||
		len(record.Attempts[0].Events) != 1 {
		t.Fatalf("interrupted attempt record = %#v", record)
	}
}

func TestDirectoryStoreRecoversMissingSummaryAfterTerminalReport(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "history")
	store := DirectoryStore{Path: root}
	result := validResult(t, "run-summary-process-loss", "attempt-1", model.DrillStatusPassed)
	for _, event := range validEvents(result) {
		if err := store.WriteEvent(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SaveReport(context.Background(), result); err != nil {
		t.Fatal(err)
	}
	summaryPath := filepath.Join(
		root,
		"runs",
		runDirectoryName(result.ID),
		"attempts",
		attemptDirectoryName(result.ID, result.AttemptID),
		"summary.json",
	)
	if err := os.Remove(summaryPath); err != nil {
		t.Fatal(err)
	}

	summaries, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List() missing summary error = %v", err)
	}
	if len(summaries) != 1 ||
		summaries[0].Status != model.DrillStatusPassed ||
		!summaries[0].ReportAvailable ||
		summaries[0].EventCount != 2 {
		t.Fatalf("fallback summary = %#v", summaries)
	}
	record, err := store.ShowAttempt(context.Background(), result.ID, result.AttemptID)
	if err != nil {
		t.Fatalf("ShowAttempt() missing summary error = %v", err)
	}
	if len(record.Attempts) != 1 || record.Attempts[0].Report == nil {
		t.Fatalf("fallback record = %#v", record)
	}
}

func TestDirectoryStoreExposesImportedReportWithoutEvents(t *testing.T) {
	t.Parallel()

	store := DirectoryStore{Path: filepath.Join(t.TempDir(), "history")}
	result := validResult(t, "run-imported-report", "attempt-1", model.DrillStatusFailed)
	if err := store.SaveReport(context.Background(), result); err != nil {
		t.Fatalf("SaveReport() error = %v", err)
	}
	summaries, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(summaries) != 1 ||
		summaries[0].Status != model.DrillStatusFailed ||
		!summaries[0].ReportAvailable ||
		summaries[0].EventCount != 0 {
		t.Fatalf("report-only summary = %#v", summaries)
	}
	record, err := store.ShowAttempt(context.Background(), result.ID, result.AttemptID)
	if err != nil {
		t.Fatalf("ShowAttempt() error = %v", err)
	}
	if len(record.Attempts) != 1 ||
		record.Attempts[0].Report == nil ||
		len(record.Attempts[0].Events) != 0 {
		t.Fatalf("report-only record = %#v", record)
	}
}

func TestDirectoryStoreShowRejectsReportWithoutSpecOrTerminalEvent(t *testing.T) {
	t.Parallel()

	t.Run("missing spec", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "history")
		store := DirectoryStore{Path: root}
		result := validResult(t, "run-missing-spec", "attempt-1", model.DrillStatusPassed)
		if err := store.SaveReport(context.Background(), result); err != nil {
			t.Fatal(err)
		}
		specPath := filepath.Join(root, "runs", runDirectoryName(result.ID), "spec.json")
		if err := os.Remove(specPath); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ShowAttempt(context.Background(), result.ID, result.AttemptID); err == nil || !strings.Contains(err.Error(), "without an immutable spec") {
			t.Fatalf("ShowAttempt() error = %v, want missing spec rejection", err)
		}
	})

	t.Run("missing terminal event", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "history")
		store := DirectoryStore{Path: root}
		result := validResult(t, "run-missing-terminal", "attempt-1", model.DrillStatusPassed)
		for _, event := range validEvents(result) {
			if err := store.WriteEvent(context.Background(), event); err != nil {
				t.Fatal(err)
			}
		}
		if err := store.SaveReport(context.Background(), result); err != nil {
			t.Fatal(err)
		}
		terminalPath := filepath.Join(
			root,
			"runs",
			runDirectoryName(result.ID),
			"attempts",
			attemptDirectoryName(result.ID, result.AttemptID),
			"events",
			eventFileName(2),
		)
		if err := os.Remove(terminalPath); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ShowAttempt(context.Background(), result.ID, result.AttemptID); err == nil || !strings.Contains(err.Error(), "no terminal event") {
			t.Fatalf("ShowAttempt() error = %v, want terminal event rejection", err)
		}
	})
}

func TestDirectoryStoreSchemaBootstrapAndUnknownVersionFailure(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "history")
	store := DirectoryStore{Path: root}
	result := validResult(t, "run-schema", "attempt-1", model.DrillStatusPassed)
	if err := store.WriteEvent(context.Background(), validEvents(result)[0]); err != nil {
		t.Fatalf("WriteEvent() error = %v", err)
	}
	metadata, err := readJSONFile[StoreMetadata](filepath.Join(root, "store.json"), MaxIdentityBytes)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.SchemaVersion != CurrentStoreSchemaVersion || metadata.LayoutVersion != CurrentLayoutVersion {
		t.Fatalf("metadata = %#v", metadata)
	}

	metadata.SchemaVersion = "pgdrill.history-store/v999"
	payload, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "store.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(context.Background()); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("List() error = %v, want schema failure", err)
	}
}

func TestDirectoryStoreListDoesNotCreateMissingStore(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "missing")
	store := DirectoryStore{Path: root}
	summaries, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(summaries) != 0 {
		t.Fatalf("summaries = %#v", summaries)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing store was created, stat error = %v", err)
	}
}

func TestDirectoryStoreUsesPrivatePermissionsAndRejectsSymlinkedMetadata(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "history")
	store := DirectoryStore{Path: root}
	result := validResult(t, "run-permissions", "attempt-1", model.DrillStatusPassed)
	if err := store.SaveReport(context.Background(), result); err != nil {
		t.Fatalf("SaveReport() error = %v", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("store permissions = %o, want private", info.Mode().Perm())
	}
	metadataPath := filepath.Join(root, "store.json")
	info, err = os.Stat(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("metadata permissions = %o, want 600", info.Mode().Perm())
	}

	real := filepath.Join(t.TempDir(), "metadata.json")
	if err := os.WriteFile(real, []byte(`{"schema_version":"pgdrill.history-store/v1alpha1","layout_version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(metadataPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, metadataPath); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(context.Background()); err == nil || !strings.Contains(err.Error(), "non-symbolic-link") {
		t.Fatalf("List() error = %v, want symlink rejection", err)
	}
}

func validResult(t *testing.T, runID, attemptID string, status model.DrillStatus) model.DrillResult {
	t.Helper()
	document := model.DrillSpec{
		SchemaVersion: model.CurrentDrillSpecSchemaVersion,
		Mode:          model.DrillModeNative,
		Cluster:       "cluster-1",
		Source: model.BackupSourceSpec{
			Ref: model.ComponentRef{
				ID:       "source-1",
				Driver:   "wal-g",
				Revision: "sha256:1111111111111111111111111111111111111111111111111111111111111111",
			},
			Provider: model.ProviderWALG,
		},
		BackupSelection: model.BackupSelection{Type: model.BackupSelectionLatestAvailable},
		Target: model.RestoreTargetSpec{
			Ref: model.ComponentRef{
				ID:       "target-1",
				Driver:   "local",
				Revision: "sha256:2222222222222222222222222222222222222222222222222222222222222222",
			},
			Spec: model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: "/tmp/restore"},
		},
		RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetLatest},
		ProbeProfile: model.ProbeProfileSpec{
			Ref: model.ComponentRef{
				ID:       "standard",
				Driver:   "probe-profile",
				Revision: "sha256:3333333333333333333333333333333333333333333333333333333333333333",
			},
			Probes: []model.ProbeDescriptor{{Type: model.ProbePGIsReady, Name: "pg_isready"}},
		},
	}
	spec, err := runspec.New(document)
	if err != nil {
		t.Fatal(err)
	}
	canonical := spec.Document()
	started := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	result := model.DrillResult{
		SchemaVersion:  model.CurrentReportSchemaVersion,
		PGDrillVersion: "test",
		ID:             runID,
		AttemptID:      attemptID,
		SpecDigest:     spec.Digest(),
		Spec:           &canonical,
		Cluster:        canonical.Cluster,
		Provider:       model.ProviderWALG,
		Target:         canonical.Target.Spec,
		RecoveryTarget: canonical.RecoveryTarget,
		StartedAt:      started,
		FinishedAt:     started.Add(time.Minute),
		Status:         status,
	}
	if status != model.DrillStatusPassed {
		result.Failure = model.NewDrillFailure(model.DrillStageProbeExecution, errors.New("probe failed"), nil)
	}
	return result
}

func validEvents(result model.DrillResult) []model.RunEvent {
	return []model.RunEvent{
		{
			SchemaVersion: model.CurrentRunEventSchemaVersion,
			RunID:         result.ID,
			AttemptID:     result.AttemptID,
			SpecDigest:    result.SpecDigest,
			Sequence:      1,
			Type:          model.RunEventStarted,
			OccurredAt:    result.StartedAt,
		},
		{
			SchemaVersion: model.CurrentRunEventSchemaVersion,
			RunID:         result.ID,
			AttemptID:     result.AttemptID,
			SpecDigest:    result.SpecDigest,
			Sequence:      2,
			Type:          model.RunEventFinished,
			Status:        result.Status,
			OccurredAt:    result.FinishedAt,
		},
	}
}
