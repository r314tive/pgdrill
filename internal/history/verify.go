package history

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/r314tive/pgdrill/internal/model"
)

// VerificationResult is the bounded full-read view returned by Verify.
type VerificationResult struct {
	SchemaVersion              string   `json:"schema_version"`
	CompatibilityFloor         string   `json:"compatibility_floor"`
	StoreSchemaVersion         string   `json:"store_schema_version"`
	LayoutVersion              int      `json:"layout_version"`
	Runs                       int      `json:"runs"`
	Attempts                   int      `json:"attempts"`
	TerminalReports            int      `json:"terminal_reports"`
	IncompleteAttempts         int      `json:"incomplete_attempts"`
	Events                     int      `json:"events"`
	ArtifactReferences         int      `json:"artifact_references"`
	PendingRetentionOperations []string `json:"pending_retention_operations"`
	PendingRetentionCleanup    []string `json:"pending_retention_cleanup"`
	MaintenanceRequired        bool     `json:"maintenance_required"`
}

// Verify fully decodes every retained run, attempt, event, and report. Unlike
// List, it deliberately does not use summary.json as a shortcut.
func (s DirectoryStore) Verify(ctx context.Context) (VerificationResult, error) {
	result := VerificationResult{
		SchemaVersion:              CurrentVerificationSchema,
		CompatibilityFloor:         PreGACompatibilityFloor,
		StoreSchemaVersion:         CurrentStoreSchemaVersion,
		LayoutVersion:              CurrentLayoutVersion,
		PendingRetentionOperations: []string{},
		PendingRetentionCleanup:    []string{},
	}
	err := s.withReadLock(ctx, func(root string) error {
		records, err := readAllRuns(root)
		if err != nil {
			return err
		}
		result.Runs = len(records)
		for _, record := range records {
			result.Attempts += len(record.Attempts)
			for _, attempt := range record.Attempts {
				result.Events += len(attempt.Events)
				if attempt.Report == nil {
					result.IncompleteAttempts++
					continue
				}
				result.TerminalReports++
				result.ArtifactReferences += len(attempt.Report.Artifacts)
			}
		}

		state, err := inspectRetentionState(root)
		if err != nil {
			return err
		}
		if len(state.operations) > 1 || len(state.trash) > 1 {
			return fmt.Errorf("history store has multiple concurrent retention operations")
		}
		if len(state.pending) > 0 && (len(state.operations) > 0 || len(state.trash) > 0) {
			return fmt.Errorf("history store has overlapping active and completed retention operations")
		}
		if len(state.trash) == 1 && !containsDigest(state.operations, state.trash[0]) {
			return fmt.Errorf("history store has orphaned retention trash %s", state.trash[0])
		}
		for _, digest := range state.operations {
			if err := validateRetentionOperationState(root, digest, containsDigest(state.trash, digest)); err != nil {
				return err
			}
		}
		result.PendingRetentionOperations = append([]string{}, state.operations...)
		result.PendingRetentionCleanup = append([]string{}, state.pending...)
		result.MaintenanceRequired = len(state.operations) > 0 || len(state.pending) > 0
		return nil
	})
	if err != nil {
		return VerificationResult{}, err
	}
	if err := result.Validate(); err != nil {
		return VerificationResult{}, err
	}
	return result, nil
}

func validateRetentionOperationState(root, digest string, hasTrash bool) error {
	plan, err := readRetentionOperation(root, digest)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && !hasTrash {
			// The operation directory is published before plan.json. No data
			// can have moved while both plan and trash are absent.
			return nil
		}
		return err
	}
	completionPath := filepath.Join(
		retentionOperationPath(root, digest),
		retentionCompleteFileName,
	)
	completion, err := readJSONFile[retentionCompletion](completionPath, MaxIdentityBytes)
	if err == nil {
		expected := retentionCompletion{
			SchemaVersion:   retentionCompleteSchema,
			PlanDigest:      plan.Digest,
			DeletedAttempts: len(plan.Attempts),
			DeletedRuns:     len(plan.Runs),
		}
		if !reflect.DeepEqual(completion, expected) {
			return fmt.Errorf("retention completion does not match confirmed plan")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read retention completion: %w", err)
	}
	progressDir := filepath.Join(retentionOperationPath(root, digest), retentionProgressDirectory)
	if err := requireDirectory(progressDir); err != nil {
		if errors.Is(err, os.ErrNotExist) && !hasTrash {
			return nil
		}
		return fmt.Errorf("inspect retention progress: %w", err)
	}
	entries, err := os.ReadDir(progressDir)
	if err != nil {
		return fmt.Errorf("read retention progress: %w", err)
	}
	expected := make(map[string]retentionProgress, len(plan.Attempts)+len(plan.Runs))
	for index, item := range plan.Attempts {
		name := filepath.Base(retentionProgressPath(root, digest, "attempt", index))
		expected[name] = retentionProgress{
			SchemaVersion: retentionProgressSchema,
			PlanDigest:    digest,
			Kind:          "attempt",
			Index:         index,
			RunID:         item.RunID,
			AttemptID:     item.AttemptID,
			RecordDigest:  item.RecordDigest,
		}
	}
	for index, item := range plan.Runs {
		name := filepath.Base(retentionProgressPath(root, digest, "run", index))
		expected[name] = retentionProgress{
			SchemaVersion: retentionProgressSchema,
			PlanDigest:    digest,
			Kind:          "run",
			Index:         index,
			RunID:         item.RunID,
		}
	}
	marked := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		want, ok := expected[entry.Name()]
		if !ok || entry.IsDir() {
			return fmt.Errorf("retention progress contains unexpected entry %q", entry.Name())
		}
		actual, err := readJSONFile[retentionProgress](
			filepath.Join(progressDir, entry.Name()),
			MaxIdentityBytes,
		)
		if err != nil {
			return fmt.Errorf("read retention progress %q: %w", entry.Name(), err)
		}
		if !reflect.DeepEqual(actual, want) {
			return fmt.Errorf("retention progress %q does not match confirmed plan", entry.Name())
		}
		marked[entry.Name()] = struct{}{}
	}

	for index, item := range plan.Attempts {
		name := filepath.Base(retentionProgressPath(root, digest, "attempt", index))
		_, progressExists := marked[name]
		source := filepath.Join(
			root,
			"runs",
			runDirectoryName(item.RunID),
			"attempts",
			attemptDirectoryName(item.RunID, item.AttemptID),
		)
		target := filepath.Join(
			retentionTrashPath(root, digest),
			"attempts",
			runDirectoryName(item.RunID),
			attemptDirectoryName(item.RunID, item.AttemptID),
		)
		sourceExists, err := privateDirectoryExists(source)
		if err != nil {
			return err
		}
		targetExists, err := privateDirectoryExists(target)
		if err != nil {
			return err
		}
		if sourceExists && targetExists {
			return fmt.Errorf("retention attempt %q exists in active state and trash", item.AttemptID)
		}
		if progressExists && sourceExists {
			return fmt.Errorf("retention attempt %q has progress but remains active", item.AttemptID)
		}
		if !progressExists && !sourceExists && !targetExists {
			return fmt.Errorf("retention attempt %q disappeared before progress was persisted", item.AttemptID)
		}
		if sourceExists {
			if err := validateRetentionAttemptAt(source, item); err != nil {
				return err
			}
		}
		if targetExists {
			if err := validateRetentionAttemptAt(target, item); err != nil {
				return err
			}
		}
	}

	for index, item := range plan.Runs {
		name := filepath.Base(retentionProgressPath(root, digest, "run", index))
		_, progressExists := marked[name]
		source := filepath.Join(root, "runs", runDirectoryName(item.RunID))
		target := filepath.Join(retentionTrashPath(root, digest), "runs", runDirectoryName(item.RunID))
		sourceExists, err := privateDirectoryExists(source)
		if err != nil {
			return err
		}
		targetExists, err := privateDirectoryExists(target)
		if err != nil {
			return err
		}
		if sourceExists && targetExists {
			return fmt.Errorf("retention run %q exists in active state and trash", item.RunID)
		}
		if progressExists && sourceExists {
			return fmt.Errorf("retention run %q has progress but remains active", item.RunID)
		}
		if !progressExists && !sourceExists && !targetExists {
			return fmt.Errorf("retention run %q disappeared before progress was persisted", item.RunID)
		}
		if targetExists {
			if err := validateEmptyRetentionRun(target, item); err != nil {
				return err
			}
		}
	}
	return nil
}

func (v VerificationResult) Validate() error {
	if v.SchemaVersion != CurrentVerificationSchema ||
		v.CompatibilityFloor != PreGACompatibilityFloor ||
		v.StoreSchemaVersion != CurrentStoreSchemaVersion ||
		v.LayoutVersion != CurrentLayoutVersion {
		return fmt.Errorf("history verification version is unsupported")
	}
	if v.Runs < 0 || v.Runs > MaxRuns ||
		v.Attempts < 0 || v.Attempts > MaxTotalAttempts ||
		v.TerminalReports < 0 || v.IncompleteAttempts < 0 ||
		v.TerminalReports+v.IncompleteAttempts != v.Attempts ||
		v.Events < 0 || v.ArtifactReferences < 0 {
		return fmt.Errorf("history verification counts are inconsistent")
	}
	if len(v.PendingRetentionOperations) > 1 || len(v.PendingRetentionCleanup) > 1 {
		return fmt.Errorf("history verification has multiple pending retention operations")
	}
	for _, digest := range append(
		append([]string(nil), v.PendingRetentionOperations...),
		v.PendingRetentionCleanup...,
	) {
		if !model.IsSHA256Digest(digest) {
			return fmt.Errorf("history verification contains an invalid retention digest")
		}
	}
	wantMaintenance := len(v.PendingRetentionOperations) > 0 || len(v.PendingRetentionCleanup) > 0
	if v.MaintenanceRequired != wantMaintenance {
		return fmt.Errorf("history verification maintenance state is inconsistent")
	}
	return nil
}
