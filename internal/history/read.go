package history

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
	"github.com/r314tive/pgdrill/internal/report"
	"github.com/r314tive/pgdrill/internal/runspec"
)

func (s DirectoryStore) List(ctx context.Context) ([]AttemptSummary, error) {
	summaries := []AttemptSummary{}
	err := s.withReadLock(ctx, func(root string) error {
		runsDir := filepath.Join(root, "runs")
		if err := requireDirectory(runsDir); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		entries, err := os.ReadDir(runsDir)
		if err != nil {
			return fmt.Errorf("read history runs: %w", err)
		}
		if len(entries) > MaxRuns {
			return fmt.Errorf("history store exceeds maximum run count %d", MaxRuns)
		}
		for _, entry := range entries {
			if !entry.IsDir() || !validHashDirectory(entry.Name()) {
				return fmt.Errorf("history runs contains unexpected entry %q", entry.Name())
			}
			runSummaries, err := readRunSummaries(filepath.Join(runsDir, entry.Name()))
			if err != nil {
				return err
			}
			for _, summary := range runSummaries {
				summaries = append(summaries, summary)
				if len(summaries) > MaxTotalAttempts {
					return fmt.Errorf("history store exceeds maximum total attempt count %d", MaxTotalAttempts)
				}
			}
		}
		return nil
	})
	if errors.Is(err, ErrStoreNotFound) {
		return summaries, nil
	}
	if err != nil {
		return nil, err
	}
	sort.Slice(summaries, func(i, j int) bool {
		left := summaries[i]
		right := summaries[j]
		if !left.StartedAt.Equal(right.StartedAt) {
			return left.StartedAt.After(right.StartedAt)
		}
		if left.RunID != right.RunID {
			return left.RunID < right.RunID
		}
		return left.AttemptID < right.AttemptID
	})
	return summaries, nil
}

func readRunSummaries(runDir string) ([]AttemptSummary, error) {
	if err := requireDirectory(runDir); err != nil {
		return nil, err
	}
	runIdentity, err := readJSONFile[RunIdentity](filepath.Join(runDir, "identity.json"), MaxIdentityBytes)
	if err != nil {
		return nil, fmt.Errorf("read history run identity: %w", err)
	}
	if err := runIdentity.validate(); err != nil {
		return nil, fmt.Errorf("validate history run identity: %w", err)
	}
	if filepath.Base(runDir) != runDirectoryName(runIdentity.RunID) {
		return nil, fmt.Errorf("history run %q is stored under the wrong directory", runIdentity.RunID)
	}
	runEventCount, runEventBytes, err := countRunEvents(runDir)
	if err != nil {
		return nil, err
	}
	if runEventCount > MaxEventsPerRun {
		return nil, fmt.Errorf("history run %q exceeds maximum total event count %d", runIdentity.RunID, MaxEventsPerRun)
	}
	if runEventBytes > MaxRunEventBytes {
		return nil, fmt.Errorf("history run %q exceeds maximum total event bytes %d", runIdentity.RunID, MaxRunEventBytes)
	}
	runReportBytes, err := countRunReports(runDir)
	if err != nil {
		return nil, err
	}
	if runReportBytes > MaxRunReportBytes {
		return nil, fmt.Errorf("history run %q exceeds maximum total report bytes %d", runIdentity.RunID, MaxRunReportBytes)
	}
	attemptsDir := filepath.Join(runDir, "attempts")
	if err := requireDirectory(attemptsDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []AttemptSummary{}, nil
		}
		return nil, err
	}
	entries, err := os.ReadDir(attemptsDir)
	if err != nil {
		return nil, fmt.Errorf("read history attempts: %w", err)
	}
	if len(entries) > MaxAttemptsPerRun {
		return nil, fmt.Errorf("history run %q exceeds maximum attempt count %d", runIdentity.RunID, MaxAttemptsPerRun)
	}
	summaries := make([]AttemptSummary, 0, len(entries))
	totalEvents := 0
	for _, entry := range entries {
		if !entry.IsDir() || !validHashDirectory(entry.Name()) {
			return nil, fmt.Errorf("history run %q contains unexpected attempt entry %q", runIdentity.RunID, entry.Name())
		}
		attemptDir := filepath.Join(attemptsDir, entry.Name())
		identity, err := readJSONFile[AttemptIdentity](filepath.Join(attemptDir, "identity.json"), MaxIdentityBytes)
		if err != nil {
			return nil, fmt.Errorf("read history attempt identity: %w", err)
		}
		if err := identity.validate(); err != nil {
			return nil, fmt.Errorf("validate history attempt identity: %w", err)
		}
		if identity.RunID != runIdentity.RunID || identity.SpecDigest != runIdentity.SpecDigest {
			return nil, fmt.Errorf("history attempt %q does not match run identity", identity.AttemptID)
		}
		if filepath.Base(attemptDir) != attemptDirectoryName(identity.RunID, identity.AttemptID) {
			return nil, fmt.Errorf("history attempt %q is stored under the wrong directory", identity.AttemptID)
		}

		index, err := readJSONFile[attemptSummaryIndex](filepath.Join(attemptDir, "summary.json"), MaxIdentityBytes)
		if err == nil {
			if err := index.validate(identity); err != nil {
				return nil, fmt.Errorf("validate history attempt summary: %w", err)
			}
			eventCount, _, err := countEventFiles(filepath.Join(attemptDir, "events"))
			if err != nil {
				return nil, err
			}
			if eventCount != index.Summary.EventCount {
				return nil, fmt.Errorf(
					"history attempt %q summary event_count %d does not match stored event count %d",
					identity.AttemptID,
					index.Summary.EventCount,
					eventCount,
				)
			}
			_, reportAvailable, err := privateRegularFileSize(filepath.Join(attemptDir, "report.json"), MaxReportBytes)
			if err != nil {
				return nil, fmt.Errorf("inspect history report for attempt %q: %w", identity.AttemptID, err)
			}
			if reportAvailable != index.Summary.ReportAvailable {
				return nil, fmt.Errorf(
					"history attempt %q summary report availability does not match stored report",
					identity.AttemptID,
				)
			}
			totalEvents += index.Summary.EventCount
			summaries = append(summaries, index.Summary)
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("read history attempt summary: %w", err)
		}

		attempt, err := readAttempt(attemptDir, runIdentity)
		if err != nil {
			return nil, err
		}
		totalEvents += len(attempt.Events)
		summaries = append(summaries, summarizeAttempt(
			RunRecord{RunID: runIdentity.RunID, SpecDigest: runIdentity.SpecDigest},
			attempt,
		))
	}
	if totalEvents > MaxEventsPerRun {
		return nil, fmt.Errorf("history run %q exceeds maximum total event count %d", runIdentity.RunID, MaxEventsPerRun)
	}
	if totalEvents != runEventCount {
		return nil, fmt.Errorf("history run %q summary event count does not match stored events", runIdentity.RunID)
	}
	return summaries, nil
}

func (s DirectoryStore) Show(ctx context.Context, runID string) (RunRecord, error) {
	if err := validateIdentityText("run_id", runID); err != nil {
		return RunRecord{}, err
	}
	var record RunRecord
	err := s.withReadLock(ctx, func(root string) error {
		runDir := filepath.Join(root, "runs", runDirectoryName(runID))
		var err error
		record, err = readRun(runDir)
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("history run %q was not found", runID)
		}
		return err
	})
	if err != nil {
		return RunRecord{}, err
	}
	if record.RunID != runID {
		return RunRecord{}, fmt.Errorf("history run directory identity does not match %q", runID)
	}
	return record, nil
}

func (s DirectoryStore) ShowAttempt(ctx context.Context, runID, attemptID string) (RunRecord, error) {
	if err := validateIdentityText("run_id", runID); err != nil {
		return RunRecord{}, err
	}
	if err := validateIdentityText("attempt_id", attemptID); err != nil {
		return RunRecord{}, err
	}
	var record RunRecord
	err := s.withReadLock(ctx, func(root string) error {
		runDir := filepath.Join(root, "runs", runDirectoryName(runID))
		header, identity, err := readRunHeader(runDir)
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("history run %q was not found", runID)
		}
		if err != nil {
			return err
		}
		if identity.RunID != runID {
			return fmt.Errorf("history run directory identity does not match %q", runID)
		}
		attemptsDir := filepath.Join(runDir, "attempts")
		if err := requireDirectory(attemptsDir); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("history attempt %q was not found for run %q", attemptID, runID)
			}
			return err
		}
		attemptDir := filepath.Join(attemptsDir, attemptDirectoryName(runID, attemptID))
		attempt, err := readAttempt(attemptDir, identity)
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("history attempt %q was not found for run %q", attemptID, runID)
		}
		if err != nil {
			return err
		}
		if attempt.AttemptID != attemptID {
			return fmt.Errorf("history attempt directory identity does not match %q", attemptID)
		}
		if attempt.Report != nil && header.Spec == nil {
			return fmt.Errorf("history run %q has a report without an immutable spec", runID)
		}
		header.Attempts = []AttemptRecord{attempt}
		record = header
		return nil
	})
	if err != nil {
		return RunRecord{}, err
	}
	return record, nil
}

func readRun(runDir string) (RunRecord, error) {
	record, identity, err := readRunHeader(runDir)
	if err != nil {
		return RunRecord{}, err
	}
	eventCount, eventBytes, err := countRunEvents(runDir)
	if err != nil {
		return RunRecord{}, err
	}
	if eventCount > MaxEventsPerRun {
		return RunRecord{}, fmt.Errorf("history run %q exceeds maximum total event count %d", identity.RunID, MaxEventsPerRun)
	}
	if eventBytes > MaxRunEventBytes {
		return RunRecord{}, fmt.Errorf("history run %q exceeds maximum total event bytes %d", identity.RunID, MaxRunEventBytes)
	}
	reportBytes, err := countRunReports(runDir)
	if err != nil {
		return RunRecord{}, err
	}
	if reportBytes > MaxRunReportBytes {
		return RunRecord{}, fmt.Errorf("history run %q exceeds maximum total report bytes %d", identity.RunID, MaxRunReportBytes)
	}
	attemptsDir := filepath.Join(runDir, "attempts")
	if err := requireDirectory(attemptsDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return record, nil
		}
		return RunRecord{}, err
	}
	entries, err := os.ReadDir(attemptsDir)
	if err != nil {
		return RunRecord{}, fmt.Errorf("read history attempts: %w", err)
	}
	if len(entries) > MaxAttemptsPerRun {
		return RunRecord{}, fmt.Errorf("history run %q exceeds maximum attempt count %d", identity.RunID, MaxAttemptsPerRun)
	}
	totalEvents := 0
	for _, entry := range entries {
		if !entry.IsDir() || !validHashDirectory(entry.Name()) {
			return RunRecord{}, fmt.Errorf("history run %q contains unexpected attempt entry %q", identity.RunID, entry.Name())
		}
		attempt, err := readAttempt(filepath.Join(attemptsDir, entry.Name()), identity)
		if err != nil {
			return RunRecord{}, err
		}
		if attempt.Report != nil && record.Spec == nil {
			return RunRecord{}, fmt.Errorf("history run %q has a report without an immutable spec", identity.RunID)
		}
		record.Attempts = append(record.Attempts, attempt)
		totalEvents += len(attempt.Events)
		if totalEvents > MaxEventsPerRun {
			return RunRecord{}, fmt.Errorf("history run %q exceeds maximum total event count %d", identity.RunID, MaxEventsPerRun)
		}
	}
	sort.Slice(record.Attempts, func(i, j int) bool {
		left := attemptStartedAt(record.Attempts[i])
		right := attemptStartedAt(record.Attempts[j])
		if !left.Equal(right) {
			return left.Before(right)
		}
		return record.Attempts[i].AttemptID < record.Attempts[j].AttemptID
	})
	return record, nil
}

func readRunHeader(runDir string) (RunRecord, RunIdentity, error) {
	if err := requireDirectory(runDir); err != nil {
		return RunRecord{}, RunIdentity{}, err
	}
	identity, err := readJSONFile[RunIdentity](filepath.Join(runDir, "identity.json"), MaxIdentityBytes)
	if err != nil {
		return RunRecord{}, RunIdentity{}, fmt.Errorf("read history run identity: %w", err)
	}
	if err := identity.validate(); err != nil {
		return RunRecord{}, RunIdentity{}, fmt.Errorf("validate history run identity: %w", err)
	}
	if filepath.Base(runDir) != runDirectoryName(identity.RunID) {
		return RunRecord{}, RunIdentity{}, fmt.Errorf("history run %q is stored under the wrong directory", identity.RunID)
	}
	record := RunRecord{
		SchemaVersion: CurrentViewSchemaVersion,
		RunID:         identity.RunID,
		SpecDigest:    identity.SpecDigest,
		Attempts:      []AttemptRecord{},
	}
	specPath := filepath.Join(runDir, "spec.json")
	specPayload, err := readRegularFile(specPath, MaxSpecBytes)
	if err == nil {
		spec, parseErr := runspec.Parse(bytes.TrimSpace(specPayload))
		if parseErr != nil {
			return RunRecord{}, RunIdentity{}, fmt.Errorf("parse history run spec: %w", parseErr)
		}
		if spec.Digest() != identity.SpecDigest {
			return RunRecord{}, RunIdentity{}, fmt.Errorf("history run spec digest does not match run identity")
		}
		document := spec.Document()
		record.Spec = &document
	} else if !errors.Is(err, os.ErrNotExist) {
		return RunRecord{}, RunIdentity{}, fmt.Errorf("read history run spec: %w", err)
	}
	return record, identity, nil
}

func readAttempt(attemptDir string, runIdentity RunIdentity) (AttemptRecord, error) {
	if err := requireDirectory(attemptDir); err != nil {
		return AttemptRecord{}, err
	}
	identity, err := readJSONFile[AttemptIdentity](filepath.Join(attemptDir, "identity.json"), MaxIdentityBytes)
	if err != nil {
		return AttemptRecord{}, fmt.Errorf("read history attempt identity: %w", err)
	}
	if err := identity.validate(); err != nil {
		return AttemptRecord{}, fmt.Errorf("validate history attempt identity: %w", err)
	}
	if identity.RunID != runIdentity.RunID || identity.SpecDigest != runIdentity.SpecDigest {
		return AttemptRecord{}, fmt.Errorf("history attempt %q does not match run identity", identity.AttemptID)
	}
	if filepath.Base(attemptDir) != attemptDirectoryName(identity.RunID, identity.AttemptID) {
		return AttemptRecord{}, fmt.Errorf("history attempt %q is stored under the wrong directory", identity.AttemptID)
	}
	attempt := AttemptRecord{
		AttemptID: identity.AttemptID,
		Events:    []model.RunEvent{},
	}
	attempt.Events, err = readEvents(attemptDir, identity)
	if err != nil {
		return AttemptRecord{}, err
	}
	reportPath := filepath.Join(attemptDir, "report.json")
	result, err := readReport(reportPath)
	if err == nil {
		if result.ID != identity.RunID || result.AttemptID != identity.AttemptID || result.SpecDigest != identity.SpecDigest {
			return AttemptRecord{}, fmt.Errorf("history report identity does not match attempt %q", identity.AttemptID)
		}
		if len(attempt.Events) > 0 {
			last := attempt.Events[len(attempt.Events)-1]
			if last.Type != model.RunEventFinished {
				return AttemptRecord{}, fmt.Errorf("history report has no terminal event for attempt %q", identity.AttemptID)
			}
			if last.Status != result.Status {
				return AttemptRecord{}, fmt.Errorf("history report status does not match terminal event for attempt %q", identity.AttemptID)
			}
		}
		attempt.Report = &result
	} else if !errors.Is(err, os.ErrNotExist) {
		return AttemptRecord{}, fmt.Errorf("read history report for attempt %q: %w", identity.AttemptID, err)
	}
	index, err := readJSONFile[attemptSummaryIndex](filepath.Join(attemptDir, "summary.json"), MaxIdentityBytes)
	if err == nil {
		if err := index.validate(identity); err != nil {
			return AttemptRecord{}, fmt.Errorf("validate history attempt summary: %w", err)
		}
		want := summarizeAttempt(
			RunRecord{RunID: runIdentity.RunID, SpecDigest: runIdentity.SpecDigest},
			attempt,
		)
		if !reflect.DeepEqual(index.Summary, want) {
			return AttemptRecord{}, fmt.Errorf("history attempt %q summary does not match stored events and report", identity.AttemptID)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return AttemptRecord{}, fmt.Errorf("read history attempt summary: %w", err)
	}
	return attempt, nil
}

func readEvents(attemptDir string, identity AttemptIdentity) ([]model.RunEvent, error) {
	eventsDir := filepath.Join(attemptDir, "events")
	eventCount, eventBytes, err := countEventFiles(eventsDir)
	if err != nil {
		return nil, err
	}
	if eventCount > MaxEventsPerAttempt {
		return nil, fmt.Errorf("history attempt %q exceeds maximum event count %d", identity.AttemptID, MaxEventsPerAttempt)
	}
	if eventBytes > MaxAttemptEventBytes {
		return nil, fmt.Errorf("history attempt %q exceeds maximum event bytes %d", identity.AttemptID, MaxAttemptEventBytes)
	}
	if err := requireDirectory(eventsDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []model.RunEvent{}, nil
		}
		return nil, err
	}
	entries, err := os.ReadDir(eventsDir)
	if err != nil {
		return nil, fmt.Errorf("read history events: %w", err)
	}
	if len(entries) > MaxEventsPerAttempt {
		return nil, fmt.Errorf("history attempt %q exceeds maximum event count %d", identity.AttemptID, MaxEventsPerAttempt)
	}
	sequences := make([]uint64, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			return nil, fmt.Errorf("history events contains unexpected directory %q", entry.Name())
		}
		sequence, ok := parseEventFileName(entry.Name())
		if !ok {
			return nil, fmt.Errorf("history events contains unexpected entry %q", entry.Name())
		}
		sequences = append(sequences, sequence)
	}
	sort.Slice(sequences, func(i, j int) bool { return sequences[i] < sequences[j] })
	events := make([]model.RunEvent, 0, len(sequences))
	var previous model.RunEvent
	for index, sequence := range sequences {
		want := uint64(index + 1)
		if sequence != want {
			return nil, fmt.Errorf("history attempt %q event sequence has gap before %d", identity.AttemptID, sequence)
		}
		event, found, err := readEventIfExists(filepath.Join(eventsDir, eventFileName(sequence)))
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, fmt.Errorf("history event %d disappeared during read", sequence)
		}
		if event.RunID != identity.RunID || event.AttemptID != identity.AttemptID || event.SpecDigest != identity.SpecDigest {
			return nil, fmt.Errorf("history event %d identity does not match attempt %q", sequence, identity.AttemptID)
		}
		if index == 0 && event.Type != model.RunEventStarted {
			return nil, fmt.Errorf("history attempt %q sequence 1 is not run_started", identity.AttemptID)
		}
		if index > 0 {
			if event.Type == model.RunEventStarted {
				return nil, fmt.Errorf("history attempt %q contains duplicate run_started event", identity.AttemptID)
			}
			if previous.Type == model.RunEventFinished {
				return nil, fmt.Errorf("history attempt %q contains event after terminal event", identity.AttemptID)
			}
			if event.OccurredAt.Before(previous.OccurredAt) {
				return nil, fmt.Errorf("history attempt %q event time moves backwards", identity.AttemptID)
			}
		}
		events = append(events, event)
		previous = event
	}
	return events, nil
}

func readReport(path string) (model.DrillResult, error) {
	payload, err := readRegularFile(path, MaxReportBytes)
	if err != nil {
		return model.DrillResult{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var result model.DrillResult
	if err := decoder.Decode(&result); err != nil {
		return model.DrillResult{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return model.DrillResult{}, fmt.Errorf("multiple JSON values")
		}
		return model.DrillResult{}, fmt.Errorf("decode trailing data: %w", err)
	}
	if err := report.Validate(result); err != nil {
		return model.DrillResult{}, err
	}
	return result, nil
}

func summarizeAttempt(run RunRecord, attempt AttemptRecord) AttemptSummary {
	summary := AttemptSummary{
		RunID:      run.RunID,
		AttemptID:  attempt.AttemptID,
		SpecDigest: run.SpecDigest,
		Status:     model.DrillStatusUnknown,
		EventCount: len(attempt.Events),
	}
	if len(attempt.Events) > 0 {
		summary.StartedAt = attempt.Events[0].OccurredAt
		last := attempt.Events[len(attempt.Events)-1]
		if last.Type == model.RunEventFinished {
			summary.Status = last.Status
			summary.FinishedAt = last.OccurredAt
		}
	}
	if attempt.Report != nil {
		result := attempt.Report
		summary.ReportAvailable = true
		summary.Status = result.Status
		summary.StartedAt = result.StartedAt
		summary.FinishedAt = result.FinishedAt
		summary.ArtifactCount = len(result.Artifacts)
		summary.EvidenceCount = len(result.Evidence)
		if result.Failure != nil {
			summary.FailureStage = result.Failure.Stage
		}
		if result.PolicyEvaluation != nil {
			summary.BlockingPolicy = len(result.PolicyEvaluation.BlockingVerdicts())
		}
	}
	return summary
}

func attemptStartedAt(attempt AttemptRecord) time.Time {
	if attempt.Report != nil {
		return attempt.Report.StartedAt
	}
	if len(attempt.Events) > 0 {
		return attempt.Events[0].OccurredAt
	}
	return time.Time{}
}

func parseEventFileName(name string) (uint64, bool) {
	if len(name) != 25 || !strings.HasSuffix(name, ".json") {
		return 0, false
	}
	sequence, err := strconv.ParseUint(strings.TrimSuffix(name, ".json"), 10, 64)
	return sequence, err == nil && sequence > 0
}

func validHashDirectory(name string) bool {
	if len(name) != 64 {
		return false
	}
	for _, char := range name {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}
