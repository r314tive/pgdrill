package history

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"

	"github.com/r314tive/pgdrill/internal/model"
)

// VerificationResult is the bounded full-read view returned by Verify.
type VerificationResult struct {
	SchemaVersion              string   `json:"schema_version"`
	CompatibilityFloor         string   `json:"compatibility_floor"`
	StoreSchemaVersion         string   `json:"store_schema_version"`
	LayoutVersion              int      `json:"layout_version"`
	MigrationRequired          bool     `json:"migration_required"`
	MigrationPlanDigest        string   `json:"migration_plan_digest,omitempty"`
	MigratedFromSchemaVersion  string   `json:"migrated_from_schema_version,omitempty"`
	SourceSnapshotDigest       string   `json:"source_snapshot_digest,omitempty"`
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
		metadata, err := readJSONFile[StoreMetadata](
			filepath.Join(root, "store.json"),
			MaxIdentityBytes,
		)
		if err != nil {
			return fmt.Errorf("read history store metadata: %w", err)
		}
		result.StoreSchemaVersion = metadata.SchemaVersion
		result.LayoutVersion = metadata.LayoutVersion
		result.MigrationRequired = metadata.SchemaVersion != CurrentStoreSchemaVersion
		migration, migrationErr := readJSONFile[migrationRecord](
			filepath.Join(root, migrationRecordFileName),
			MaxIdentityBytes,
		)
		if migrationErr == nil {
			if metadata.SchemaVersion != CurrentStoreSchemaVersion {
				return fmt.Errorf("legacy history store must not contain a stable migration record")
			}
			if err := migration.validateStandalone(); err != nil {
				return err
			}
			result.MigrationPlanDigest = migration.PlanDigest
			result.MigratedFromSchemaVersion = migration.SourceStoreSchemaVersion
			result.SourceSnapshotDigest = migration.SourceSnapshotDigest
		} else if !errors.Is(migrationErr, os.ErrNotExist) {
			return fmt.Errorf("read history migration record: %w", migrationErr)
		}

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
		if err := validateRetentionStateCardinality(state); err != nil {
			return err
		}
		for _, digest := range state.operations {
			if err := validateRetentionOperationState(root, digest, slices.Contains(state.trash, digest)); err != nil {
				return err
			}
		}
		for _, digest := range state.pending {
			if _, err := validateCompletedRetentionOperation(root, digest); err != nil {
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

func validateRetentionStateCardinality(state retentionState) error {
	if len(state.operations) > 1 || len(state.trash) > 1 || len(state.pending) > 1 {
		return fmt.Errorf("history store has multiple concurrent retention operations")
	}
	if len(state.pending) > 0 && (len(state.operations) > 0 || len(state.trash) > 0) {
		return fmt.Errorf("history store has overlapping active and completed retention operations")
	}
	if len(state.trash) == 1 && !slices.Contains(state.operations, state.trash[0]) {
		return fmt.Errorf("history store has orphaned retention trash %s", state.trash[0])
	}
	return nil
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
		expected := expectedRetentionCompletion(plan)
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
	expected := expectedRetentionProgress(root, digest, plan)
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

func validateCompletedRetentionOperation(root, digest string) (RetentionPlan, error) {
	path := retentionPendingPath(root, digest)
	plan, err := readRetentionPlanAt(path, digest, "completed retention plan")
	if err != nil {
		return RetentionPlan{}, err
	}
	completion, err := readJSONFile[retentionCompletion](
		filepath.Join(path, retentionCompleteFileName),
		MaxIdentityBytes,
	)
	if err != nil {
		return RetentionPlan{}, fmt.Errorf("read completed retention result: %w", err)
	}
	if !reflect.DeepEqual(completion, expectedRetentionCompletion(plan)) {
		return RetentionPlan{}, fmt.Errorf("retention completion does not match confirmed plan")
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return RetentionPlan{}, fmt.Errorf("read completed retention operation: %w", err)
	}
	allowed := map[string]bool{
		retentionPlanFileName:      false,
		retentionProgressDirectory: true,
		retentionCompleteFileName:  false,
	}
	for _, entry := range entries {
		directory, ok := allowed[entry.Name()]
		if !ok || entry.IsDir() != directory {
			return RetentionPlan{}, fmt.Errorf(
				"completed retention operation contains unexpected entry %q",
				entry.Name(),
			)
		}
	}
	if len(entries) != len(allowed) {
		return RetentionPlan{}, fmt.Errorf("completed retention operation file set is incomplete")
	}

	progressDir := filepath.Join(path, retentionProgressDirectory)
	if err := requireDirectory(progressDir); err != nil {
		return RetentionPlan{}, fmt.Errorf("inspect completed retention progress: %w", err)
	}
	progressEntries, err := os.ReadDir(progressDir)
	if err != nil {
		return RetentionPlan{}, fmt.Errorf("read completed retention progress: %w", err)
	}
	expected := expectedRetentionProgress(root, digest, plan)
	if len(progressEntries) != len(expected) {
		return RetentionPlan{}, fmt.Errorf("completed retention progress is incomplete")
	}
	for _, entry := range progressEntries {
		want, ok := expected[entry.Name()]
		if !ok || entry.IsDir() {
			return RetentionPlan{}, fmt.Errorf(
				"completed retention progress contains unexpected entry %q",
				entry.Name(),
			)
		}
		actual, err := readJSONFile[retentionProgress](
			filepath.Join(progressDir, entry.Name()),
			MaxIdentityBytes,
		)
		if err != nil {
			return RetentionPlan{}, fmt.Errorf(
				"read completed retention progress %q: %w",
				entry.Name(),
				err,
			)
		}
		if !reflect.DeepEqual(actual, want) {
			return RetentionPlan{}, fmt.Errorf(
				"completed retention progress %q does not match confirmed plan",
				entry.Name(),
			)
		}
	}

	for _, item := range plan.Attempts {
		source := filepath.Join(
			root,
			"runs",
			runDirectoryName(item.RunID),
			"attempts",
			attemptDirectoryName(item.RunID, item.AttemptID),
		)
		if exists, err := privateDirectoryExists(source); err != nil {
			return RetentionPlan{}, err
		} else if exists {
			return RetentionPlan{}, fmt.Errorf(
				"completed retention attempt %q remains active",
				item.AttemptID,
			)
		}
	}
	for _, item := range plan.Runs {
		source := filepath.Join(root, "runs", runDirectoryName(item.RunID))
		if exists, err := privateDirectoryExists(source); err != nil {
			return RetentionPlan{}, err
		} else if exists {
			return RetentionPlan{}, fmt.Errorf(
				"completed retention run %q remains active",
				item.RunID,
			)
		}
	}
	return plan, nil
}

func expectedRetentionCompletion(plan RetentionPlan) retentionCompletion {
	return retentionCompletion{
		SchemaVersion:   retentionCompleteSchema,
		PlanDigest:      plan.Digest,
		DeletedAttempts: len(plan.Attempts),
		DeletedRuns:     len(plan.Runs),
	}
}

func expectedRetentionProgress(
	root, digest string,
	plan RetentionPlan,
) map[string]retentionProgress {
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
	return expected
}

func (v VerificationResult) Validate() error {
	if v.SchemaVersion != CurrentVerificationSchema ||
		v.CompatibilityFloor != PreGACompatibilityFloor ||
		v.LayoutVersion != CurrentLayoutVersion {
		return fmt.Errorf("history verification version is unsupported")
	}
	if v.StoreSchemaVersion != CurrentStoreSchemaVersion &&
		v.StoreSchemaVersion != LegacyStoreSchemaVersion {
		return fmt.Errorf("history verification store schema is unsupported")
	}
	if v.MigrationRequired != (v.StoreSchemaVersion == LegacyStoreSchemaVersion) {
		return fmt.Errorf("history verification migration state is inconsistent")
	}
	hasMigration := v.MigrationPlanDigest != "" ||
		v.MigratedFromSchemaVersion != "" ||
		v.SourceSnapshotDigest != ""
	if hasMigration {
		if v.StoreSchemaVersion != CurrentStoreSchemaVersion ||
			v.MigratedFromSchemaVersion != LegacyStoreSchemaVersion ||
			!model.IsSHA256Digest(v.MigrationPlanDigest) ||
			!model.IsSHA256Digest(v.SourceSnapshotDigest) {
			return fmt.Errorf("history verification migration provenance is inconsistent")
		}
	}
	if !countsWithinBounds(MaxRuns, v.Runs) ||
		!countsWithinBounds(
			MaxTotalAttempts,
			v.Attempts,
			v.TerminalReports,
			v.IncompleteAttempts,
		) ||
		v.Attempts > v.Runs*MaxAttemptsPerRun ||
		v.TerminalReports+v.IncompleteAttempts != v.Attempts ||
		!countsWithinBounds(MaxEventsPerRun*v.Runs, v.Events) ||
		!countsWithinBounds(
			model.MaxArtifactsPerReport*v.TerminalReports,
			v.ArtifactReferences,
		) {
		return fmt.Errorf("history verification counts are inconsistent")
	}
	if len(v.PendingRetentionOperations) > 1 || len(v.PendingRetentionCleanup) > 1 {
		return fmt.Errorf("history verification has multiple pending retention operations")
	}
	if len(v.PendingRetentionOperations) > 0 &&
		len(v.PendingRetentionCleanup) > 0 {
		return fmt.Errorf("history verification has overlapping active and completed retention operations")
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
