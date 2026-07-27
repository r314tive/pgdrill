package artifact

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/r314tive/pgdrill/internal/filelock"
	"github.com/r314tive/pgdrill/internal/model"
)

type VerificationResult struct {
	SchemaVersion        string   `json:"schema_version"`
	StoreSchemaVersion   string   `json:"store_schema_version,omitempty"`
	LayoutVersion        int      `json:"layout_version,omitempty"`
	MigrationRequired    bool     `json:"migration_required"`
	URIBase              string   `json:"uri_base"`
	Blobs                int      `json:"blobs"`
	BlobBytes            int64    `json:"blob_bytes"`
	ManagedBlobs         int      `json:"managed_blobs"`
	LegacyBlobs          int      `json:"legacy_blobs"`
	AuditClassifiedBlobs int      `json:"audit_classified_blobs"`
	ReferencedBlobs      int      `json:"referenced_blobs"`
	UnreferencedBlobs    int      `json:"unreferenced_blobs"`
	ReferenceOccurrences int      `json:"reference_occurrences"`
	ForeignReferences    int      `json:"foreign_references"`
	ReferenceDigest      string   `json:"reference_digest"`
	TemporaryFiles       int      `json:"temporary_files"`
	PendingGCOperations  []string `json:"pending_gc_operations"`
	PendingGCCleanup     []string `json:"pending_gc_cleanup"`
	MaintenanceRequired  bool     `json:"maintenance_required"`
}

func (s DirectoryStore) Verify(
	ctx context.Context,
	references []model.ArtifactRef,
) (VerificationResult, error) {
	settings, err := s.settings()
	if err != nil {
		return VerificationResult{}, err
	}
	result := VerificationResult{
		SchemaVersion:       CurrentVerificationSchemaVersion,
		URIBase:             settings.uriBase,
		PendingGCOperations: []string{},
		PendingGCCleanup:    []string{},
	}
	err = withStoreLock(ctx, settings.root, filelock.Shared, false, func() error {
		inventory, err := scanStore(ctx, settings, references)
		if err != nil {
			return err
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
		allowedOrphans := map[string]struct{}{}
		for _, digest := range state.operations {
			plan, err := validateGCOperationState(
				ctx,
				settings.root,
				digest,
				containsDigest(state.trash, digest),
			)
			if err != nil {
				return err
			}
			for _, item := range plan.Blobs {
				allowedOrphans[item.ID] = struct{}{}
			}
		}
		for _, digest := range state.pending {
			if _, err := readPendingGCPlan(settings.root, digest); err != nil {
				return err
			}
		}
		for _, id := range inventory.OrphanClaimIDs {
			if _, ok := allowedOrphans[id]; !ok {
				return fmt.Errorf("artifact store has claims without blob %s", id)
			}
		}
		if inventory.Managed {
			result.StoreSchemaVersion = inventory.StoreMetadata.SchemaVersion
			result.LayoutVersion = inventory.StoreMetadata.LayoutVersion
			result.MigrationRequired =
				inventory.StoreMetadata.SchemaVersion != CurrentStoreSchemaVersion
		}
		result.Blobs = len(inventory.Blobs)
		for _, blob := range inventory.Blobs {
			result.BlobBytes += blob.SizeBytes
			if blob.legacy() {
				result.LegacyBlobs++
			} else {
				result.ManagedBlobs++
			}
			if blob.hasRetention(model.ArtifactRetentionAudit) {
				result.AuditClassifiedBlobs++
			}
			if _, ok := inventory.ReferenceIndex.ByID[blob.ID]; ok {
				result.ReferencedBlobs++
			} else {
				result.UnreferencedBlobs++
			}
		}
		result.ReferenceOccurrences = inventory.ReferenceIndex.Occurrences
		result.ForeignReferences = inventory.ReferenceIndex.Foreign
		result.ReferenceDigest = inventory.ReferenceIndex.Digest
		result.TemporaryFiles = len(inventory.TemporaryFiles)
		result.PendingGCOperations = append([]string{}, state.operations...)
		result.PendingGCCleanup = append([]string{}, state.pending...)
		result.MaintenanceRequired = result.TemporaryFiles > 0 ||
			len(state.operations) > 0 ||
			len(state.pending) > 0
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

func (v VerificationResult) Validate() error {
	if v.SchemaVersion != CurrentVerificationSchemaVersion ||
		strings.TrimSpace(v.URIBase) == "" ||
		!model.IsSHA256Digest(v.ReferenceDigest) {
		return fmt.Errorf("artifact verification identity is invalid")
	}
	if v.StoreSchemaVersion == "" {
		if v.LayoutVersion != 0 {
			return fmt.Errorf("legacy artifact verification must not declare a layout version")
		}
	} else if (v.StoreSchemaVersion != CurrentStoreSchemaVersion &&
		v.StoreSchemaVersion != LegacyStoreSchemaVersion) ||
		v.LayoutVersion != CurrentLayoutVersion {
		return fmt.Errorf("artifact verification store version is unsupported")
	}
	if v.MigrationRequired != (v.StoreSchemaVersion == LegacyStoreSchemaVersion) {
		return fmt.Errorf("artifact verification migration state is inconsistent")
	}
	if v.Blobs < 0 || v.Blobs > MaxStoreBlobs || v.BlobBytes < 0 ||
		v.ManagedBlobs < 0 || v.LegacyBlobs < 0 ||
		v.ManagedBlobs+v.LegacyBlobs != v.Blobs ||
		v.AuditClassifiedBlobs < 0 || v.AuditClassifiedBlobs > v.ManagedBlobs ||
		v.ReferencedBlobs < 0 || v.UnreferencedBlobs < 0 ||
		v.ReferencedBlobs+v.UnreferencedBlobs != v.Blobs ||
		v.ReferenceOccurrences < 0 || v.ReferenceOccurrences > MaxGCReferences ||
		v.ForeignReferences < 0 || v.ForeignReferences > MaxGCReferences ||
		v.TemporaryFiles < 0 || v.TemporaryFiles > MaxTemporaryFiles {
		return fmt.Errorf("artifact verification counts are inconsistent")
	}
	if len(v.PendingGCOperations) > 1 || len(v.PendingGCCleanup) > 1 {
		return fmt.Errorf("artifact verification has multiple pending GC operations")
	}
	for _, digest := range append(
		append([]string{}, v.PendingGCOperations...),
		v.PendingGCCleanup...,
	) {
		if !model.IsSHA256Digest(digest) {
			return fmt.Errorf("artifact verification contains an invalid GC digest")
		}
	}
	wantMaintenance := v.TemporaryFiles > 0 ||
		len(v.PendingGCOperations) > 0 ||
		len(v.PendingGCCleanup) > 0
	if v.MaintenanceRequired != wantMaintenance {
		return fmt.Errorf("artifact verification maintenance state is inconsistent")
	}
	return nil
}

func validateGCOperationState(
	ctx context.Context,
	root, digest string,
	hasTrash bool,
) (GCPlan, error) {
	plan, err := readGCOperation(root, digest)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && !hasTrash {
			return GCPlan{}, nil
		}
		return GCPlan{}, err
	}
	completion, err := readJSONFile[gcCompletion](
		filepath.Join(gcOperationPath(root, digest), gcCompleteFileName),
		maxStoreJSONBytes,
	)
	if err == nil {
		if err := validateGCCompletion(plan, completion); err != nil {
			return GCPlan{}, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return GCPlan{}, fmt.Errorf("read artifact GC completion: %w", err)
	}

	progressDir := filepath.Join(gcOperationPath(root, digest), gcProgressDirectory)
	if err := requireRealDirectory(progressDir); err != nil {
		if errors.Is(err, os.ErrNotExist) && !hasTrash {
			return plan, nil
		}
		return GCPlan{}, fmt.Errorf("inspect artifact GC progress: %w", err)
	}
	entries, err := os.ReadDir(progressDir)
	if err != nil {
		return GCPlan{}, err
	}
	expected := make(map[string]gcProgress, len(plan.Blobs)+len(plan.TemporaryFiles))
	for index, item := range plan.Blobs {
		name := filepath.Base(gcProgressPath(root, digest, "blob", index))
		expected[name] = gcProgress{
			SchemaVersion: gcProgressSchema,
			PlanDigest:    digest,
			Kind:          "blob",
			Index:         index,
			Identity:      item.ID,
			RecordDigest:  item.RecordDigest,
		}
	}
	for index, item := range plan.TemporaryFiles {
		name := filepath.Base(gcProgressPath(root, digest, "temporary", index))
		expected[name] = gcProgress{
			SchemaVersion: gcProgressSchema,
			PlanDigest:    digest,
			Kind:          "temporary",
			Index:         index,
			Identity:      item.RelativePath,
			RecordDigest:  item.RecordDigest,
		}
	}
	marked := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		want, ok := expected[entry.Name()]
		if !ok || entry.IsDir() {
			return GCPlan{}, fmt.Errorf("artifact GC progress contains unexpected entry %q", entry.Name())
		}
		actual, err := readJSONFile[gcProgress](
			filepath.Join(progressDir, entry.Name()),
			maxStoreJSONBytes,
		)
		if err != nil {
			return GCPlan{}, err
		}
		if !reflect.DeepEqual(actual, want) {
			return GCPlan{}, fmt.Errorf("artifact GC progress %q does not match plan", entry.Name())
		}
		marked[entry.Name()] = struct{}{}
	}
	for index, item := range plan.Blobs {
		if err := validateGCBlobOperationState(
			ctx,
			root,
			digest,
			index,
			item,
			marked,
		); err != nil {
			return GCPlan{}, err
		}
	}
	for index, item := range plan.TemporaryFiles {
		if err := validateGCTemporaryOperationState(
			root,
			digest,
			index,
			item,
			marked,
		); err != nil {
			return GCPlan{}, err
		}
	}
	return plan, nil
}

func validateGCBlobOperationState(
	ctx context.Context,
	root, digest string,
	index int,
	item GCBlob,
	marked map[string]struct{},
) error {
	name := filepath.Base(gcProgressPath(root, digest, "blob", index))
	_, progressExists := marked[name]
	hexDigest := strings.TrimPrefix(item.ID, "sha256:")
	sourceBlob := filepath.Join(root, blobDirectoryName, hexDigest[:2], hexDigest)
	targetBlob := filepath.Join(gcTrashPath(root, digest), "blobs", hexDigest[:2], hexDigest)
	sourceClaims := filepath.Join(
		root,
		claimDirectoryName,
		blobDirectoryName,
		hexDigest[:2],
		hexDigest,
	)
	targetClaims := filepath.Join(gcTrashPath(root, digest), "claims", hexDigest[:2], hexDigest)
	sourceExists, err := privateRegularFileExists(sourceBlob)
	if err != nil {
		return err
	}
	targetExists, err := privateRegularFileExists(targetBlob)
	if err != nil {
		return err
	}
	if sourceExists && targetExists {
		return fmt.Errorf("artifact GC blob %s exists in active state and trash", item.ID)
	}
	if progressExists {
		if sourceExists {
			return fmt.Errorf("artifact GC blob %s is marked but remains active", item.ID)
		}
		return nil
	}
	if !sourceExists && !targetExists {
		return fmt.Errorf("artifact GC blob %s disappeared before progress", item.ID)
	}
	verifyPath := sourceBlob
	if targetExists {
		verifyPath = targetBlob
	}
	return validateGCBlobAt(ctx, verifyPath, sourceClaims, targetClaims, item)
}

func validateGCTemporaryOperationState(
	root, digest string,
	index int,
	item GCTemporaryFile,
	marked map[string]struct{},
) error {
	name := filepath.Base(gcProgressPath(root, digest, "temporary", index))
	_, progressExists := marked[name]
	relative := filepath.FromSlash(item.RelativePath)
	source := filepath.Join(root, relative)
	target := filepath.Join(gcTrashPath(root, digest), "temporary", relative)
	sourceExists, err := privateRegularFileExists(source)
	if err != nil {
		return err
	}
	targetExists, err := privateRegularFileExists(target)
	if err != nil {
		return err
	}
	if sourceExists && targetExists {
		return fmt.Errorf("artifact GC temporary %s exists in active state and trash", item.RelativePath)
	}
	if progressExists {
		if sourceExists {
			return fmt.Errorf("artifact GC temporary %s is marked but remains active", item.RelativePath)
		}
		return nil
	}
	if !sourceExists && !targetExists {
		return fmt.Errorf(
			"artifact GC temporary %s disappeared before progress",
			item.RelativePath,
		)
	}
	verifyPath := source
	if targetExists {
		verifyPath = target
	}
	return validateGCTemporaryAt(root, verifyPath, item)
}

func containsDigest(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
