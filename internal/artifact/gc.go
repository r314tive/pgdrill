package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	gcOperationsDirectory = "operations"
	gcTrashDirectory      = "trash"
	gcPendingDirectory    = "pending-delete"
	gcProgressDirectory   = "progress"
	gcPlanFileName        = "plan.json"
	gcCompleteFileName    = "complete.json"

	gcProgressSchema = "pgdrill.artifact-gc-progress/v1"
	gcCompleteSchema = "pgdrill.artifact-gc-completion/v1"

	gcStepAfterBlobRename     = "after_blob_rename"
	gcStepAfterClaimRename    = "after_claim_rename"
	gcStepAfterItemMarker     = "after_item_marker"
	gcStepAfterComplete       = "after_complete"
	gcStepAfterFinalizeRename = "after_finalize_rename"
)

type GCPolicy struct {
	Before           time.Time `json:"before"`
	IncludeAudit     bool      `json:"include_audit"`
	IncludeLegacy    bool      `json:"include_legacy"`
	IncludeTemporary bool      `json:"include_temporary"`
}

type GCSummary struct {
	TotalBlobs              int   `json:"total_blobs"`
	TotalBytes              int64 `json:"total_bytes"`
	ReferencedBlobs         int   `json:"referenced_blobs"`
	ReferencedOccurrences   int   `json:"referenced_occurrences"`
	ForeignReferences       int   `json:"foreign_references"`
	ProtectedRecentBlobs    int   `json:"protected_recent_blobs"`
	ProtectedAuditBlobs     int   `json:"protected_audit_blobs"`
	ProtectedLegacyBlobs    int   `json:"protected_legacy_blobs"`
	CandidateBlobs          int   `json:"candidate_blobs"`
	CandidateBlobBytes      int64 `json:"candidate_blob_bytes"`
	TemporaryFiles          int   `json:"temporary_files"`
	ProtectedTemporaryFiles int   `json:"protected_temporary_files"`
	CandidateTemporaryFiles int   `json:"candidate_temporary_files"`
	CandidateTemporaryBytes int64 `json:"candidate_temporary_bytes"`
}

type GCBlob struct {
	ID               string                         `json:"id"`
	SizeBytes        int64                          `json:"size_bytes"`
	LastObservedAt   time.Time                      `json:"last_observed_at"`
	RetentionClasses []model.ArtifactRetentionClass `json:"retention_classes"`
	Legacy           bool                           `json:"legacy"`
	ClaimDigest      string                         `json:"claim_digest"`
	RecordDigest     string                         `json:"record_digest"`
}

type GCTemporaryFile struct {
	RelativePath string    `json:"relative_path"`
	SizeBytes    int64     `json:"size_bytes"`
	ModifiedAt   time.Time `json:"modified_at"`
	RecordDigest string    `json:"record_digest"`
}

type GCPlan struct {
	SchemaVersion      string            `json:"schema_version"`
	StoreSchemaVersion string            `json:"store_schema_version,omitempty"`
	LayoutVersion      int               `json:"layout_version,omitempty"`
	URIBase            string            `json:"uri_base"`
	Policy             GCPolicy          `json:"policy"`
	ReferenceDigest    string            `json:"reference_digest"`
	Summary            GCSummary         `json:"summary"`
	Blobs              []GCBlob          `json:"blobs"`
	TemporaryFiles     []GCTemporaryFile `json:"temporary_files"`
	Digest             string            `json:"digest"`
}

type GCResult struct {
	SchemaVersion         string `json:"schema_version"`
	PlanDigest            string `json:"plan_digest"`
	DeletedBlobs          int    `json:"deleted_blobs"`
	DeletedBlobBytes      int64  `json:"deleted_blob_bytes"`
	DeletedTemporaryFiles int    `json:"deleted_temporary_files"`
	DeletedTemporaryBytes int64  `json:"deleted_temporary_bytes"`
	Resumed               bool   `json:"resumed"`
	AlreadyApplied        bool   `json:"already_applied"`
	ReferenceScopeChanged bool   `json:"reference_scope_changed"`
}

type gcState struct {
	operations []string
	trash      []string
	pending    []string
}

type gcProgress struct {
	SchemaVersion string `json:"schema_version"`
	PlanDigest    string `json:"plan_digest"`
	Kind          string `json:"kind"`
	Index         int    `json:"index"`
	Identity      string `json:"identity"`
	RecordDigest  string `json:"record_digest"`
}

type gcCompletion struct {
	SchemaVersion         string `json:"schema_version"`
	PlanDigest            string `json:"plan_digest"`
	DeletedBlobs          int    `json:"deleted_blobs"`
	DeletedTemporaryFiles int    `json:"deleted_temporary_files"`
}

func (s DirectoryStore) PlanGC(
	ctx context.Context,
	policy GCPolicy,
	references []model.ArtifactRef,
) (GCPlan, error) {
	settings, err := s.settings()
	if err != nil {
		return GCPlan{}, err
	}
	policy, err = normalizeGCPolicy(policy)
	if err != nil {
		return GCPlan{}, err
	}
	var plan GCPlan
	err = withStoreLock(ctx, settings.root, filelock.Shared, false, func() error {
		inventory, err := scanStore(ctx, settings, references)
		if err != nil {
			return err
		}
		if inventory.Managed &&
			inventory.StoreMetadata.SchemaVersion != CurrentStoreSchemaVersion {
			return fmt.Errorf(
				"artifact GC requires store schema_version %q; legacy stores are read-only",
				CurrentStoreSchemaVersion,
			)
		}
		if err := requireCleanGCState(inventory); err != nil {
			return err
		}
		if err := validateLiveReferences(inventory); err != nil {
			return err
		}
		if len(inventory.OrphanClaimIDs) > 0 {
			return fmt.Errorf(
				"artifact store has claims without blobs: %s",
				strings.Join(inventory.OrphanClaimIDs, ", "),
			)
		}
		plan, err = buildGCPlan(settings, policy, inventory)
		return err
	})
	if err != nil {
		return GCPlan{}, err
	}
	return plan, nil
}

func (s DirectoryStore) ApplyGC(
	ctx context.Context,
	policy GCPolicy,
	references []model.ArtifactRef,
	confirmation string,
) (GCResult, error) {
	settings, err := s.settings()
	if err != nil {
		return GCResult{}, err
	}
	policy, err = normalizeGCPolicy(policy)
	if err != nil {
		return GCResult{}, err
	}
	confirmation = strings.TrimSpace(confirmation)
	if !model.IsSHA256Digest(confirmation) {
		return GCResult{}, fmt.Errorf("artifact GC confirmation must be a canonical sha256 digest")
	}
	var result GCResult
	err = withStoreLock(ctx, settings.root, filelock.Exclusive, false, func() error {
		inventory, err := scanStore(ctx, settings, references)
		if err != nil {
			return err
		}
		if inventory.Managed &&
			inventory.StoreMetadata.SchemaVersion != CurrentStoreSchemaVersion {
			return fmt.Errorf(
				"artifact GC requires store schema_version %q; legacy stores are read-only",
				CurrentStoreSchemaVersion,
			)
		}
		state := gcState{
			operations: inventory.ActiveGCDigests,
			trash:      inventory.TrashGCDigests,
			pending:    inventory.PendingGCDigests,
		}
		if err := validateGCStateCardinality(state); err != nil {
			return err
		}
		if err := validateLiveReferences(inventory); err != nil {
			return err
		}
		if len(state.pending) == 1 {
			if state.pending[0] != confirmation {
				return pendingGCError("completed cleanup", state.pending[0])
			}
			plan, err := readPendingGCPlan(settings.root, confirmation)
			if err != nil {
				return err
			}
			scopeChanged, err := validateResumeInputs(
				plan,
				policy,
				inventory,
				settings,
			)
			if err != nil {
				return err
			}
			if err := removePrivateTree(gcPendingPath(settings.root, confirmation)); err != nil {
				return fmt.Errorf("remove completed artifact GC operation: %w", err)
			}
			result = gcResult(plan, true, true, scopeChanged)
			return nil
		}

		resumed := len(state.operations) == 1 || len(state.trash) == 1
		var plan GCPlan
		if resumed {
			active := confirmation
			if len(state.operations) == 1 {
				active = state.operations[0]
			} else if len(state.trash) == 1 {
				active = state.trash[0]
			}
			if active != confirmation {
				return pendingGCError("operation", active)
			}
			plan, err = readGCOperation(settings.root, confirmation)
			if err != nil {
				return err
			}
			scopeChanged, err := validateResumeInputs(
				plan,
				policy,
				inventory,
				settings,
			)
			if err != nil {
				return err
			}
			result.ReferenceScopeChanged = scopeChanged
		} else {
			if err := requireCleanGCState(inventory); err != nil {
				return err
			}
			if len(inventory.OrphanClaimIDs) > 0 {
				return fmt.Errorf(
					"artifact store has claims without blobs: %s",
					strings.Join(inventory.OrphanClaimIDs, ", "),
				)
			}
			plan, err = buildGCPlan(settings, policy, inventory)
			if err != nil {
				return err
			}
			if plan.Digest != confirmation {
				return fmt.Errorf(
					"artifact GC confirmation %s is stale; current plan digest is %s",
					confirmation,
					plan.Digest,
				)
			}
			if err := createGCOperation(ctx, settings.root, plan); err != nil {
				return err
			}
		}
		if plan.Digest != confirmation {
			return fmt.Errorf("pending artifact GC plan does not match confirmation")
		}
		if err := s.executeGCOperation(ctx, settings.root, plan); err != nil {
			return err
		}
		result = gcResult(plan, resumed, false, result.ReferenceScopeChanged)
		return nil
	})
	if err != nil {
		return GCResult{}, err
	}
	if err := result.Validate(); err != nil {
		return GCResult{}, err
	}
	return result, nil
}

func normalizeGCPolicy(policy GCPolicy) (GCPolicy, error) {
	if policy.Before.IsZero() {
		return GCPolicy{}, fmt.Errorf("artifact GC cutoff is required")
	}
	policy.Before = policy.Before.UTC().Round(0)
	return policy, nil
}

func buildGCPlan(
	settings directorySettings,
	policy GCPolicy,
	inventory storeInventory,
) (GCPlan, error) {
	plan := GCPlan{
		SchemaVersion:   CurrentGCPlanSchemaVersion,
		URIBase:         settings.uriBase,
		Policy:          policy,
		ReferenceDigest: inventory.ReferenceIndex.Digest,
		Blobs:           []GCBlob{},
		TemporaryFiles:  []GCTemporaryFile{},
	}
	if inventory.Managed {
		plan.StoreSchemaVersion = inventory.StoreMetadata.SchemaVersion
		plan.LayoutVersion = inventory.StoreMetadata.LayoutVersion
	}
	plan.Summary.ReferencedOccurrences = inventory.ReferenceIndex.Occurrences
	plan.Summary.ForeignReferences = inventory.ReferenceIndex.Foreign
	for _, blob := range inventory.Blobs {
		plan.Summary.TotalBlobs++
		plan.Summary.TotalBytes += blob.SizeBytes
		if _, referenced := inventory.ReferenceIndex.ByID[blob.ID]; referenced {
			plan.Summary.ReferencedBlobs++
			continue
		}
		if !blob.LastObservedAt.Before(policy.Before) {
			plan.Summary.ProtectedRecentBlobs++
			continue
		}
		if blob.hasRetention(model.ArtifactRetentionAudit) && !policy.IncludeAudit {
			plan.Summary.ProtectedAuditBlobs++
			continue
		}
		if blob.legacy() && !policy.IncludeLegacy {
			plan.Summary.ProtectedLegacyBlobs++
			continue
		}
		item, err := newGCBlob(blob)
		if err != nil {
			return GCPlan{}, err
		}
		plan.Blobs = append(plan.Blobs, item)
		plan.Summary.CandidateBlobs++
		plan.Summary.CandidateBlobBytes += item.SizeBytes
	}
	for _, temporary := range inventory.TemporaryFiles {
		plan.Summary.TemporaryFiles++
		if !policy.IncludeTemporary || !temporary.ModifiedAt.Before(policy.Before) {
			plan.Summary.ProtectedTemporaryFiles++
			continue
		}
		item, err := newGCTemporaryFile(temporary)
		if err != nil {
			return GCPlan{}, err
		}
		plan.TemporaryFiles = append(plan.TemporaryFiles, item)
		plan.Summary.CandidateTemporaryFiles++
		plan.Summary.CandidateTemporaryBytes += item.SizeBytes
	}
	sort.Slice(plan.Blobs, func(i, j int) bool { return plan.Blobs[i].ID < plan.Blobs[j].ID })
	sort.Slice(plan.TemporaryFiles, func(i, j int) bool {
		return plan.TemporaryFiles[i].RelativePath < plan.TemporaryFiles[j].RelativePath
	})
	digest, err := planDigest(plan)
	if err != nil {
		return GCPlan{}, err
	}
	plan.Digest = digest
	if err := plan.Validate(); err != nil {
		return GCPlan{}, err
	}
	return plan, nil
}

func newGCBlob(blob blobState) (GCBlob, error) {
	classes := make([]model.ArtifactRetentionClass, 0)
	for _, claim := range blob.Claims {
		classes = appendUniqueSortedRetention(classes, claim.RetentionClass)
	}
	item := GCBlob{
		ID:               blob.ID,
		SizeBytes:        blob.SizeBytes,
		LastObservedAt:   blob.LastObservedAt.UTC().Round(0),
		RetentionClasses: classes,
		Legacy:           blob.legacy(),
		ClaimDigest:      blob.ClaimDigest,
	}
	digest, err := gcBlobDigest(item)
	if err != nil {
		return GCBlob{}, err
	}
	item.RecordDigest = digest
	return item, nil
}

func newGCTemporaryFile(state temporaryState) (GCTemporaryFile, error) {
	item := GCTemporaryFile{
		RelativePath: state.RelativePath,
		SizeBytes:    state.SizeBytes,
		ModifiedAt:   state.ModifiedAt.UTC().Round(0),
	}
	digest, err := gcTemporaryDigest(item)
	if err != nil {
		return GCTemporaryFile{}, err
	}
	item.RecordDigest = digest
	return item, nil
}

func planDigest(plan GCPlan) (string, error) {
	plan.Digest = ""
	payload, err := marshalBoundedJSON(plan, maxGCPlanJSONBytes)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func gcBlobDigest(item GCBlob) (string, error) {
	item.RecordDigest = ""
	payload, err := marshalBoundedJSON(item, maxClaimJSONBytes)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func gcTemporaryDigest(item GCTemporaryFile) (string, error) {
	item.RecordDigest = ""
	payload, err := marshalBoundedJSON(item, maxClaimJSONBytes)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func (p GCPlan) Validate() error {
	if p.SchemaVersion != CurrentGCPlanSchemaVersion {
		return fmt.Errorf("artifact GC plan schema_version must be %q", CurrentGCPlanSchemaVersion)
	}
	if p.StoreSchemaVersion == "" {
		if p.LayoutVersion != 0 {
			return fmt.Errorf("legacy artifact GC plan must not declare a layout version")
		}
	} else if p.StoreSchemaVersion != CurrentStoreSchemaVersion ||
		p.LayoutVersion != CurrentLayoutVersion {
		return fmt.Errorf("artifact GC plan store version is unsupported")
	}
	if strings.TrimSpace(p.URIBase) == "" {
		return fmt.Errorf("artifact GC plan uri_base is required")
	}
	policy, err := normalizeGCPolicy(p.Policy)
	if err != nil || !reflect.DeepEqual(policy, p.Policy) {
		return fmt.Errorf("artifact GC plan policy is not canonical")
	}
	if !model.IsSHA256Digest(p.ReferenceDigest) || !model.IsSHA256Digest(p.Digest) {
		return fmt.Errorf("artifact GC plan digests must be canonical sha256 values")
	}
	if err := p.Summary.validateBounds(); err != nil {
		return err
	}
	classified := p.Summary.ReferencedBlobs +
		p.Summary.ProtectedRecentBlobs +
		p.Summary.ProtectedAuditBlobs +
		p.Summary.ProtectedLegacyBlobs +
		p.Summary.CandidateBlobs
	if classified != p.Summary.TotalBlobs ||
		p.Summary.CandidateBlobs != len(p.Blobs) ||
		p.Summary.ProtectedTemporaryFiles+p.Summary.CandidateTemporaryFiles !=
			p.Summary.TemporaryFiles ||
		p.Summary.CandidateTemporaryFiles != len(p.TemporaryFiles) {
		return fmt.Errorf("artifact GC plan accounting is inconsistent")
	}
	var blobBytes int64
	previousID := ""
	for _, item := range p.Blobs {
		if err := item.Validate(); err != nil {
			return err
		}
		if previousID != "" && item.ID <= previousID {
			return fmt.Errorf("artifact GC plan blobs must be uniquely sorted")
		}
		previousID = item.ID
		blobBytes += item.SizeBytes
	}
	if blobBytes != p.Summary.CandidateBlobBytes {
		return fmt.Errorf("artifact GC plan blob bytes are inconsistent")
	}
	var temporaryBytes int64
	previousPath := ""
	for _, item := range p.TemporaryFiles {
		if err := item.Validate(); err != nil {
			return err
		}
		if previousPath != "" && item.RelativePath <= previousPath {
			return fmt.Errorf("artifact GC plan temporary files must be uniquely sorted")
		}
		previousPath = item.RelativePath
		temporaryBytes += item.SizeBytes
	}
	if temporaryBytes != p.Summary.CandidateTemporaryBytes {
		return fmt.Errorf("artifact GC plan temporary bytes are inconsistent")
	}
	digest, err := planDigest(p)
	if err != nil {
		return err
	}
	if digest != p.Digest {
		return fmt.Errorf("artifact GC plan digest does not match canonical content")
	}
	return nil
}

func (b GCBlob) Validate() error {
	if !model.IsSHA256Digest(b.ID) || b.SizeBytes < 0 ||
		b.SizeBytes > model.MaxArtifactBytes || b.LastObservedAt.IsZero() ||
		!model.IsSHA256Digest(b.ClaimDigest) || !model.IsSHA256Digest(b.RecordDigest) {
		return fmt.Errorf("artifact GC blob identity is invalid")
	}
	if b.Legacy != (len(b.RetentionClasses) == 0) {
		return fmt.Errorf("artifact GC blob legacy classification is inconsistent")
	}
	previous := model.ArtifactRetentionClass("")
	for _, class := range b.RetentionClasses {
		if !class.IsKnown() || (previous != "" && class <= previous) {
			return fmt.Errorf("artifact GC blob retention classes must be uniquely sorted")
		}
		previous = class
	}
	digest, err := gcBlobDigest(b)
	if err != nil {
		return err
	}
	if digest != b.RecordDigest {
		return fmt.Errorf("artifact GC blob record digest does not match content")
	}
	return nil
}

func (t GCTemporaryFile) Validate() error {
	if t.RelativePath == "" || filepath.IsAbs(t.RelativePath) ||
		filepath.ToSlash(filepath.Clean(filepath.FromSlash(t.RelativePath))) != t.RelativePath ||
		strings.HasPrefix(t.RelativePath, "../") ||
		t.SizeBytes < 0 || t.SizeBytes > model.MaxArtifactBytes ||
		t.ModifiedAt.IsZero() || !model.IsSHA256Digest(t.RecordDigest) {
		return fmt.Errorf("artifact GC temporary file identity is invalid")
	}
	if !isArtifactTemporary(filepath.Base(filepath.FromSlash(t.RelativePath))) {
		return fmt.Errorf("artifact GC temporary file name is unsupported")
	}
	digest, err := gcTemporaryDigest(t)
	if err != nil {
		return err
	}
	if digest != t.RecordDigest {
		return fmt.Errorf("artifact GC temporary record digest does not match content")
	}
	return nil
}

func (r GCResult) Validate() error {
	if r.SchemaVersion != CurrentGCResultSchemaVersion ||
		!model.IsSHA256Digest(r.PlanDigest) ||
		r.DeletedBlobs < 0 || r.DeletedBlobBytes < 0 ||
		r.DeletedTemporaryFiles < 0 || r.DeletedTemporaryBytes < 0 {
		return fmt.Errorf("artifact GC result is invalid")
	}
	if r.DeletedBlobs > MaxStoreBlobs ||
		r.DeletedBlobBytes > int64(r.DeletedBlobs)*model.MaxArtifactBytes ||
		r.DeletedTemporaryFiles > MaxTemporaryFiles ||
		r.DeletedTemporaryBytes >
			int64(r.DeletedTemporaryFiles)*model.MaxArtifactBytes {
		return fmt.Errorf("artifact GC result exceeds supported bounds")
	}
	if r.AlreadyApplied && !r.Resumed {
		return fmt.Errorf("artifact GC already_applied requires resumed")
	}
	return nil
}

func (s GCSummary) validateBounds() error {
	if !countsWithinBounds(
		MaxStoreBlobs,
		s.TotalBlobs,
		s.ReferencedBlobs,
		s.ProtectedRecentBlobs,
		s.ProtectedAuditBlobs,
		s.ProtectedLegacyBlobs,
		s.CandidateBlobs,
	) ||
		!countsWithinBounds(
			MaxGCReferences,
			s.ReferencedOccurrences,
			s.ForeignReferences,
		) ||
		!countsWithinBounds(
			MaxTemporaryFiles,
			s.TemporaryFiles,
			s.ProtectedTemporaryFiles,
			s.CandidateTemporaryFiles,
		) ||
		s.TotalBytes < 0 ||
		s.TotalBytes > int64(MaxStoreBlobs)*model.MaxArtifactBytes ||
		s.CandidateBlobBytes < 0 ||
		s.CandidateBlobBytes > s.TotalBytes ||
		s.CandidateTemporaryBytes < 0 ||
		s.CandidateTemporaryBytes >
			int64(s.CandidateTemporaryFiles)*model.MaxArtifactBytes ||
		s.ReferencedOccurrences < s.ReferencedBlobs ||
		s.ForeignReferences > MaxGCReferences-s.ReferencedOccurrences {
		return fmt.Errorf("artifact GC plan accounting exceeds supported bounds")
	}
	return nil
}

func countsWithinBounds(maximum int, values ...int) bool {
	for _, value := range values {
		if value < 0 || value > maximum {
			return false
		}
	}
	return true
}

func validateLiveReferences(inventory storeInventory) error {
	for id, reference := range inventory.ReferenceIndex.ByID {
		blob, exists := inventory.BlobByID[id]
		if !exists {
			return fmt.Errorf("referenced artifact %s is missing from the store", id)
		}
		if blob.SizeBytes != reference.SizeBytes {
			return fmt.Errorf("referenced artifact %s size does not match the store", id)
		}
	}
	return nil
}

func requireCleanGCState(inventory storeInventory) error {
	state := gcState{
		operations: inventory.ActiveGCDigests,
		trash:      inventory.TrashGCDigests,
		pending:    inventory.PendingGCDigests,
	}
	return requireCleanGCStateValues(state)
}

func requireCleanGCStateValues(state gcState) error {
	if err := validateGCStateCardinality(state); err != nil {
		return err
	}
	if len(state.operations) > 0 {
		return pendingGCError("operation", state.operations[0])
	}
	if len(state.trash) > 0 {
		return pendingGCError("trash", state.trash[0])
	}
	if len(state.pending) > 0 {
		return pendingGCError("completed cleanup", state.pending[0])
	}
	return nil
}

func validateGCStateCardinality(state gcState) error {
	if len(state.operations) > 1 || len(state.trash) > 1 || len(state.pending) > 1 {
		return fmt.Errorf("artifact store has multiple concurrent GC operations")
	}
	if len(state.pending) > 0 && (len(state.operations) > 0 || len(state.trash) > 0) {
		return fmt.Errorf("artifact store has overlapping active and completed GC operations")
	}
	if len(state.trash) == 1 && len(state.operations) == 1 &&
		state.trash[0] != state.operations[0] {
		return fmt.Errorf("artifact store GC operation and trash identities differ")
	}
	if len(state.trash) == 1 && len(state.operations) == 0 {
		return fmt.Errorf("artifact store has orphaned GC trash %s", state.trash[0])
	}
	return nil
}

func pendingGCError(kind, digest string) error {
	return fmt.Errorf(
		"artifact store has pending GC %s %s; resume with the same policy, references, and confirmation digest",
		kind,
		digest,
	)
}

func validateResumeInputs(
	plan GCPlan,
	policy GCPolicy,
	inventory storeInventory,
	settings directorySettings,
) (bool, error) {
	if err := plan.Validate(); err != nil {
		return false, fmt.Errorf("validate pending artifact GC plan: %w", err)
	}
	if !reflect.DeepEqual(plan.Policy, policy) {
		return false, fmt.Errorf("pending artifact GC policy does not match the confirmed plan")
	}
	if plan.URIBase != settings.uriBase {
		return false, fmt.Errorf("pending artifact GC plan belongs to another URI base")
	}
	if inventory.Managed {
		if plan.StoreSchemaVersion != CurrentStoreSchemaVersion ||
			plan.LayoutVersion != CurrentLayoutVersion {
			return false, fmt.Errorf("pending artifact GC plan belongs to another store version")
		}
	} else if plan.StoreSchemaVersion != "" || plan.LayoutVersion != 0 {
		return false, fmt.Errorf("pending artifact GC plan does not match the legacy store")
	}
	if plan.ReferenceDigest == inventory.ReferenceIndex.Digest {
		return false, nil
	}
	for _, item := range plan.Blobs {
		if _, referenced := inventory.ReferenceIndex.ByID[item.ID]; referenced {
			return false, fmt.Errorf(
				"artifact references changed and now retain pending GC candidate %s",
				item.ID,
			)
		}
	}
	return true, nil
}

func inspectGCState(root string) (gcState, error) {
	gcRoot := filepath.Join(root, gcDirectoryName)
	if err := requireRealDirectory(gcRoot); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return gcState{}, nil
		}
		return gcState{}, fmt.Errorf("inspect artifact GC state: %w", err)
	}
	entries, err := os.ReadDir(gcRoot)
	if err != nil {
		return gcState{}, fmt.Errorf("read artifact GC state: %w", err)
	}
	allowed := map[string]struct{}{
		gcOperationsDirectory: {},
		gcTrashDirectory:      {},
		gcPendingDirectory:    {},
	}
	for _, entry := range entries {
		if _, ok := allowed[entry.Name()]; !ok || !entry.IsDir() {
			return gcState{}, fmt.Errorf("artifact GC contains unexpected entry %q", entry.Name())
		}
		if err := requireRealDirectory(filepath.Join(gcRoot, entry.Name())); err != nil {
			return gcState{}, err
		}
	}
	operations, err := readDigestDirectories(
		filepath.Join(gcRoot, gcOperationsDirectory),
		"artifact GC operations",
	)
	if err != nil {
		return gcState{}, err
	}
	trash, err := readDigestDirectories(
		filepath.Join(gcRoot, gcTrashDirectory),
		"artifact GC trash",
	)
	if err != nil {
		return gcState{}, err
	}
	pending, err := readDigestDirectories(
		filepath.Join(gcRoot, gcPendingDirectory),
		"artifact GC pending cleanup",
	)
	if err != nil {
		return gcState{}, err
	}
	return gcState{operations: operations, trash: trash, pending: pending}, nil
}

func readDigestDirectories(path, description string) ([]string, error) {
	if err := requireRealDirectory(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []string{}, nil
		}
		return nil, err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", description, err)
	}
	result := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !isLowerHex(entry.Name(), 64) {
			return nil, fmt.Errorf("%s contains unexpected entry %q", description, entry.Name())
		}
		if err := requireRealDirectory(filepath.Join(path, entry.Name())); err != nil {
			return nil, err
		}
		result = append(result, "sha256:"+entry.Name())
	}
	sort.Strings(result)
	return result, nil
}

func ensureGCBase(root string) (operations, trash, pending string, err error) {
	gcRoot, err := ensurePrivateChildDirectory(root, gcDirectoryName)
	if err != nil {
		return "", "", "", fmt.Errorf("create artifact GC directory: %w", err)
	}
	operations, err = ensurePrivateChildDirectory(gcRoot, gcOperationsDirectory)
	if err != nil {
		return "", "", "", fmt.Errorf("create artifact GC operations: %w", err)
	}
	trash, err = ensurePrivateChildDirectory(gcRoot, gcTrashDirectory)
	if err != nil {
		return "", "", "", fmt.Errorf("create artifact GC trash: %w", err)
	}
	pending, err = ensurePrivateChildDirectory(gcRoot, gcPendingDirectory)
	if err != nil {
		return "", "", "", fmt.Errorf("create artifact GC pending cleanup: %w", err)
	}
	return operations, trash, pending, nil
}

func createGCOperation(ctx context.Context, root string, plan GCPlan) error {
	if err := plan.Validate(); err != nil {
		return err
	}
	operations, trash, _, err := ensureGCBase(root)
	if err != nil {
		return err
	}
	hexDigest := strings.TrimPrefix(plan.Digest, "sha256:")
	operationPath, err := ensurePrivateChildDirectory(operations, hexDigest)
	if err != nil {
		return fmt.Errorf("create artifact GC operation: %w", err)
	}
	payload, err := marshalBoundedJSON(plan, maxGCPlanJSONBytes)
	if err != nil {
		return err
	}
	if err := writeImmutableFile(
		ctx,
		operationPath,
		filepath.Join(operationPath, gcPlanFileName),
		payload,
		maxGCPlanJSONBytes,
	); err != nil {
		return fmt.Errorf("persist artifact GC plan: %w", err)
	}
	if _, err := ensurePrivateChildDirectory(operationPath, gcProgressDirectory); err != nil {
		return fmt.Errorf("create artifact GC progress directory: %w", err)
	}
	if _, err := ensurePrivateChildDirectory(trash, hexDigest); err != nil {
		return fmt.Errorf("create artifact GC trash: %w", err)
	}
	return nil
}

func readGCOperation(root, digest string) (GCPlan, error) {
	plan, err := readJSONFile[GCPlan](
		filepath.Join(gcOperationPath(root, digest), gcPlanFileName),
		maxGCPlanJSONBytes,
	)
	if err != nil {
		return GCPlan{}, fmt.Errorf("read pending artifact GC plan: %w", err)
	}
	if err := plan.Validate(); err != nil {
		return GCPlan{}, fmt.Errorf("validate pending artifact GC plan: %w", err)
	}
	if plan.Digest != digest {
		return GCPlan{}, fmt.Errorf("pending artifact GC plan identity does not match directory")
	}
	return plan, nil
}

func readPendingGCPlan(root, digest string) (GCPlan, error) {
	plan, err := readJSONFile[GCPlan](
		filepath.Join(gcPendingPath(root, digest), gcPlanFileName),
		maxGCPlanJSONBytes,
	)
	if err != nil {
		return GCPlan{}, fmt.Errorf("read completed artifact GC plan: %w", err)
	}
	if err := plan.Validate(); err != nil {
		return GCPlan{}, err
	}
	completion, err := readJSONFile[gcCompletion](
		filepath.Join(gcPendingPath(root, digest), gcCompleteFileName),
		maxStoreJSONBytes,
	)
	if err != nil {
		return GCPlan{}, fmt.Errorf("read artifact GC completion: %w", err)
	}
	if err := validateGCCompletion(plan, completion); err != nil {
		return GCPlan{}, err
	}
	return plan, nil
}

func (s DirectoryStore) executeGCOperation(
	ctx context.Context,
	root string,
	plan GCPlan,
) error {
	for index, item := range plan.TemporaryFiles {
		if err := s.applyGCTemporary(ctx, root, plan.Digest, index, item); err != nil {
			return fmt.Errorf(
				"apply artifact GC temporary file %s: %w",
				item.RelativePath,
				err,
			)
		}
	}
	for index, item := range plan.Blobs {
		if err := s.applyGCBlob(ctx, root, plan.Digest, index, item); err != nil {
			return fmt.Errorf("apply artifact GC blob %s: %w", item.ID, err)
		}
	}
	if err := removePrivateTree(gcTrashPath(root, plan.Digest)); err != nil {
		return fmt.Errorf("remove artifact GC trash: %w", err)
	}
	cleanupEmptyArtifactDirectories(root, plan)

	operationPath := gcOperationPath(root, plan.Digest)
	completion := gcCompletion{
		SchemaVersion:         gcCompleteSchema,
		PlanDigest:            plan.Digest,
		DeletedBlobs:          len(plan.Blobs),
		DeletedTemporaryFiles: len(plan.TemporaryFiles),
	}
	payload, err := marshalBoundedJSON(completion, maxStoreJSONBytes)
	if err != nil {
		return err
	}
	if err := writeImmutableFile(
		ctx,
		operationPath,
		filepath.Join(operationPath, gcCompleteFileName),
		payload,
		maxStoreJSONBytes,
	); err != nil {
		return fmt.Errorf("persist artifact GC completion: %w", err)
	}
	if err := s.callGCHook(gcStepAfterComplete, len(plan.Blobs)+len(plan.TemporaryFiles)); err != nil {
		return err
	}
	_, _, pendingParent, err := ensureGCBase(root)
	if err != nil {
		return err
	}
	pendingPath := gcPendingPath(root, plan.Digest)
	if exists, err := privateDirectoryExists(pendingPath); err != nil {
		return err
	} else if exists {
		return fmt.Errorf("artifact GC pending-delete path already exists")
	}
	if err := os.Rename(operationPath, pendingPath); err != nil {
		return fmt.Errorf("finalize artifact GC operation: %w", err)
	}
	if err := syncDirectory(filepath.Dir(operationPath)); err != nil {
		return fmt.Errorf("sync artifact GC operations: %w", err)
	}
	if err := syncDirectory(pendingParent); err != nil {
		return fmt.Errorf("sync artifact GC pending cleanup: %w", err)
	}
	if err := s.callGCHook(gcStepAfterFinalizeRename, len(plan.Blobs)+len(plan.TemporaryFiles)); err != nil {
		return err
	}
	if err := removePrivateTree(pendingPath); err != nil {
		return fmt.Errorf("remove completed artifact GC operation: %w", err)
	}
	return nil
}

func (s DirectoryStore) applyGCBlob(
	ctx context.Context,
	root, planDigest string,
	index int,
	item GCBlob,
) error {
	expected := gcProgress{
		SchemaVersion: gcProgressSchema,
		PlanDigest:    planDigest,
		Kind:          "blob",
		Index:         index,
		Identity:      item.ID,
		RecordDigest:  item.RecordDigest,
	}
	markerPath := gcProgressPath(root, planDigest, "blob", index)
	marked, err := validateGCProgress(markerPath, expected)
	if err != nil {
		return err
	}
	hexDigest := strings.TrimPrefix(item.ID, "sha256:")
	sourceBlob := filepath.Join(root, blobDirectoryName, hexDigest[:2], hexDigest)
	targetBlob := filepath.Join(
		gcTrashPath(root, planDigest),
		"blobs",
		hexDigest[:2],
		hexDigest,
	)
	sourceClaims := filepath.Join(
		root,
		claimDirectoryName,
		blobDirectoryName,
		hexDigest[:2],
		hexDigest,
	)
	targetClaims := filepath.Join(
		gcTrashPath(root, planDigest),
		"claims",
		hexDigest[:2],
		hexDigest,
	)
	if marked {
		if exists, err := privateRegularFileExists(sourceBlob); err != nil {
			return err
		} else if exists {
			return fmt.Errorf("progress marker exists but active artifact blob remains")
		}
		if err := removePrivateFile(targetBlob); err != nil {
			return err
		}
		if err := removePrivateTree(targetClaims); err != nil {
			return err
		}
		return nil
	}

	if err := ensureGCBlobTargetParents(root, planDigest, hexDigest[:2]); err != nil {
		return err
	}
	sourceExists, err := privateRegularFileExists(sourceBlob)
	if err != nil {
		return err
	}
	targetExists, err := privateRegularFileExists(targetBlob)
	if err != nil {
		return err
	}
	if sourceExists && targetExists {
		return fmt.Errorf("artifact blob exists in active store and GC trash")
	}
	if !sourceExists && !targetExists {
		return fmt.Errorf("artifact blob is missing from active store and GC trash")
	}
	verifyBlob := sourceBlob
	if targetExists {
		verifyBlob = targetBlob
	}
	if err := validateGCBlobAt(ctx, verifyBlob, sourceClaims, targetClaims, item); err != nil {
		return err
	}
	if sourceExists {
		if err := os.Rename(sourceBlob, targetBlob); err != nil {
			return fmt.Errorf("move artifact blob to GC trash: %w", err)
		}
		if err := syncDirectory(filepath.Dir(sourceBlob)); err != nil {
			return err
		}
		if err := syncDirectory(filepath.Dir(targetBlob)); err != nil {
			return err
		}
		if err := s.callGCHook(gcStepAfterBlobRename, index); err != nil {
			return err
		}
	}
	if err := moveGCClaims(sourceClaims, targetClaims, item.Legacy); err != nil {
		return err
	}
	if err := s.callGCHook(gcStepAfterClaimRename, index); err != nil {
		return err
	}
	if err := writeGCProgress(ctx, root, planDigest, expected, markerPath); err != nil {
		return err
	}
	if err := s.callGCHook(gcStepAfterItemMarker, index); err != nil {
		return err
	}
	if err := removePrivateFile(targetBlob); err != nil {
		return err
	}
	if err := removePrivateTree(targetClaims); err != nil {
		return err
	}
	return nil
}

func (s DirectoryStore) applyGCTemporary(
	ctx context.Context,
	root, planDigest string,
	index int,
	item GCTemporaryFile,
) error {
	expected := gcProgress{
		SchemaVersion: gcProgressSchema,
		PlanDigest:    planDigest,
		Kind:          "temporary",
		Index:         index,
		Identity:      item.RelativePath,
		RecordDigest:  item.RecordDigest,
	}
	markerPath := gcProgressPath(root, planDigest, "temporary", index)
	marked, err := validateGCProgress(markerPath, expected)
	if err != nil {
		return err
	}
	relative := filepath.FromSlash(item.RelativePath)
	source := filepath.Join(root, relative)
	target := filepath.Join(gcTrashPath(root, planDigest), "temporary", relative)
	if marked {
		if exists, err := privateRegularFileExists(source); err != nil {
			return err
		} else if exists {
			return fmt.Errorf("progress marker exists but active temporary file remains")
		}
		return removePrivateFile(target)
	}
	if err := ensurePrivateParents(
		filepath.Join(gcTrashPath(root, planDigest), "temporary"),
		filepath.Dir(relative),
	); err != nil {
		return err
	}
	sourceExists, err := privateRegularFileExists(source)
	if err != nil {
		return err
	}
	targetExists, err := privateRegularFileExists(target)
	if err != nil {
		return err
	}
	if sourceExists && targetExists {
		return fmt.Errorf("temporary file exists in active store and GC trash")
	}
	if !sourceExists && !targetExists {
		return fmt.Errorf("temporary file is missing from active store and GC trash")
	}
	verifyPath := source
	if targetExists {
		verifyPath = target
	}
	if err := validateGCTemporaryAt(verifyPath, item); err != nil {
		return err
	}
	if sourceExists {
		if err := os.Rename(source, target); err != nil {
			return fmt.Errorf("move artifact temporary file to GC trash: %w", err)
		}
		if err := syncDirectory(filepath.Dir(source)); err != nil {
			return err
		}
		if err := syncDirectory(filepath.Dir(target)); err != nil {
			return err
		}
	}
	if err := writeGCProgress(ctx, root, planDigest, expected, markerPath); err != nil {
		return err
	}
	if err := s.callGCHook(gcStepAfterItemMarker, index); err != nil {
		return err
	}
	return removePrivateFile(target)
}

func validateGCBlobAt(
	ctx context.Context,
	blobPath, sourceClaims, targetClaims string,
	item GCBlob,
) error {
	hexDigest := strings.TrimPrefix(item.ID, "sha256:")
	state, err := inspectBlob(ctx, blobPath, hexDigest)
	if err != nil {
		return err
	}
	sourceExists, err := privateDirectoryExists(sourceClaims)
	if err != nil {
		return err
	}
	targetExists, err := privateDirectoryExists(targetClaims)
	if err != nil {
		return err
	}
	if sourceExists && targetExists {
		return fmt.Errorf("artifact claims exist in active store and GC trash")
	}
	if item.Legacy {
		if sourceExists || targetExists {
			return fmt.Errorf("legacy artifact unexpectedly has metadata claims")
		}
		state.Claims = nil
	} else {
		if !sourceExists && !targetExists {
			return fmt.Errorf("managed artifact claims are missing")
		}
		claimPath := sourceClaims
		if targetExists {
			claimPath = targetClaims
		}
		state.Claims, err = readClaimsAt(claimPath, item.ID, item.SizeBytes)
		if err != nil {
			return err
		}
	}
	state.ClaimDigest, err = digestClaims(state.Claims)
	if err != nil {
		return err
	}
	actual, err := newGCBlob(state)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(actual, item) {
		return fmt.Errorf("artifact blob content does not match confirmed GC plan")
	}
	return nil
}

func readClaimsAt(path, artifactID string, sizeBytes int64) ([]blobClaim, error) {
	if err := requireRealDirectory(path); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	if len(entries) > MaxClaimsPerBlob {
		return nil, fmt.Errorf(
			"artifact %s exceeds maximum claim count %d",
			artifactID,
			MaxClaimsPerBlob,
		)
	}
	claims := make([]blobClaim, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") ||
			!isLowerHex(strings.TrimSuffix(entry.Name(), ".json"), 64) {
			return nil, fmt.Errorf("artifact claims contain unexpected entry %q", entry.Name())
		}
		claim, err := readJSONFile[blobClaim](
			filepath.Join(path, entry.Name()),
			maxClaimJSONBytes,
		)
		if err != nil {
			return nil, err
		}
		if err := claim.validate(); err != nil {
			return nil, err
		}
		digest, _, err := claim.digest()
		if err != nil {
			return nil, err
		}
		if strings.TrimPrefix(digest, "sha256:")+".json" != entry.Name() {
			return nil, fmt.Errorf("artifact claim filename does not match content")
		}
		if claim.ArtifactID != artifactID || claim.SizeBytes != sizeBytes {
			return nil, fmt.Errorf("artifact claim identity does not match blob")
		}
		claims = append(claims, claim)
	}
	return claims, nil
}

func moveGCClaims(source, target string, legacy bool) error {
	sourceExists, err := privateDirectoryExists(source)
	if err != nil {
		return err
	}
	targetExists, err := privateDirectoryExists(target)
	if err != nil {
		return err
	}
	if legacy {
		if sourceExists || targetExists {
			return fmt.Errorf("legacy artifact unexpectedly has metadata claims")
		}
		return nil
	}
	if sourceExists && targetExists {
		return fmt.Errorf("artifact claims exist in active store and GC trash")
	}
	if !sourceExists && !targetExists {
		return fmt.Errorf("artifact claims are missing from active store and GC trash")
	}
	if targetExists {
		return nil
	}
	if err := requireRealDirectory(filepath.Dir(target)); err != nil {
		return err
	}
	if err := os.Rename(source, target); err != nil {
		return fmt.Errorf("move artifact claims to GC trash: %w", err)
	}
	if err := syncDirectory(filepath.Dir(source)); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(target))
}

func validateGCTemporaryAt(path string, item GCTemporaryFile) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() ||
		info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("artifact temporary file is not a private regular file")
	}
	actual := GCTemporaryFile{
		RelativePath: item.RelativePath,
		SizeBytes:    info.Size(),
		ModifiedAt:   info.ModTime().UTC().Round(0),
	}
	actual.RecordDigest, err = gcTemporaryDigest(actual)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(actual, item) {
		return fmt.Errorf("artifact temporary file does not match confirmed GC plan")
	}
	return nil
}

func ensureGCBlobTargetParents(root, digest, prefix string) error {
	trash := gcTrashPath(root, digest)
	blobs, err := ensurePrivateChildDirectory(trash, "blobs")
	if err != nil {
		return err
	}
	if _, err := ensurePrivateChildDirectory(blobs, prefix); err != nil {
		return err
	}
	claims, err := ensurePrivateChildDirectory(trash, "claims")
	if err != nil {
		return err
	}
	if _, err := ensurePrivateChildDirectory(claims, prefix); err != nil {
		return err
	}
	return nil
}

func ensurePrivateParents(base, relative string) error {
	if err := requireRealDirectory(base); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := ensureDirectory(base); err != nil {
			return err
		}
	}
	clean := filepath.Clean(relative)
	if clean == "." {
		return nil
	}
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("private directory path escapes its base")
	}
	current := base
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		var err error
		current, err = ensurePrivateChildDirectory(current, part)
		if err != nil {
			return err
		}
	}
	return nil
}

func writeGCProgress(
	ctx context.Context,
	root, planDigest string,
	progress gcProgress,
	path string,
) error {
	payload, err := marshalBoundedJSON(progress, maxStoreJSONBytes)
	if err != nil {
		return err
	}
	progressDir := filepath.Join(gcOperationPath(root, planDigest), gcProgressDirectory)
	if err := writeImmutableFile(ctx, progressDir, path, payload, maxStoreJSONBytes); err != nil {
		return fmt.Errorf("persist artifact GC progress: %w", err)
	}
	return nil
}

func validateGCProgress(path string, expected gcProgress) (bool, error) {
	actual, err := readJSONFile[gcProgress](path, maxStoreJSONBytes)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read artifact GC progress: %w", err)
	}
	if !reflect.DeepEqual(actual, expected) {
		return false, fmt.Errorf("artifact GC progress does not match confirmed plan")
	}
	return true, nil
}

func validateGCCompletion(plan GCPlan, completion gcCompletion) error {
	expected := gcCompletion{
		SchemaVersion:         gcCompleteSchema,
		PlanDigest:            plan.Digest,
		DeletedBlobs:          len(plan.Blobs),
		DeletedTemporaryFiles: len(plan.TemporaryFiles),
	}
	if !reflect.DeepEqual(completion, expected) {
		return fmt.Errorf("artifact GC completion does not match confirmed plan")
	}
	return nil
}

func cleanupEmptyArtifactDirectories(root string, plan GCPlan) {
	for _, item := range plan.Blobs {
		hexDigest := strings.TrimPrefix(item.ID, "sha256:")
		removeEmptyDirectory(filepath.Join(
			root,
			claimDirectoryName,
			blobDirectoryName,
			hexDigest[:2],
		))
		removeEmptyDirectory(filepath.Join(root, claimDirectoryName, blobDirectoryName))
		removeEmptyDirectory(filepath.Join(root, claimDirectoryName))
		removeEmptyDirectory(filepath.Join(root, blobDirectoryName, hexDigest[:2]))
	}
}

func removeEmptyDirectory(path string) {
	if err := os.Remove(path); err == nil {
		_ = syncDirectory(filepath.Dir(path))
	}
}

func removePrivateFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular non-symbolic-link file", path)
	}
	parent := filepath.Dir(path)
	if err := os.Remove(path); err != nil {
		return err
	}
	return syncDirectory(parent)
}

func removePrivateTree(path string) error {
	if err := requireRealDirectory(path); err != nil {
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

func privateRegularFileExists(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, fmt.Errorf("%s is not a regular non-symbolic-link file", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return false, fmt.Errorf("%s permissions %o are not private", path, info.Mode().Perm())
	}
	return true, nil
}

func privateDirectoryExists(path string) (bool, error) {
	err := requireRealDirectory(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func ensurePrivateChildDirectory(parent, name string) (string, error) {
	if filepath.Base(name) != name || name == "." || name == "" {
		return "", fmt.Errorf("invalid private child directory name %q", name)
	}
	if err := requireRealDirectory(parent); err != nil {
		return "", err
	}
	path := filepath.Join(parent, name)
	if err := os.Mkdir(path, 0o700); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return "", err
		}
		if err := requireRealDirectory(path); err != nil {
			return "", err
		}
		return path, nil
	}
	if err := syncDirectory(parent); err != nil {
		return "", err
	}
	return path, nil
}

func gcResult(
	plan GCPlan,
	resumed, alreadyApplied, referenceScopeChanged bool,
) GCResult {
	return GCResult{
		SchemaVersion:         CurrentGCResultSchemaVersion,
		PlanDigest:            plan.Digest,
		DeletedBlobs:          len(plan.Blobs),
		DeletedBlobBytes:      plan.Summary.CandidateBlobBytes,
		DeletedTemporaryFiles: len(plan.TemporaryFiles),
		DeletedTemporaryBytes: plan.Summary.CandidateTemporaryBytes,
		Resumed:               resumed,
		AlreadyApplied:        alreadyApplied,
		ReferenceScopeChanged: referenceScopeChanged,
	}
}

func (s DirectoryStore) callGCHook(step string, index int) error {
	if s.gcHook == nil {
		return nil
	}
	return s.gcHook(step, index)
}

func gcOperationPath(root, digest string) string {
	return filepath.Join(
		root,
		gcDirectoryName,
		gcOperationsDirectory,
		strings.TrimPrefix(digest, "sha256:"),
	)
}

func gcTrashPath(root, digest string) string {
	return filepath.Join(
		root,
		gcDirectoryName,
		gcTrashDirectory,
		strings.TrimPrefix(digest, "sha256:"),
	)
}

func gcPendingPath(root, digest string) string {
	return filepath.Join(
		root,
		gcDirectoryName,
		gcPendingDirectory,
		strings.TrimPrefix(digest, "sha256:"),
	)
}

func gcProgressPath(root, digest, kind string, index int) string {
	return filepath.Join(
		gcOperationPath(root, digest),
		gcProgressDirectory,
		fmt.Sprintf("%s-%06d.json", kind, index),
	)
}
