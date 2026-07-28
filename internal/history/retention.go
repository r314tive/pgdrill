package history

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/r314tive/pgdrill/internal/filelock"
	"github.com/r314tive/pgdrill/internal/model"
)

const (
	retentionDirectoryName       = "retention"
	retentionOperationsDirectory = "operations"
	retentionTrashDirectory      = "trash"
	retentionPendingDirectory    = "pending-delete"
	retentionPlanFileName        = "plan.json"
	retentionProgressDirectory   = "progress"
	retentionCompleteFileName    = "complete.json"

	retentionProgressSchema = "pgdrill.history-retention-progress/v1"
	retentionCompleteSchema = "pgdrill.history-retention-complete/v1"

	retentionStepAfterAttemptRename = "after_attempt_rename"
	retentionStepAfterAttemptMarker = "after_attempt_marker"
	retentionStepAfterRunRename     = "after_run_rename"
	retentionStepAfterRunMarker     = "after_run_marker"
	retentionStepAfterComplete      = "after_complete"
	retentionStepAfterFinalizeMove  = "after_finalize_move"
)

// RetentionPolicy protects incomplete attempts, the latest completed attempts,
// and audit-linked attempts unless IncludeAudit is explicitly enabled.
type RetentionPolicy struct {
	Before           time.Time `json:"before"`
	KeepLatestPerRun int       `json:"keep_latest_per_run"`
	IncludeAudit     bool      `json:"include_audit"`
}

// RetentionPlan is the canonical read-only selection that must be confirmed by
// its exact Digest before any history is removed.
type RetentionPlan struct {
	SchemaVersion      string             `json:"schema_version"`
	CompatibilityFloor string             `json:"compatibility_floor"`
	StoreSchemaVersion string             `json:"store_schema_version"`
	LayoutVersion      int                `json:"layout_version"`
	Policy             RetentionPolicy    `json:"policy"`
	Summary            RetentionSummary   `json:"summary"`
	Attempts           []RetentionAttempt `json:"attempts"`
	Runs               []RetentionRun     `json:"runs"`
	Digest             string             `json:"digest"`
}

// RetentionSummary accounts for every attempt exactly once as selected or
// protected by one policy rule.
type RetentionSummary struct {
	TotalRuns                  int `json:"total_runs"`
	TotalAttempts              int `json:"total_attempts"`
	SelectedAttempts           int `json:"selected_attempts"`
	SelectedRuns               int `json:"selected_runs"`
	ProtectedIncomplete        int `json:"protected_incomplete"`
	ProtectedLatest            int `json:"protected_latest"`
	ProtectedRecent            int `json:"protected_recent"`
	ProtectedAudit             int `json:"protected_audit"`
	RetainedArtifactReferences int `json:"retained_artifact_references"`
}

// RetentionAttempt binds one selected attempt to its immutable semantic
// content before the active directory is moved.
type RetentionAttempt struct {
	RunID              string            `json:"run_id"`
	AttemptID          string            `json:"attempt_id"`
	SpecDigest         string            `json:"spec_digest"`
	Status             model.DrillStatus `json:"status"`
	FinishedAt         time.Time         `json:"finished_at"`
	RecordDigest       string            `json:"record_digest"`
	ArtifactCount      int               `json:"artifact_count"`
	AuditArtifactCount int               `json:"audit_artifact_count"`
}

// RetentionRun identifies run metadata that becomes empty after every attempt
// in the run is selected.
type RetentionRun struct {
	RunID      string `json:"run_id"`
	SpecDigest string `json:"spec_digest"`
}

// PruneResult records the bounded outcome returned after a confirmed plan.
type PruneResult struct {
	SchemaVersion              string `json:"schema_version"`
	PlanDigest                 string `json:"plan_digest"`
	DeletedAttempts            int    `json:"deleted_attempts"`
	DeletedRuns                int    `json:"deleted_runs"`
	RetainedArtifactReferences int    `json:"retained_artifact_references"`
	Resumed                    bool   `json:"resumed"`
	AlreadyApplied             bool   `json:"already_applied"`
}

type retentionProgress struct {
	SchemaVersion string `json:"schema_version"`
	PlanDigest    string `json:"plan_digest"`
	Kind          string `json:"kind"`
	Index         int    `json:"index"`
	RunID         string `json:"run_id"`
	AttemptID     string `json:"attempt_id,omitempty"`
	RecordDigest  string `json:"record_digest,omitempty"`
}

type retentionCompletion struct {
	SchemaVersion   string `json:"schema_version"`
	PlanDigest      string `json:"plan_digest"`
	DeletedAttempts int    `json:"deleted_attempts"`
	DeletedRuns     int    `json:"deleted_runs"`
}

type retentionState struct {
	operations []string
	trash      []string
	pending    []string
}

func (s DirectoryStore) PlanRetention(ctx context.Context, policy RetentionPolicy) (RetentionPlan, error) {
	policy, err := normalizeRetentionPolicy(policy)
	if err != nil {
		return RetentionPlan{}, err
	}
	var plan RetentionPlan
	err = s.withReadLock(ctx, func(root string) error {
		metadata, err := readJSONFile[StoreMetadata](
			filepath.Join(root, "store.json"),
			MaxIdentityBytes,
		)
		if err != nil {
			return fmt.Errorf("read history store metadata: %w", err)
		}
		if metadata.SchemaVersion != CurrentStoreSchemaVersion {
			return fmt.Errorf(
				"history retention requires store schema_version %q; migrate the store first",
				CurrentStoreSchemaVersion,
			)
		}
		state, err := inspectRetentionState(root)
		if err != nil {
			return err
		}
		if err := requireCleanRetentionState(state); err != nil {
			return err
		}
		plan, err = buildRetentionPlan(root, policy)
		return err
	})
	if err != nil {
		return RetentionPlan{}, err
	}
	return plan, nil
}

// ApplyRetention recomputes or resumes a plan under the exclusive store lock.
// The caller must supply the exact digest emitted by PlanRetention.
func (s DirectoryStore) ApplyRetention(
	ctx context.Context,
	policy RetentionPolicy,
	confirmation string,
) (PruneResult, error) {
	policy, err := normalizeRetentionPolicy(policy)
	if err != nil {
		return PruneResult{}, err
	}
	if !model.IsSHA256Digest(confirmation) || confirmation != strings.ToLower(confirmation) {
		return PruneResult{}, fmt.Errorf("retention confirmation must be a canonical sha256 plan digest")
	}

	var result PruneResult
	err = s.withExistingWriteLock(ctx, func(root string) error {
		state, err := inspectRetentionState(root)
		if err != nil {
			return err
		}
		if err := validateRetentionStateCardinality(state); err != nil {
			return err
		}
		if len(state.pending) == 1 {
			if state.pending[0] != confirmation {
				return pendingRetentionError("completed cleanup", state.pending[0])
			}
			plan, err := validateCompletedRetentionOperation(root, confirmation)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(plan.Policy, policy) {
				return fmt.Errorf(
					"completed retention operation %s uses a different policy",
					confirmation,
				)
			}
			if err := removePrivateTree(retentionPendingPath(root, confirmation)); err != nil {
				return fmt.Errorf("finish completed retention cleanup: %w", err)
			}
			result = pruneResult(plan, true)
			result.AlreadyApplied = true
			return nil
		}
		if len(state.operations) == 1 && state.operations[0] != confirmation {
			return pendingRetentionError("operation", state.operations[0])
		}
		if len(state.trash) == 1 && state.trash[0] != confirmation {
			return pendingRetentionError("trash", state.trash[0])
		}

		resumed := len(state.operations) == 1
		var plan RetentionPlan
		if resumed {
			plan, err = readRetentionOperation(root, confirmation)
			if err != nil {
				if !errors.Is(err, os.ErrNotExist) || len(state.trash) != 0 {
					return err
				}
				// Operation publication precedes every data move. A process can
				// therefore leave an empty operation directory, but no trash,
				// before plan.json is linked into place.
				if err := removePrivateTree(retentionOperationPath(root, confirmation)); err != nil {
					return fmt.Errorf("remove unpublished retention operation: %w", err)
				}
				plan, err = buildRetentionPlan(root, policy)
				if err != nil {
					return err
				}
				if plan.Digest != confirmation {
					return fmt.Errorf(
						"retention confirmation %s is stale; current plan digest is %s",
						confirmation,
						plan.Digest,
					)
				}
				if err := createRetentionOperation(ctx, root, plan); err != nil {
					return err
				}
			}
			if !reflect.DeepEqual(plan.Policy, policy) {
				return fmt.Errorf("pending retention operation %s uses a different policy", confirmation)
			}
			if _, err := ensurePrivateChildDirectory(
				retentionOperationPath(root, confirmation),
				retentionProgressDirectory,
			); err != nil {
				return fmt.Errorf("restore retention progress directory: %w", err)
			}
			_, trashParent, _, err := ensureRetentionBase(root)
			if err != nil {
				return err
			}
			if _, err := ensurePrivateChildDirectory(
				trashParent,
				strings.TrimPrefix(confirmation, "sha256:"),
			); err != nil {
				return fmt.Errorf("restore retention trash directory: %w", err)
			}
		} else {
			plan, err = buildRetentionPlan(root, policy)
			if err != nil {
				return err
			}
			if plan.Digest != confirmation {
				return fmt.Errorf(
					"retention confirmation %s is stale; current plan digest is %s",
					confirmation,
					plan.Digest,
				)
			}
			if len(plan.Attempts) == 0 {
				result = pruneResult(plan, false)
				return nil
			}
			if err := createRetentionOperation(ctx, root, plan); err != nil {
				return err
			}
		}

		if err := s.executeRetentionOperation(ctx, root, plan); err != nil {
			return err
		}
		result = pruneResult(plan, resumed)
		return nil
	})
	if err != nil {
		return PruneResult{}, err
	}
	if err := result.Validate(); err != nil {
		return PruneResult{}, err
	}
	return result, nil
}

func (p RetentionPlan) Validate() error {
	if p.SchemaVersion != CurrentRetentionPlanSchema {
		return fmt.Errorf("retention plan schema_version must be %q", CurrentRetentionPlanSchema)
	}
	if p.CompatibilityFloor != PreGACompatibilityFloor {
		return fmt.Errorf("retention plan compatibility_floor must be %q", PreGACompatibilityFloor)
	}
	if p.StoreSchemaVersion != CurrentStoreSchemaVersion || p.LayoutVersion != CurrentLayoutVersion {
		return fmt.Errorf("retention plan store version is unsupported")
	}
	policy, err := normalizeRetentionPolicy(p.Policy)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(policy, p.Policy) {
		return fmt.Errorf("retention plan policy is not canonical")
	}
	if len(p.Attempts) > MaxTotalAttempts || len(p.Runs) > MaxRuns {
		return fmt.Errorf("retention plan exceeds store bounds")
	}
	if p.Summary.TotalRuns < 0 || p.Summary.TotalRuns > MaxRuns ||
		p.Summary.TotalAttempts < 0 || p.Summary.TotalAttempts > MaxTotalAttempts ||
		p.Summary.SelectedAttempts != len(p.Attempts) ||
		p.Summary.SelectedRuns != len(p.Runs) ||
		p.Summary.ProtectedIncomplete < 0 ||
		p.Summary.ProtectedLatest < 0 ||
		p.Summary.ProtectedRecent < 0 ||
		p.Summary.ProtectedAudit < 0 ||
		p.Summary.RetainedArtifactReferences < 0 {
		return fmt.Errorf("retention plan summary is inconsistent")
	}
	protected := p.Summary.ProtectedIncomplete +
		p.Summary.ProtectedLatest +
		p.Summary.ProtectedRecent +
		p.Summary.ProtectedAudit
	if p.Summary.SelectedAttempts+protected != p.Summary.TotalAttempts {
		return fmt.Errorf("retention plan attempt accounting is inconsistent")
	}
	if p.Summary.SelectedRuns > p.Summary.TotalRuns {
		return fmt.Errorf("retention plan run accounting is inconsistent")
	}
	if p.Summary.SelectedRuns > p.Summary.SelectedAttempts {
		return fmt.Errorf("retention plan cannot remove more runs than attempts")
	}

	artifactCount := 0
	attemptKeys := map[string]struct{}{}
	selectedRuns := map[string]string{}
	for index, attempt := range p.Attempts {
		if err := validateIdentityText("retention run_id", attempt.RunID); err != nil {
			return err
		}
		if err := validateIdentityText("retention attempt_id", attempt.AttemptID); err != nil {
			return err
		}
		if !model.IsSHA256Digest(attempt.SpecDigest) || !model.IsSHA256Digest(attempt.RecordDigest) {
			return fmt.Errorf("retention attempt digests must be canonical sha256 values")
		}
		if !attempt.Status.IsTerminal() {
			return fmt.Errorf("retention attempt %q status must be terminal", attempt.AttemptID)
		}
		if attempt.FinishedAt.IsZero() || !attempt.FinishedAt.Before(p.Policy.Before) {
			return fmt.Errorf("retention attempt %q is not older than the cutoff", attempt.AttemptID)
		}
		if attempt.ArtifactCount < 0 || attempt.AuditArtifactCount < 0 ||
			attempt.AuditArtifactCount > attempt.ArtifactCount {
			return fmt.Errorf("retention attempt %q artifact counts are invalid", attempt.AttemptID)
		}
		if !p.Policy.IncludeAudit && attempt.AuditArtifactCount > 0 {
			return fmt.Errorf("retention attempt %q contains protected audit artifacts", attempt.AttemptID)
		}
		key := attempt.RunID + "\x00" + attempt.AttemptID
		if _, duplicate := attemptKeys[key]; duplicate {
			return fmt.Errorf("retention plan contains duplicate attempt %q", attempt.AttemptID)
		}
		attemptKeys[key] = struct{}{}
		if digest, exists := selectedRuns[attempt.RunID]; exists && digest != attempt.SpecDigest {
			return fmt.Errorf("retention run %q uses conflicting spec digests", attempt.RunID)
		}
		selectedRuns[attempt.RunID] = attempt.SpecDigest
		if index > 0 && !retentionAttemptLess(p.Attempts[index-1], attempt) {
			return fmt.Errorf("retention plan attempts are not in canonical order")
		}
		artifactCount += attempt.ArtifactCount
	}
	if artifactCount != p.Summary.RetainedArtifactReferences {
		return fmt.Errorf("retention plan artifact accounting is inconsistent")
	}

	runIDs := map[string]struct{}{}
	for index, run := range p.Runs {
		if err := validateIdentityText("retention run_id", run.RunID); err != nil {
			return err
		}
		if !model.IsSHA256Digest(run.SpecDigest) {
			return fmt.Errorf("retention run spec_digest must be a canonical sha256 value")
		}
		if _, duplicate := runIDs[run.RunID]; duplicate {
			return fmt.Errorf("retention plan contains duplicate run %q", run.RunID)
		}
		runIDs[run.RunID] = struct{}{}
		specDigest, selected := selectedRuns[run.RunID]
		if !selected || specDigest != run.SpecDigest {
			return fmt.Errorf("retention run %q does not match selected attempts", run.RunID)
		}
		if index > 0 && p.Runs[index-1].RunID >= run.RunID {
			return fmt.Errorf("retention plan runs are not in canonical order")
		}
	}
	if !model.IsSHA256Digest(p.Digest) {
		return fmt.Errorf("retention plan digest must be a canonical sha256 value")
	}
	want, err := retentionPlanDigest(p)
	if err != nil {
		return err
	}
	if p.Digest != want {
		return fmt.Errorf("retention plan digest %s does not match canonical digest %s", p.Digest, want)
	}
	return nil
}

func (r PruneResult) Validate() error {
	if r.SchemaVersion != CurrentPruneResultSchema {
		return fmt.Errorf("history prune result schema_version must be %q", CurrentPruneResultSchema)
	}
	if !model.IsSHA256Digest(r.PlanDigest) || r.PlanDigest != strings.ToLower(r.PlanDigest) {
		return fmt.Errorf("history prune result plan_digest must be a canonical sha256 value")
	}
	if r.DeletedAttempts < 0 || r.DeletedAttempts > MaxTotalAttempts ||
		r.DeletedRuns < 0 || r.DeletedRuns > MaxRuns ||
		r.DeletedRuns > r.DeletedAttempts ||
		r.RetainedArtifactReferences < 0 {
		return fmt.Errorf("history prune result counts are inconsistent")
	}
	if r.AlreadyApplied && !r.Resumed {
		return fmt.Errorf("history prune result already_applied requires resumed")
	}
	return nil
}

func buildRetentionPlan(root string, policy RetentionPolicy) (RetentionPlan, error) {
	records, err := readAllRuns(root)
	if err != nil {
		return RetentionPlan{}, err
	}
	plan := RetentionPlan{
		SchemaVersion:      CurrentRetentionPlanSchema,
		CompatibilityFloor: PreGACompatibilityFloor,
		StoreSchemaVersion: CurrentStoreSchemaVersion,
		LayoutVersion:      CurrentLayoutVersion,
		Policy:             policy,
		Attempts:           []RetentionAttempt{},
		Runs:               []RetentionRun{},
	}
	plan.Summary.TotalRuns = len(records)
	for _, record := range records {
		plan.Summary.TotalAttempts += len(record.Attempts)
		latest := latestCompletedAttempts(record.Attempts, policy.KeepLatestPerRun)
		selectedForRun := 0
		for _, attempt := range record.Attempts {
			if attempt.Report == nil {
				plan.Summary.ProtectedIncomplete++
				continue
			}
			if _, protected := latest[attempt.AttemptID]; protected {
				plan.Summary.ProtectedLatest++
				continue
			}
			if !attempt.Report.FinishedAt.Before(policy.Before) {
				plan.Summary.ProtectedRecent++
				continue
			}
			auditCount := auditArtifactCount(attempt.Report.Artifacts)
			if auditCount > 0 && !policy.IncludeAudit {
				plan.Summary.ProtectedAudit++
				continue
			}
			item, err := newRetentionAttempt(record, attempt)
			if err != nil {
				return RetentionPlan{}, err
			}
			plan.Attempts = append(plan.Attempts, item)
			plan.Summary.RetainedArtifactReferences += item.ArtifactCount
			selectedForRun++
		}
		if selectedForRun > 0 && selectedForRun == len(record.Attempts) {
			plan.Runs = append(plan.Runs, RetentionRun{
				RunID:      record.RunID,
				SpecDigest: record.SpecDigest,
			})
		}
	}
	sort.Slice(plan.Attempts, func(i, j int) bool {
		return retentionAttemptLess(plan.Attempts[i], plan.Attempts[j])
	})
	sort.Slice(plan.Runs, func(i, j int) bool { return plan.Runs[i].RunID < plan.Runs[j].RunID })
	plan.Summary.SelectedAttempts = len(plan.Attempts)
	plan.Summary.SelectedRuns = len(plan.Runs)
	plan.Digest, err = retentionPlanDigest(plan)
	if err != nil {
		return RetentionPlan{}, err
	}
	if err := plan.Validate(); err != nil {
		return RetentionPlan{}, fmt.Errorf("validate retention plan: %w", err)
	}
	return plan, nil
}

func readAllRuns(root string) ([]RunRecord, error) {
	runsDir := filepath.Join(root, "runs")
	if err := requireDirectory(runsDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []RunRecord{}, nil
		}
		return nil, err
	}
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		return nil, fmt.Errorf("read history runs: %w", err)
	}
	if len(entries) > MaxRuns {
		return nil, fmt.Errorf("history store exceeds maximum run count %d", MaxRuns)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	records := make([]RunRecord, 0, len(entries))
	totalAttempts := 0
	for _, entry := range entries {
		if !entry.IsDir() || !validHashDirectory(entry.Name()) {
			return nil, fmt.Errorf("history runs contains unexpected entry %q", entry.Name())
		}
		record, err := readRun(filepath.Join(runsDir, entry.Name()))
		if err != nil {
			return nil, err
		}
		records = append(records, record)
		totalAttempts += len(record.Attempts)
		if totalAttempts > MaxTotalAttempts {
			return nil, fmt.Errorf("history store exceeds maximum total attempt count %d", MaxTotalAttempts)
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].RunID < records[j].RunID })
	return records, nil
}

func latestCompletedAttempts(attempts []AttemptRecord, keep int) map[string]struct{} {
	completed := make([]AttemptRecord, 0, len(attempts))
	for _, attempt := range attempts {
		if attempt.Report != nil {
			completed = append(completed, attempt)
		}
	}
	sort.Slice(completed, func(i, j int) bool {
		left := completed[i].Report.FinishedAt
		right := completed[j].Report.FinishedAt
		if !left.Equal(right) {
			return left.After(right)
		}
		return completed[i].AttemptID < completed[j].AttemptID
	})
	if keep > len(completed) {
		keep = len(completed)
	}
	protected := make(map[string]struct{}, keep)
	for _, attempt := range completed[:keep] {
		protected[attempt.AttemptID] = struct{}{}
	}
	return protected
}

func newRetentionAttempt(run RunRecord, attempt AttemptRecord) (RetentionAttempt, error) {
	if attempt.Report == nil {
		return RetentionAttempt{}, fmt.Errorf("retention attempt %q has no terminal report", attempt.AttemptID)
	}
	digest, err := retentionRecordDigest(run.RunID, run.SpecDigest, attempt)
	if err != nil {
		return RetentionAttempt{}, err
	}
	return RetentionAttempt{
		RunID:              run.RunID,
		AttemptID:          attempt.AttemptID,
		SpecDigest:         run.SpecDigest,
		Status:             attempt.Report.Status,
		FinishedAt:         attempt.Report.FinishedAt.UTC().Round(0),
		RecordDigest:       digest,
		ArtifactCount:      len(attempt.Report.Artifacts),
		AuditArtifactCount: auditArtifactCount(attempt.Report.Artifacts),
	}, nil
}

func retentionRecordDigest(runID, specDigest string, attempt AttemptRecord) (string, error) {
	payload, err := json.Marshal(struct {
		RunID      string        `json:"run_id"`
		SpecDigest string        `json:"spec_digest"`
		Attempt    AttemptRecord `json:"attempt"`
	}{
		RunID:      runID,
		SpecDigest: specDigest,
		Attempt:    attempt,
	})
	if err != nil {
		return "", fmt.Errorf("encode retention attempt: %w", err)
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func auditArtifactCount(artifacts []model.ArtifactRef) int {
	count := 0
	for _, artifact := range artifacts {
		if artifact.RetentionClass == model.ArtifactRetentionAudit {
			count++
		}
	}
	return count
}

func retentionPlanDigest(plan RetentionPlan) (string, error) {
	plan.Digest = ""
	payload, err := json.Marshal(plan)
	if err != nil {
		return "", fmt.Errorf("encode retention plan: %w", err)
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func retentionAttemptLess(left, right RetentionAttempt) bool {
	if left.RunID != right.RunID {
		return left.RunID < right.RunID
	}
	return left.AttemptID < right.AttemptID
}

func normalizeRetentionPolicy(policy RetentionPolicy) (RetentionPolicy, error) {
	if policy.Before.IsZero() {
		return RetentionPolicy{}, fmt.Errorf("retention cutoff is required")
	}
	if policy.KeepLatestPerRun < 0 || policy.KeepLatestPerRun > MaxAttemptsPerRun {
		return RetentionPolicy{}, fmt.Errorf(
			"retention keep_latest_per_run must be between 0 and %d",
			MaxAttemptsPerRun,
		)
	}
	policy.Before = policy.Before.UTC().Round(0)
	return policy, nil
}

func (s DirectoryStore) withExistingWriteLock(ctx context.Context, operation func(string) error) error {
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
	if err := requireDirectory(root); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrStoreNotFound
		}
		return fmt.Errorf("inspect history store: %w", err)
	}
	return withLock(ctx, root, filelock.Exclusive, false, func() error {
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

func inspectRetentionState(root string) (retentionState, error) {
	retentionRoot := filepath.Join(root, retentionDirectoryName)
	if err := requireDirectory(retentionRoot); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return retentionState{}, nil
		}
		return retentionState{}, fmt.Errorf("inspect history retention state: %w", err)
	}
	entries, err := os.ReadDir(retentionRoot)
	if err != nil {
		return retentionState{}, fmt.Errorf("read history retention state: %w", err)
	}
	allowed := map[string]struct{}{
		retentionOperationsDirectory: {},
		retentionTrashDirectory:      {},
		retentionPendingDirectory:    {},
	}
	for _, entry := range entries {
		if _, ok := allowed[entry.Name()]; !ok || !entry.IsDir() {
			return retentionState{}, fmt.Errorf("history retention contains unexpected entry %q", entry.Name())
		}
		if err := requireDirectory(filepath.Join(retentionRoot, entry.Name())); err != nil {
			return retentionState{}, err
		}
	}
	operations, err := readDigestDirectories(
		filepath.Join(retentionRoot, retentionOperationsDirectory),
		"history retention operations",
	)
	if err != nil {
		return retentionState{}, err
	}
	trash, err := readDigestDirectories(
		filepath.Join(retentionRoot, retentionTrashDirectory),
		"history retention trash",
	)
	if err != nil {
		return retentionState{}, err
	}
	pending, err := readDigestDirectories(
		filepath.Join(retentionRoot, retentionPendingDirectory),
		"history retention pending cleanup",
	)
	if err != nil {
		return retentionState{}, err
	}
	return retentionState{operations: operations, trash: trash, pending: pending}, nil
}

func readDigestDirectories(path, description string) ([]string, error) {
	if err := requireDirectory(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []string{}, nil
		}
		return nil, err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", description, err)
	}
	digests := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !validHashDirectory(entry.Name()) {
			return nil, fmt.Errorf("%s contains unexpected entry %q", description, entry.Name())
		}
		if err := requireDirectory(filepath.Join(path, entry.Name())); err != nil {
			return nil, err
		}
		digests = append(digests, "sha256:"+entry.Name())
	}
	sort.Strings(digests)
	return digests, nil
}

func requireCleanRetentionState(state retentionState) error {
	if len(state.operations) > 0 {
		return pendingRetentionError("operation", state.operations[0])
	}
	if len(state.trash) > 0 {
		return pendingRetentionError("trash", state.trash[0])
	}
	if len(state.pending) > 0 {
		return pendingRetentionError("completed cleanup", state.pending[0])
	}
	return nil
}

func pendingRetentionError(kind, digest string) error {
	return fmt.Errorf(
		"history store has pending retention %s %s; resume with the same policy and confirmation digest",
		kind,
		digest,
	)
}

func createRetentionOperation(ctx context.Context, root string, plan RetentionPlan) error {
	if err := plan.Validate(); err != nil {
		return err
	}
	operationsPath, trashPath, _, err := ensureRetentionBase(root)
	if err != nil {
		return err
	}
	operationPath := retentionOperationPath(root, plan.Digest)
	if exists, err := privateDirectoryExists(operationPath); err != nil {
		return err
	} else if exists {
		return fmt.Errorf("retention operation %s already exists", plan.Digest)
	}
	if _, err := ensurePrivateChildDirectory(
		operationsPath,
		strings.TrimPrefix(plan.Digest, "sha256:"),
	); err != nil {
		return fmt.Errorf("create retention operation: %w", err)
	}
	payload, err := marshalJSON(plan, MaxReportBytes)
	if err != nil {
		return fmt.Errorf("encode retention plan: %w", err)
	}
	if err := writeImmutable(
		ctx,
		operationPath,
		filepath.Join(operationPath, retentionPlanFileName),
		payload,
		MaxReportBytes,
	); err != nil {
		return fmt.Errorf("persist retention plan: %w", err)
	}
	if _, err := ensurePrivateChildDirectory(operationPath, retentionProgressDirectory); err != nil {
		return fmt.Errorf("create retention progress directory: %w", err)
	}
	if _, err := ensurePrivateChildDirectory(
		trashPath,
		strings.TrimPrefix(plan.Digest, "sha256:"),
	); err != nil {
		return fmt.Errorf("create retention trash: %w", err)
	}
	return nil
}

func readRetentionOperation(root, digest string) (RetentionPlan, error) {
	return readRetentionPlanAt(
		retentionOperationPath(root, digest),
		digest,
		"pending retention plan",
	)
}

func readRetentionPlanAt(path, digest, description string) (RetentionPlan, error) {
	plan, err := readJSONFile[RetentionPlan](
		filepath.Join(path, retentionPlanFileName),
		MaxReportBytes,
	)
	if err != nil {
		return RetentionPlan{}, fmt.Errorf("read %s: %w", description, err)
	}
	if err := plan.Validate(); err != nil {
		return RetentionPlan{}, fmt.Errorf("validate %s: %w", description, err)
	}
	if plan.Digest != digest {
		return RetentionPlan{}, fmt.Errorf("%s identity does not match directory", description)
	}
	return plan, nil
}

func (s DirectoryStore) executeRetentionOperation(ctx context.Context, root string, plan RetentionPlan) error {
	for index, attempt := range plan.Attempts {
		if err := s.applyRetentionAttempt(ctx, root, plan.Digest, index, attempt); err != nil {
			return fmt.Errorf(
				"apply retention attempt %s/%s: %w",
				attempt.RunID,
				attempt.AttemptID,
				err,
			)
		}
	}
	for index, run := range plan.Runs {
		if err := s.applyRetentionRun(ctx, root, plan.Digest, index, run); err != nil {
			return fmt.Errorf("apply retention run %s: %w", run.RunID, err)
		}
	}
	if err := removePrivateTree(retentionTrashPath(root, plan.Digest)); err != nil {
		return fmt.Errorf("remove retention trash: %w", err)
	}

	operationPath := retentionOperationPath(root, plan.Digest)
	completion := retentionCompletion{
		SchemaVersion:   retentionCompleteSchema,
		PlanDigest:      plan.Digest,
		DeletedAttempts: len(plan.Attempts),
		DeletedRuns:     len(plan.Runs),
	}
	payload, err := marshalJSON(completion, MaxIdentityBytes)
	if err != nil {
		return err
	}
	if err := writeImmutable(
		ctx,
		operationPath,
		filepath.Join(operationPath, retentionCompleteFileName),
		payload,
		MaxIdentityBytes,
	); err != nil {
		return fmt.Errorf("persist retention completion: %w", err)
	}
	if err := s.callRetentionHook(retentionStepAfterComplete, len(plan.Attempts)); err != nil {
		return err
	}

	_, _, pendingParent, err := ensureRetentionBase(root)
	if err != nil {
		return err
	}
	pendingPath := retentionPendingPath(root, plan.Digest)
	if exists, err := privateDirectoryExists(pendingPath); err != nil {
		return err
	} else if exists {
		return fmt.Errorf("retention pending-delete path already exists")
	}
	if err := os.Rename(operationPath, pendingPath); err != nil {
		return fmt.Errorf("finalize retention operation: %w", err)
	}
	if err := syncDirectory(filepath.Dir(operationPath)); err != nil {
		return fmt.Errorf("sync retention operations directory: %w", err)
	}
	if err := syncDirectory(pendingParent); err != nil {
		return fmt.Errorf("sync retention pending-delete directory: %w", err)
	}
	if err := s.callRetentionHook(retentionStepAfterFinalizeMove, len(plan.Attempts)); err != nil {
		return err
	}
	if err := removePrivateTree(pendingPath); err != nil {
		return fmt.Errorf("remove completed retention operation: %w", err)
	}
	return nil
}

func (s DirectoryStore) applyRetentionAttempt(
	ctx context.Context,
	root, planDigest string,
	index int,
	item RetentionAttempt,
) error {
	expected := retentionProgress{
		SchemaVersion: retentionProgressSchema,
		PlanDigest:    planDigest,
		Kind:          "attempt",
		Index:         index,
		RunID:         item.RunID,
		AttemptID:     item.AttemptID,
		RecordDigest:  item.RecordDigest,
	}
	markerPath := retentionProgressPath(root, planDigest, "attempt", index)
	marked, err := validateRetentionProgress(markerPath, expected)
	if err != nil {
		return err
	}
	source := filepath.Join(
		root,
		"runs",
		runDirectoryName(item.RunID),
		"attempts",
		attemptDirectoryName(item.RunID, item.AttemptID),
	)
	target := filepath.Join(
		retentionTrashPath(root, planDigest),
		"attempts",
		runDirectoryName(item.RunID),
		attemptDirectoryName(item.RunID, item.AttemptID),
	)
	if marked {
		if exists, err := privateDirectoryExists(source); err != nil {
			return err
		} else if exists {
			return fmt.Errorf("progress marker exists but active attempt still exists")
		}
	} else {
		attemptsTrash, err := ensurePrivateChildDirectory(
			retentionTrashPath(root, planDigest),
			"attempts",
		)
		if err != nil {
			return err
		}
		if _, err := ensurePrivateChildDirectory(
			attemptsTrash,
			runDirectoryName(item.RunID),
		); err != nil {
			return err
		}
		sourceExists, err := privateDirectoryExists(source)
		if err != nil {
			return err
		}
		targetExists, err := privateDirectoryExists(target)
		if err != nil {
			return err
		}
		if sourceExists && targetExists {
			return fmt.Errorf("attempt exists in both active store and retention trash")
		}
		if !sourceExists && !targetExists {
			return fmt.Errorf("attempt is missing from both active store and retention trash")
		}
		verifyPath := source
		if targetExists {
			verifyPath = target
		}
		if err := validateRetentionAttemptAt(verifyPath, item); err != nil {
			return err
		}
		if sourceExists {
			if err := os.Rename(source, target); err != nil {
				return fmt.Errorf("move attempt to retention trash: %w", err)
			}
			if err := syncDirectory(filepath.Dir(source)); err != nil {
				return err
			}
			if err := syncDirectory(filepath.Dir(target)); err != nil {
				return err
			}
			if err := s.callRetentionHook(retentionStepAfterAttemptRename, index); err != nil {
				return err
			}
		}
		if err := writeRetentionProgress(ctx, root, planDigest, expected, markerPath); err != nil {
			return err
		}
		if err := s.callRetentionHook(retentionStepAfterAttemptMarker, index); err != nil {
			return err
		}
	}
	if err := removePrivateTree(target); err != nil {
		return fmt.Errorf("remove retained attempt: %w", err)
	}
	return nil
}

func (s DirectoryStore) applyRetentionRun(
	ctx context.Context,
	root, planDigest string,
	index int,
	item RetentionRun,
) error {
	expected := retentionProgress{
		SchemaVersion: retentionProgressSchema,
		PlanDigest:    planDigest,
		Kind:          "run",
		Index:         index,
		RunID:         item.RunID,
	}
	markerPath := retentionProgressPath(root, planDigest, "run", index)
	marked, err := validateRetentionProgress(markerPath, expected)
	if err != nil {
		return err
	}
	source := filepath.Join(root, "runs", runDirectoryName(item.RunID))
	target := filepath.Join(retentionTrashPath(root, planDigest), "runs", runDirectoryName(item.RunID))
	if marked {
		if exists, err := privateDirectoryExists(source); err != nil {
			return err
		} else if exists {
			return fmt.Errorf("progress marker exists but empty active run still exists")
		}
	} else {
		if _, err := ensurePrivateChildDirectory(
			retentionTrashPath(root, planDigest),
			"runs",
		); err != nil {
			return err
		}
		sourceExists, err := privateDirectoryExists(source)
		if err != nil {
			return err
		}
		targetExists, err := privateDirectoryExists(target)
		if err != nil {
			return err
		}
		if sourceExists && targetExists {
			return fmt.Errorf("run exists in both active store and retention trash")
		}
		if !sourceExists && !targetExists {
			return fmt.Errorf("run is missing from both active store and retention trash")
		}
		verifyPath := source
		if targetExists {
			verifyPath = target
		}
		if err := validateEmptyRetentionRun(verifyPath, item); err != nil {
			return err
		}
		if sourceExists {
			if err := os.Rename(source, target); err != nil {
				return fmt.Errorf("move empty run to retention trash: %w", err)
			}
			if err := syncDirectory(filepath.Dir(source)); err != nil {
				return err
			}
			if err := syncDirectory(filepath.Dir(target)); err != nil {
				return err
			}
			if err := s.callRetentionHook(retentionStepAfterRunRename, index); err != nil {
				return err
			}
		}
		if err := writeRetentionProgress(ctx, root, planDigest, expected, markerPath); err != nil {
			return err
		}
		if err := s.callRetentionHook(retentionStepAfterRunMarker, index); err != nil {
			return err
		}
	}
	if err := removePrivateTree(target); err != nil {
		return fmt.Errorf("remove retained run metadata: %w", err)
	}
	return nil
}

func validateRetentionAttemptAt(path string, item RetentionAttempt) error {
	attempt, err := readAttempt(path, RunIdentity{
		SchemaVersion: CurrentRunSchemaVersion,
		RunID:         item.RunID,
		SpecDigest:    item.SpecDigest,
	})
	if err != nil {
		return err
	}
	actual, err := newRetentionAttempt(RunRecord{
		RunID:      item.RunID,
		SpecDigest: item.SpecDigest,
	}, attempt)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(actual, item) {
		return fmt.Errorf("retention attempt content does not match confirmed plan")
	}
	return nil
}

func validateEmptyRetentionRun(path string, item RetentionRun) error {
	record, identity, err := readRunHeader(path)
	if err != nil {
		return err
	}
	if identity.RunID != item.RunID || identity.SpecDigest != item.SpecDigest {
		return fmt.Errorf("retention run identity does not match confirmed plan")
	}
	attemptsDir := filepath.Join(path, "attempts")
	if err := requireDirectory(attemptsDir); err != nil {
		return err
	}
	entries, err := os.ReadDir(attemptsDir)
	if err != nil {
		return err
	}
	if len(entries) != 0 || len(record.Attempts) != 0 {
		return fmt.Errorf("retention run still contains protected attempts")
	}
	return nil
}

func validateRetentionProgress(path string, expected retentionProgress) (bool, error) {
	actual, err := readJSONFile[retentionProgress](path, MaxIdentityBytes)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read retention progress: %w", err)
	}
	if !reflect.DeepEqual(actual, expected) {
		return false, fmt.Errorf("retention progress does not match confirmed plan")
	}
	return true, nil
}

func writeRetentionProgress(
	ctx context.Context,
	root, planDigest string,
	progress retentionProgress,
	path string,
) error {
	payload, err := marshalJSON(progress, MaxIdentityBytes)
	if err != nil {
		return err
	}
	progressDir := filepath.Join(retentionOperationPath(root, planDigest), retentionProgressDirectory)
	if err := writeImmutable(ctx, progressDir, path, payload, MaxIdentityBytes); err != nil {
		return fmt.Errorf("persist retention progress: %w", err)
	}
	return nil
}

func removePrivateTree(path string) error {
	if err := requireDirectory(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	parent := filepath.Dir(path)
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	return syncDirectory(parent)
}

func ensureRetentionBase(root string) (operations, trash, pending string, err error) {
	retentionRoot, err := ensurePrivateChildDirectory(root, retentionDirectoryName)
	if err != nil {
		return "", "", "", fmt.Errorf("create retention directory: %w", err)
	}
	operations, err = ensurePrivateChildDirectory(retentionRoot, retentionOperationsDirectory)
	if err != nil {
		return "", "", "", fmt.Errorf("create retention operations directory: %w", err)
	}
	trash, err = ensurePrivateChildDirectory(retentionRoot, retentionTrashDirectory)
	if err != nil {
		return "", "", "", fmt.Errorf("create retention trash directory: %w", err)
	}
	pending, err = ensurePrivateChildDirectory(retentionRoot, retentionPendingDirectory)
	if err != nil {
		return "", "", "", fmt.Errorf("create retention pending-delete directory: %w", err)
	}
	return operations, trash, pending, nil
}

func ensurePrivateChildDirectory(parent, name string) (string, error) {
	if filepath.Base(name) != name || name == "." || name == "" {
		return "", fmt.Errorf("invalid private child directory name %q", name)
	}
	if err := requireDirectory(parent); err != nil {
		return "", err
	}
	path := filepath.Join(parent, name)
	if err := os.Mkdir(path, 0o700); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return "", err
		}
		if err := requireDirectory(path); err != nil {
			return "", err
		}
		return path, nil
	}
	if err := syncDirectory(parent); err != nil {
		return "", err
	}
	return path, nil
}

func (s DirectoryStore) callRetentionHook(step string, index int) error {
	if s.retentionHook == nil {
		return nil
	}
	return s.retentionHook(step, index)
}

func pruneResult(plan RetentionPlan, resumed bool) PruneResult {
	return PruneResult{
		SchemaVersion:              CurrentPruneResultSchema,
		PlanDigest:                 plan.Digest,
		DeletedAttempts:            len(plan.Attempts),
		DeletedRuns:                len(plan.Runs),
		RetainedArtifactReferences: plan.Summary.RetainedArtifactReferences,
		Resumed:                    resumed,
	}
}

func retentionOperationPath(root, digest string) string {
	return filepath.Join(
		root,
		retentionDirectoryName,
		retentionOperationsDirectory,
		strings.TrimPrefix(digest, "sha256:"),
	)
}

func retentionTrashPath(root, digest string) string {
	return filepath.Join(
		root,
		retentionDirectoryName,
		retentionTrashDirectory,
		strings.TrimPrefix(digest, "sha256:"),
	)
}

func retentionPendingPath(root, digest string) string {
	return filepath.Join(
		root,
		retentionDirectoryName,
		retentionPendingDirectory,
		strings.TrimPrefix(digest, "sha256:"),
	)
}

func retentionProgressPath(root, digest, kind string, index int) string {
	return filepath.Join(
		retentionOperationPath(root, digest),
		retentionProgressDirectory,
		fmt.Sprintf("%s-%06d.json", kind, index),
	)
}

func containsDigest(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
