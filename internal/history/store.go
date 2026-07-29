package history

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/r314tive/pgdrill/internal/durablefs"
	"github.com/r314tive/pgdrill/internal/filelock"
	"github.com/r314tive/pgdrill/internal/jsonutil"
	"github.com/r314tive/pgdrill/internal/model"
	"github.com/r314tive/pgdrill/internal/report"
	"github.com/r314tive/pgdrill/internal/runspec"
)

var ErrStoreNotFound = errors.New("history store not found")

type DirectoryStore struct {
	Path          string
	retentionHook func(step string, index int) error
	migrationHook func(step string, index int) error
}

// WriteEvent durably appends one lifecycle event. Exact retries are
// idempotent; sequence reuse with different content is rejected.
func (s DirectoryStore) WriteEvent(ctx context.Context, event model.RunEvent) error {
	if err := event.Validate(); err != nil {
		return fmt.Errorf("validate history event: %w", err)
	}
	if event.SchemaVersion != model.CurrentRunEventSchemaVersion {
		return fmt.Errorf(
			"history event writer requires schema_version %q",
			model.CurrentRunEventSchemaVersion,
		)
	}
	if !model.IsSHA256Digest(event.SpecDigest) {
		return fmt.Errorf("history event spec_digest is required")
	}
	if event.Sequence > MaxEventsPerAttempt {
		return fmt.Errorf("history event sequence exceeds maximum event count %d", MaxEventsPerAttempt)
	}
	payload, err := marshalJSON(event, MaxEventBytes)
	if err != nil {
		return fmt.Errorf("encode history event: %w", err)
	}
	return s.withWriteLock(ctx, func(root string) error {
		if event.Sequence > 1 {
			if err := requireExistingAttempt(root, event.RunID, event.AttemptID, event.Sequence); err != nil {
				return err
			}
		}
		if err := validateIdentityCapacity(root, event.RunID, event.AttemptID); err != nil {
			return err
		}
		runIdentity := RunIdentity{
			SchemaVersion: CurrentRunSchemaVersion,
			RunID:         event.RunID,
			SpecDigest:    event.SpecDigest,
		}
		runDir, err := ensureRun(ctx, root, runIdentity)
		if err != nil {
			return err
		}
		attemptIdentity := AttemptIdentity{
			SchemaVersion: CurrentAttemptSchemaVersion,
			RunID:         event.RunID,
			AttemptID:     event.AttemptID,
			SpecDigest:    event.SpecDigest,
		}
		attemptDir, err := ensureAttempt(ctx, root, runDir, attemptIdentity)
		if err != nil {
			return err
		}
		eventsDir := filepath.Join(attemptDir, "events")
		if err := ensureDirectory(eventsDir); err != nil {
			return fmt.Errorf("create history events directory: %w", err)
		}
		if err := cleanupHistoryTemporaryFiles(eventsDir); err != nil {
			return fmt.Errorf("recover history event write: %w", err)
		}
		exists, err := validateEventPosition(eventsDir, event)
		if err != nil {
			return err
		}
		if exists {
			return nil
		}
		attemptEventCount, attemptEventBytes, err := countEventFiles(eventsDir)
		if err != nil {
			return err
		}
		if attemptEventCount >= MaxEventsPerAttempt {
			return fmt.Errorf("history attempt %q exceeds maximum event count %d", event.AttemptID, MaxEventsPerAttempt)
		}
		if attemptEventBytes+int64(len(payload)) > MaxAttemptEventBytes {
			return fmt.Errorf("history attempt %q exceeds maximum event bytes %d", event.AttemptID, MaxAttemptEventBytes)
		}
		runEventCount, runEventBytes, err := countRunEvents(runDir)
		if err != nil {
			return err
		}
		if runEventCount >= MaxEventsPerRun {
			return fmt.Errorf("history run %q exceeds maximum total event count %d", event.RunID, MaxEventsPerRun)
		}
		if runEventBytes+int64(len(payload)) > MaxRunEventBytes {
			return fmt.Errorf("history run %q exceeds maximum total event bytes %d", event.RunID, MaxRunEventBytes)
		}
		path := filepath.Join(eventsDir, eventFileName(event.Sequence))
		if err := writeImmutable(ctx, eventsDir, path, payload, MaxEventBytes); err != nil {
			return fmt.Errorf("persist history event %d: %w", event.Sequence, err)
		}
		return nil
	})
}

// SaveReport persists the terminal report and its canonical immutable spec.
// The report is not used as the event journal; either side can remain useful
// when a process terminates between the two writes.
func (s DirectoryStore) SaveReport(ctx context.Context, result model.DrillResult) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := report.ValidateReaderContract(result); err != nil {
		return fmt.Errorf("validate history report: %w", err)
	}
	if err := validateIdentityText("attempt_id", result.AttemptID); err != nil {
		return fmt.Errorf("history report: %w", err)
	}
	if !model.IsSHA256Digest(result.SpecDigest) {
		return fmt.Errorf("history report spec_digest is required")
	}
	if result.Spec == nil {
		return fmt.Errorf("history report spec is required")
	}
	spec, err := runspec.New(*result.Spec)
	if err != nil {
		return fmt.Errorf("validate history report spec: %w", err)
	}
	if spec.Digest() != result.SpecDigest {
		return fmt.Errorf("history report spec_digest does not match spec")
	}

	var reportBuffer bytes.Buffer
	if err := report.WriteCompatibleJSON(&reportBuffer, result); err != nil {
		return fmt.Errorf("encode history report: %w", err)
	}
	if reportBuffer.Len() > MaxReportBytes {
		return fmt.Errorf("history report exceeds %d bytes", MaxReportBytes)
	}

	return s.withWriteLock(ctx, func(root string) error {
		if err := validateIdentityCapacity(root, result.ID, result.AttemptID); err != nil {
			return err
		}
		runIdentity := RunIdentity{
			SchemaVersion: CurrentRunSchemaVersion,
			RunID:         result.ID,
			SpecDigest:    result.SpecDigest,
		}
		runDir, err := ensureRun(ctx, root, runIdentity)
		if err != nil {
			return err
		}
		reportPath := filepath.Join(
			runDir,
			"attempts",
			attemptDirectoryName(result.ID, result.AttemptID),
			"report.json",
		)
		additionalReportBytes, err := immutableAdditionalBytes(reportPath, reportBuffer.Bytes(), MaxReportBytes)
		if err != nil {
			return fmt.Errorf("inspect history report: %w", err)
		}
		runReportBytes, err := countRunReports(runDir)
		if err != nil {
			return err
		}
		if runReportBytes+additionalReportBytes > MaxRunReportBytes {
			return fmt.Errorf("history run %q exceeds maximum total report bytes %d", result.ID, MaxRunReportBytes)
		}
		specPayload := append(spec.CanonicalJSON(), '\n')
		if err := writeImmutable(ctx, runDir, filepath.Join(runDir, "spec.json"), specPayload, MaxSpecBytes); err != nil {
			return fmt.Errorf("persist history run spec: %w", err)
		}
		attemptIdentity := AttemptIdentity{
			SchemaVersion: CurrentAttemptSchemaVersion,
			RunID:         result.ID,
			AttemptID:     result.AttemptID,
			SpecDigest:    result.SpecDigest,
		}
		attemptDir, err := ensureAttempt(ctx, root, runDir, attemptIdentity)
		if err != nil {
			return err
		}
		events, err := validateTerminalReport(attemptDir, result)
		if err != nil {
			return err
		}
		if err := writeImmutable(ctx, attemptDir, reportPath, reportBuffer.Bytes(), MaxReportBytes); err != nil {
			return fmt.Errorf("persist history report: %w", err)
		}
		summary := summarizeAttempt(
			RunRecord{RunID: result.ID, SpecDigest: result.SpecDigest},
			AttemptRecord{AttemptID: result.AttemptID, Events: events, Report: &result},
		)
		indexPayload, err := marshalJSON(attemptSummaryIndex{
			SchemaVersion: CurrentSummarySchemaVersion,
			Summary:       summary,
		}, MaxIdentityBytes)
		if err != nil {
			return fmt.Errorf("encode history attempt summary: %w", err)
		}
		if err := writeImmutable(ctx, attemptDir, filepath.Join(attemptDir, "summary.json"), indexPayload, MaxIdentityBytes); err != nil {
			return fmt.Errorf("persist history attempt summary: %w", err)
		}
		return nil
	})
}

func (s DirectoryStore) withWriteLock(ctx context.Context, operation func(string) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := s.root()
	if err != nil {
		return err
	}
	if err := ensureDirectory(root); err != nil {
		return fmt.Errorf("create history store: %w", err)
	}
	return withLock(ctx, root, filelock.Exclusive, true, func() error {
		if err := ensureMetadata(ctx, root); err != nil {
			return err
		}
		state, err := inspectRetentionState(root)
		if err != nil {
			return err
		}
		if err := requireCleanRetentionState(state); err != nil {
			return fmt.Errorf("history write requires clean retention state: %w", err)
		}
		return operation(root)
	})
}

func (s DirectoryStore) withReadLock(ctx context.Context, operation func(string) error) error {
	root, err := s.root()
	if err != nil {
		return err
	}
	if err := requireDirectory(root); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrStoreNotFound
		}
		return fmt.Errorf("inspect history store: %w", err)
	}
	return withLock(ctx, root, filelock.Shared, false, func() error {
		metadata, err := readJSONFile[StoreMetadata](filepath.Join(root, "store.json"), MaxIdentityBytes)
		if err != nil {
			return fmt.Errorf("read history store metadata: %w", err)
		}
		if err := metadata.validate(); err != nil {
			return err
		}
		return operation(root)
	})
}

func (s DirectoryStore) root() (string, error) {
	if strings.TrimSpace(s.Path) == "" {
		return "", fmt.Errorf("history store path is required")
	}
	return filepath.Clean(s.Path), nil
}

func ensureMetadata(ctx context.Context, root string) error {
	path := filepath.Join(root, "store.json")
	stored, err := readJSONFile[StoreMetadata](path, MaxIdentityBytes)
	if err == nil {
		if err := stored.validate(); err != nil {
			return err
		}
		if stored.SchemaVersion == LegacyStoreSchemaVersion {
			return fmt.Errorf(
				"history store schema_version %q is read-only; migrate it to %q before writing",
				stored.SchemaVersion,
				CurrentStoreSchemaVersion,
			)
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read history store metadata: %w", err)
	}
	metadata := StoreMetadata{
		SchemaVersion: CurrentStoreSchemaVersion,
		LayoutVersion: CurrentLayoutVersion,
	}
	payload, err := marshalJSON(metadata, MaxIdentityBytes)
	if err != nil {
		return err
	}
	if err := writeImmutable(ctx, root, path, payload, MaxIdentityBytes); err != nil {
		return fmt.Errorf("persist history store metadata: %w", err)
	}
	stored, err = readJSONFile[StoreMetadata](path, MaxIdentityBytes)
	if err != nil {
		return fmt.Errorf("read history store metadata: %w", err)
	}
	return stored.validate()
}

func ensureRun(ctx context.Context, root string, identity RunIdentity) (string, error) {
	if err := identity.validate(); err != nil {
		return "", err
	}
	runsDir := filepath.Join(root, "runs")
	if err := ensureDirectory(runsDir); err != nil {
		return "", fmt.Errorf("create history runs directory: %w", err)
	}
	runDir := filepath.Join(runsDir, runDirectoryName(identity.RunID))
	if err := ensureDirectory(runDir); err != nil {
		return "", fmt.Errorf("create history run directory: %w", err)
	}
	payload, err := marshalJSON(identity, MaxIdentityBytes)
	if err != nil {
		return "", err
	}
	path := filepath.Join(runDir, "identity.json")
	if err := writeImmutable(ctx, runDir, path, payload, MaxIdentityBytes); err != nil {
		return "", fmt.Errorf("persist history run identity: %w", err)
	}
	stored, err := readJSONFile[RunIdentity](path, MaxIdentityBytes)
	if err != nil {
		return "", fmt.Errorf("read history run identity: %w", err)
	}
	if !reflect.DeepEqual(stored, identity) {
		return "", fmt.Errorf("run_id %q is already bound to another immutable identity", identity.RunID)
	}
	return runDir, nil
}

func ensureAttempt(ctx context.Context, root, runDir string, identity AttemptIdentity) (string, error) {
	if err := identity.validate(); err != nil {
		return "", err
	}
	if err := validateIdentityCapacity(root, identity.RunID, identity.AttemptID); err != nil {
		return "", err
	}
	attemptsDir := filepath.Join(runDir, "attempts")
	if err := ensureDirectory(attemptsDir); err != nil {
		return "", fmt.Errorf("create history attempts directory: %w", err)
	}
	attemptDir := filepath.Join(attemptsDir, attemptDirectoryName(identity.RunID, identity.AttemptID))
	if err := ensureDirectory(attemptDir); err != nil {
		return "", fmt.Errorf("create history attempt directory: %w", err)
	}
	payload, err := marshalJSON(identity, MaxIdentityBytes)
	if err != nil {
		return "", err
	}
	path := filepath.Join(attemptDir, "identity.json")
	if err := writeImmutable(ctx, attemptDir, path, payload, MaxIdentityBytes); err != nil {
		return "", fmt.Errorf("persist history attempt identity: %w", err)
	}
	stored, err := readJSONFile[AttemptIdentity](path, MaxIdentityBytes)
	if err != nil {
		return "", fmt.Errorf("read history attempt identity: %w", err)
	}
	if !reflect.DeepEqual(stored, identity) {
		return "", fmt.Errorf("attempt_id %q is already bound to another immutable identity", identity.AttemptID)
	}
	return attemptDir, nil
}

func validateIdentityCapacity(root, runID, attemptID string) error {
	runsDir := filepath.Join(root, "runs")
	runDir := filepath.Join(runsDir, runDirectoryName(runID))
	runExists, err := privateDirectoryExists(runDir)
	if err != nil {
		return fmt.Errorf("inspect history run capacity: %w", err)
	}
	if !runExists {
		count, err := countHashDirectories(runsDir, "history runs", MaxRuns)
		if err != nil {
			return err
		}
		if count >= MaxRuns {
			return fmt.Errorf("history store exceeds maximum run count %d", MaxRuns)
		}
	}

	attemptDir := filepath.Join(runDir, "attempts", attemptDirectoryName(runID, attemptID))
	attemptExists, err := privateDirectoryExists(attemptDir)
	if err != nil {
		return fmt.Errorf("inspect history attempt capacity: %w", err)
	}
	if attemptExists {
		return nil
	}
	if runExists {
		count, err := countHashDirectories(
			filepath.Join(runDir, "attempts"),
			"history attempts",
			MaxAttemptsPerRun,
		)
		if err != nil {
			return err
		}
		if count >= MaxAttemptsPerRun {
			return fmt.Errorf("history run %q exceeds maximum attempt count %d", runID, MaxAttemptsPerRun)
		}
	}
	total, err := countTotalAttempts(root)
	if err != nil {
		return err
	}
	if total >= MaxTotalAttempts {
		return fmt.Errorf("history store exceeds maximum total attempt count %d", MaxTotalAttempts)
	}
	return nil
}

func requireExistingAttempt(root, runID, attemptID string, sequence uint64) error {
	paths := []string{
		filepath.Join(root, "runs", runDirectoryName(runID)),
		filepath.Join(root, "runs", runDirectoryName(runID), "attempts", attemptDirectoryName(runID, attemptID)),
		filepath.Join(root, "runs", runDirectoryName(runID), "attempts", attemptDirectoryName(runID, attemptID), "events"),
	}
	for _, path := range paths {
		if err := requireDirectory(path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("history event sequence %d cannot be appended before sequence %d", sequence, sequence-1)
			}
			return err
		}
	}
	return nil
}

func privateDirectoryExists(path string) (bool, error) {
	err := requireDirectory(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func countHashDirectories(path, description string, limit int) (int, error) {
	if err := requireDirectory(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	entries, err := durablefs.ReadDirBounded(path, limit)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", description, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() || !validHashDirectory(entry.Name()) {
			return 0, fmt.Errorf("%s contains unexpected entry %q", description, entry.Name())
		}
		if err := requireDirectory(filepath.Join(path, entry.Name())); err != nil {
			return 0, err
		}
	}
	return len(entries), nil
}

func countTotalAttempts(root string) (int, error) {
	runsDir := filepath.Join(root, "runs")
	if _, err := countHashDirectories(runsDir, "history runs", MaxRuns); err != nil {
		return 0, err
	}
	entries, err := durablefs.ReadDirBounded(runsDir, MaxRuns)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read history runs: %w", err)
	}
	total := 0
	for _, entry := range entries {
		runDir := filepath.Join(runsDir, entry.Name())
		count, err := countHashDirectories(
			filepath.Join(runDir, "attempts"),
			"history attempts",
			MaxAttemptsPerRun,
		)
		if err != nil {
			return 0, err
		}
		total += count
		if total > MaxTotalAttempts {
			return total, nil
		}
	}
	return total, nil
}

func countRunEvents(runDir string) (int, int64, error) {
	attemptsDir := filepath.Join(runDir, "attempts")
	if _, err := countHashDirectories(
		attemptsDir,
		"history attempts",
		MaxAttemptsPerRun,
	); err != nil {
		return 0, 0, err
	}
	entries, err := durablefs.ReadDirBounded(attemptsDir, MaxAttemptsPerRun)
	if errors.Is(err, os.ErrNotExist) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, fmt.Errorf("read history attempts: %w", err)
	}
	totalCount := 0
	var totalBytes int64
	for _, entry := range entries {
		eventsDir := filepath.Join(attemptsDir, entry.Name(), "events")
		count, size, err := countEventFiles(eventsDir)
		if err != nil {
			return 0, 0, err
		}
		totalCount += count
		totalBytes += size
		if totalCount > MaxEventsPerRun || totalBytes > MaxRunEventBytes {
			return totalCount, totalBytes, nil
		}
	}
	return totalCount, totalBytes, nil
}

func countEventFiles(eventsDir string) (int, int64, error) {
	if err := requireDirectory(eventsDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	entries, err := durablefs.ReadDirBounded(
		eventsDir,
		MaxEventsPerAttempt+maxHistoryTemporaryFilesPerDirectory,
	)
	if err != nil {
		return 0, 0, fmt.Errorf("read history events: %w", err)
	}
	var totalBytes int64
	eventCount := 0
	temporaryCount := 0
	for _, entry := range entries {
		path := filepath.Join(eventsDir, entry.Name())
		if isHistoryTemporaryFileName(entry.Name()) {
			temporaryCount++
			if temporaryCount > maxHistoryTemporaryFilesPerDirectory {
				return 0, 0, fmt.Errorf(
					"history events exceeds maximum temporary file count %d",
					maxHistoryTemporaryFilesPerDirectory,
				)
			}
			if err := validateHistoryTemporaryFile(path); err != nil {
				return 0, 0, err
			}
			continue
		}
		if _, ok := parseEventFileName(entry.Name()); !ok {
			return 0, 0, fmt.Errorf("history events contains unexpected entry %q", entry.Name())
		}
		info, err := os.Lstat(path)
		if err != nil {
			return 0, 0, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return 0, 0, fmt.Errorf("%s is not a regular non-symbolic-link file", path)
		}
		if info.Mode().Perm()&0o077 != 0 {
			return 0, 0, fmt.Errorf("%s permissions %o are not private", path, info.Mode().Perm())
		}
		if info.Size() > MaxEventBytes {
			return 0, 0, fmt.Errorf("%s exceeds %d bytes", path, MaxEventBytes)
		}
		totalBytes += info.Size()
		eventCount++
	}
	return eventCount, totalBytes, nil
}

func countRunReports(runDir string) (int64, error) {
	attemptsDir := filepath.Join(runDir, "attempts")
	if _, err := countHashDirectories(
		attemptsDir,
		"history attempts",
		MaxAttemptsPerRun,
	); err != nil {
		return 0, err
	}
	entries, err := durablefs.ReadDirBounded(attemptsDir, MaxAttemptsPerRun)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read history attempts: %w", err)
	}
	var total int64
	for _, entry := range entries {
		path := filepath.Join(attemptsDir, entry.Name(), "report.json")
		size, found, err := privateRegularFileSize(path, MaxReportBytes)
		if err != nil {
			return 0, err
		}
		if !found {
			continue
		}
		total += size
		if total > MaxRunReportBytes {
			return total, nil
		}
	}
	return total, nil
}

func privateRegularFileSize(path string, maxBytes int) (int64, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return 0, false, fmt.Errorf("%s is not a regular non-symbolic-link file", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return 0, false, fmt.Errorf("%s permissions %o are not private", path, info.Mode().Perm())
	}
	if info.Size() > int64(maxBytes) {
		return 0, false, fmt.Errorf("%s exceeds %d bytes", path, maxBytes)
	}
	return info.Size(), true, nil
}

func immutableAdditionalBytes(path string, payload []byte, maxBytes int) (int64, error) {
	existing, err := readRegularFile(path, maxBytes)
	if errors.Is(err, os.ErrNotExist) {
		return int64(len(payload)), nil
	}
	if err != nil {
		return 0, err
	}
	if !bytes.Equal(existing, payload) {
		return 0, fmt.Errorf("immutable file %s already contains different content", path)
	}
	return 0, nil
}

func validateEventPosition(eventsDir string, event model.RunEvent) (bool, error) {
	path := filepath.Join(eventsDir, eventFileName(event.Sequence))
	existing, found, err := readEventIfExists(path)
	if err != nil {
		return false, err
	}
	if found {
		if reflect.DeepEqual(existing, event) {
			return true, nil
		}
		return false, fmt.Errorf("history event sequence %d is immutable", event.Sequence)
	}
	reportPath := filepath.Join(filepath.Dir(eventsDir), "report.json")
	if info, err := os.Lstat(reportPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return false, fmt.Errorf("history terminal report is not a regular non-symbolic-link file")
		}
		return false, fmt.Errorf("history attempt already has a terminal report")
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("inspect history terminal report: %w", err)
	}
	if event.Sequence == 1 {
		if event.Type != model.RunEventStarted {
			return false, fmt.Errorf("history event sequence 1 must be %q", model.RunEventStarted)
		}
		return false, nil
	}
	if event.Type == model.RunEventStarted {
		return false, fmt.Errorf("history run_started event must use sequence 1")
	}
	previousPath := filepath.Join(eventsDir, eventFileName(event.Sequence-1))
	previous, found, err := readEventIfExists(previousPath)
	if err != nil {
		return false, err
	}
	if !found {
		return false, fmt.Errorf("history event sequence %d cannot be appended before sequence %d", event.Sequence, event.Sequence-1)
	}
	if previous.RunID != event.RunID || previous.AttemptID != event.AttemptID || previous.SpecDigest != event.SpecDigest {
		return false, fmt.Errorf("history event sequence %d identity does not match previous event", event.Sequence)
	}
	if previous.Type == model.RunEventFinished {
		return false, fmt.Errorf("history attempt is already terminal at sequence %d", previous.Sequence)
	}
	if event.OccurredAt.Before(previous.OccurredAt) {
		return false, fmt.Errorf("history event occurred_at must not move backwards")
	}
	return false, nil
}

func validateTerminalReport(attemptDir string, result model.DrillResult) ([]model.RunEvent, error) {
	events, err := readEvents(attemptDir, AttemptIdentity{
		SchemaVersion: CurrentAttemptSchemaVersion,
		RunID:         result.ID,
		AttemptID:     result.AttemptID,
		SpecDigest:    result.SpecDigest,
	})
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return events, nil
	}
	last := events[len(events)-1]
	if last.Type != model.RunEventFinished {
		return nil, fmt.Errorf("history terminal report requires a run_finished event")
	}
	if last.Status != result.Status {
		return nil, fmt.Errorf("history report status %q does not match terminal event status %q", result.Status, last.Status)
	}
	return events, nil
}

func readEventIfExists(path string) (model.RunEvent, bool, error) {
	event, err := readJSONFile[model.RunEvent](path, MaxEventBytes)
	if errors.Is(err, os.ErrNotExist) {
		return model.RunEvent{}, false, nil
	}
	if err != nil {
		return model.RunEvent{}, false, fmt.Errorf("read history event %s: %w", filepath.Base(path), err)
	}
	if err := event.Validate(); err != nil {
		return model.RunEvent{}, false, fmt.Errorf("validate history event %s: %w", filepath.Base(path), err)
	}
	return event, true, nil
}

func marshalJSON(value any, maxBytes int) ([]byte, error) {
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	payload = append(payload, '\n')
	if len(payload) > maxBytes {
		return nil, fmt.Errorf("JSON document exceeds %d bytes", maxBytes)
	}
	return payload, nil
}

func writeImmutable(ctx context.Context, dir, path string, payload []byte, maxBytes int) (resultErr error) {
	if len(payload) > maxBytes {
		return fmt.Errorf("document exceeds %d bytes", maxBytes)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := cleanupHistoryTemporaryFiles(dir); err != nil {
		return err
	}
	existing, err := readRegularFile(path, maxBytes)
	if err == nil {
		if bytes.Equal(existing, payload) {
			return nil
		}
		return fmt.Errorf("immutable file %s already contains different content", path)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	file, err := os.CreateTemp(dir, ".history-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary history file: %w", err)
	}
	tmpPath := file.Name()
	defer func() {
		removeErr := os.Remove(tmpPath)
		if errors.Is(removeErr, os.ErrNotExist) {
			return
		}
		if removeErr != nil {
			resultErr = errors.Join(
				resultErr,
				fmt.Errorf("remove temporary history file: %w", removeErr),
			)
			return
		}
		if syncErr := syncDirectory(dir); syncErr != nil {
			resultErr = errors.Join(
				resultErr,
				fmt.Errorf("sync history directory after temporary cleanup: %w", syncErr),
			)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("chmod temporary history file: %w", err)
	}
	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		return fmt.Errorf("write temporary history file: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync temporary history file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temporary history file: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Link(tmpPath, path); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("publish immutable history file %s: %w", path, err)
		}
		existing, readErr := readRegularFile(path, maxBytes)
		if readErr != nil {
			return readErr
		}
		if !bytes.Equal(existing, payload) {
			return fmt.Errorf("immutable file %s already contains different content", path)
		}
		return nil
	}
	return syncDirectory(dir)
}

func cleanupHistoryTemporaryFiles(dir string) error {
	entries, err := durablefs.ReadDirBounded(
		dir,
		MaxEventsPerRun+maxHistoryTemporaryFilesPerDirectory,
	)
	if err != nil {
		return fmt.Errorf("read history directory for temporary recovery: %w", err)
	}
	paths := make([]string, 0)
	for _, entry := range entries {
		if !isHistoryTemporaryFileName(entry.Name()) {
			continue
		}
		if len(paths) >= maxHistoryTemporaryFilesPerDirectory {
			return fmt.Errorf(
				"history directory %s exceeds maximum temporary file count %d",
				dir,
				maxHistoryTemporaryFilesPerDirectory,
			)
		}
		path := filepath.Join(dir, entry.Name())
		if err := validateHistoryTemporaryFile(path); err != nil {
			return err
		}
		paths = append(paths, path)
	}
	if len(paths) == 0 {
		return nil
	}
	var removeErr error
	removed := false
	for _, path := range paths {
		if err := os.Remove(path); err != nil {
			removeErr = fmt.Errorf("remove temporary history file %s: %w", path, err)
			break
		}
		removed = true
	}
	var syncErr error
	if removed {
		if err := syncDirectory(dir); err != nil {
			syncErr = fmt.Errorf("sync history directory after temporary recovery: %w", err)
		}
	}
	return errors.Join(removeErr, syncErr)
}

func validateHistoryTemporaryFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect temporary history file %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("temporary history file is not a regular non-symbolic-link file: %s", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf(
			"temporary history file permissions %o are not private: %s",
			info.Mode().Perm(),
			path,
		)
	}
	if info.Size() > MaxReportBytes {
		return fmt.Errorf("temporary history file %s exceeds %d bytes", path, MaxReportBytes)
	}
	return nil
}

func isHistoryTemporaryFileName(name string) bool {
	const (
		prefix = ".history-"
		suffix = ".tmp"
	)
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return false
	}
	random := strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix)
	if random == "" {
		return false
	}
	for _, character := range random {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func withLock(ctx context.Context, root string, mode filelock.Mode, create bool, operation func() error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	lockPath := filepath.Join(root, ".lock")
	flags := os.O_RDONLY
	if create {
		flags = os.O_CREATE | os.O_RDWR
	}
	lock, err := filelock.OpenPrivate(lockPath, flags)
	if err != nil {
		return fmt.Errorf("open history store lock: %w", err)
	}
	defer lock.Close()
	if err := filelock.Lock(ctx, lock, mode); err != nil {
		return fmt.Errorf("lock history store: %w", err)
	}
	defer filelock.Unlock(lock) //nolint:errcheck
	return operation()
}

func ensureDirectory(path string) error {
	if err := durablefs.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return requireDirectory(path)
}

func requireDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%s is not a real directory", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s permissions %o are not private", path, info.Mode().Perm())
	}
	return nil
}

func readRegularFile(path string, maxBytes int) ([]byte, error) {
	file, err := filelock.OpenPrivate(path, os.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > int64(maxBytes) {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, maxBytes)
	}
	payload, err := io.ReadAll(io.LimitReader(file, int64(maxBytes)+1))
	if err != nil {
		return nil, err
	}
	if len(payload) > maxBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, maxBytes)
	}
	return payload, nil
}

func readJSONFile[T any](path string, maxBytes int) (T, error) {
	var value T
	payload, err := readRegularFile(path, maxBytes)
	if err != nil {
		return value, err
	}
	if err := jsonutil.DecodeOneStrict(payload, &value); err != nil {
		return value, err
	}
	return value, nil
}

func syncDirectory(path string) error {
	return durablefs.SyncDirectory(path)
}

func runDirectoryName(runID string) string {
	sum := sha256.Sum256([]byte(runID))
	return hex.EncodeToString(sum[:])
}

func attemptDirectoryName(runID, attemptID string) string {
	sum := sha256.Sum256([]byte(runID + "\x00" + attemptID))
	return hex.EncodeToString(sum[:])
}

func eventFileName(sequence uint64) string {
	return fmt.Sprintf("%020d.json", sequence)
}
