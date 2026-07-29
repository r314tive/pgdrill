package artifact

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
)

func TestDirectoryStorePersistsClaimsAndVerifiesReferenceSet(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "report.json.artifacts")
	store := DirectoryStore{Path: root}
	historyMetadata := artifactMetadata(
		t,
		model.ArtifactRetentionHistory,
		model.ArtifactRedactionNotRequired,
	)
	auditMetadata := artifactMetadata(
		t,
		model.ArtifactRetentionAudit,
		model.ArtifactRedactionNotRequired,
	)
	historyRef, err := store.Put(
		context.Background(),
		historyMetadata,
		strings.NewReader("same-content"),
	)
	if err != nil {
		t.Fatal(err)
	}
	auditRef, err := store.Put(
		context.Background(),
		auditMetadata,
		strings.NewReader("same-content"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if historyRef.ID != auditRef.ID || historyRef.RetentionClass == auditRef.RetentionClass {
		t.Fatalf("deduplicated references = %#v, %#v", historyRef, auditRef)
	}

	foreign := historyRef
	foreign.URI = "another-store/sha256/" +
		strings.TrimPrefix(foreign.ID, "sha256:")[:2] + "/" +
		strings.TrimPrefix(foreign.ID, "sha256:")
	verification, err := store.Verify(
		context.Background(),
		[]model.ArtifactRef{historyRef, auditRef, foreign},
	)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if verification.Blobs != 1 ||
		verification.ManagedBlobs != 1 ||
		verification.AuditClassifiedBlobs != 1 ||
		verification.ReferencedBlobs != 1 ||
		verification.ReferenceOccurrences != 2 ||
		verification.ForeignReferences != 1 ||
		verification.MaintenanceRequired {
		t.Fatalf("verification = %#v", verification)
	}
	if err := verification.Validate(); err != nil {
		t.Fatalf("verification validation = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, storeMetadataFileName)); err != nil {
		t.Fatalf("stat store metadata: %v", err)
	}
}

func TestEnsurePrivateParentsRejectsEscapesAndSymbolicLinks(t *testing.T) {
	t.Run("canonical descendants", func(t *testing.T) {
		base := filepath.Join(t.TempDir(), "trash")
		if err := ensurePrivateParents(base, filepath.Join("claims", "ab")); err != nil {
			t.Fatalf("ensurePrivateParents() error = %v", err)
		}
		for _, path := range []string{
			base,
			filepath.Join(base, "claims"),
			filepath.Join(base, "claims", "ab"),
		} {
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
				info.Mode().Perm()&0o077 != 0 {
				t.Fatalf("private parent %s has mode %s", path, info.Mode())
			}
		}
	})

	t.Run("path escape", func(t *testing.T) {
		base := filepath.Join(t.TempDir(), "trash")
		if err := ensurePrivateParents(base, filepath.Join("..", "outside")); err == nil ||
			!strings.Contains(err.Error(), "escapes") {
			t.Fatalf("ensurePrivateParents(path escape) error = %v", err)
		}
	})

	t.Run("symbolic link", func(t *testing.T) {
		root := t.TempDir()
		base := filepath.Join(root, "trash")
		if err := os.Mkdir(base, 0o700); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(root, "outside")
		if err := os.Mkdir(outside, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(base, "claims")); err != nil {
			t.Skipf("create symbolic link: %v", err)
		}
		if err := ensurePrivateParents(base, filepath.Join("claims", "ab")); err == nil ||
			!strings.Contains(err.Error(), "not a real directory") {
			t.Fatalf("ensurePrivateParents(symbolic link) error = %v", err)
		}
		if _, err := os.Lstat(filepath.Join(outside, "ab")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("symbolic-link target was modified: %v", err)
		}
	})
}

func TestValidateGCTemporaryOperationState(t *testing.T) {
	const (
		digest       = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		relativePath = ".artifact-recovery.tmp"
	)
	tests := []struct {
		name      string
		source    bool
		trash     bool
		progress  bool
		wantError string
	}{
		{name: "active before progress", source: true},
		{name: "trash before progress", trash: true},
		{
			name:      "active and trash conflict",
			source:    true,
			trash:     true,
			wantError: "active state and trash",
		},
		{name: "missing before progress", wantError: "disappeared before progress"},
		{
			name:      "marked but active",
			source:    true,
			progress:  true,
			wantError: "marked but remains active",
		},
		{name: "marked and absent", progress: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, filepath.FromSlash(relativePath))
			trash := filepath.Join(
				gcTrashPath(root, digest),
				"temporary",
				filepath.FromSlash(relativePath),
			)
			modifiedAt := time.Date(2026, 7, 29, 1, 2, 3, 0, time.UTC)
			for path, create := range map[string]bool{
				source: test.source,
				trash:  test.trash,
			} {
				if !create {
					continue
				}
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("temporary"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(path, modifiedAt, modifiedAt); err != nil {
					t.Fatal(err)
				}
			}
			if test.source || test.trash {
				path := source
				if !test.source {
					path = trash
				}
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				modifiedAt = info.ModTime().UTC().Round(0)
			}
			item, err := newGCTemporaryFile(temporaryState{
				RelativePath: relativePath,
				SizeBytes:    int64(len("temporary")),
				ModifiedAt:   modifiedAt,
			})
			if err != nil {
				t.Fatal(err)
			}
			marked := map[string]struct{}{}
			if test.progress {
				name := filepath.Base(gcProgressPath(root, digest, "temporary", 0))
				marked[name] = struct{}{}
			}

			err = validateGCTemporaryOperationState(
				root,
				digest,
				0,
				item,
				marked,
			)
			if test.wantError == "" {
				if err != nil {
					t.Fatalf("validateGCTemporaryOperationState() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf(
					"validateGCTemporaryOperationState() error = %v, want %q",
					err,
					test.wantError,
				)
			}
		})
	}
}

func TestGCPlanValidateRejectsOutOfBoundsAccounting(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "artifacts")
	store := DirectoryStore{Path: root}
	ref := putArtifact(
		t,
		store,
		artifactMetadata(t, model.ArtifactRetentionHistory, model.ArtifactRedactionNotRequired),
		"orphan",
	)
	setBlobTime(t, root, ref.ID, time.Now().UTC().Add(-4*time.Hour))
	plan, err := store.PlanGC(
		context.Background(),
		GCPolicy{Before: time.Now().UTC().Add(-time.Hour)},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(*GCPlan)
	}{
		{
			name: "candidate bytes exceed total",
			mutate: func(plan *GCPlan) {
				plan.Summary.TotalBytes = plan.Summary.CandidateBlobBytes - 1
			},
		},
		{
			name: "reference scope exceeds limit",
			mutate: func(plan *GCPlan) {
				plan.Summary.ReferencedOccurrences = MaxGCReferences
				plan.Summary.ForeignReferences = 1
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			broken := plan
			test.mutate(&broken)
			digest, err := planDigest(broken)
			if err != nil {
				t.Fatal(err)
			}
			broken.Digest = digest
			if err := broken.Validate(); err == nil ||
				!strings.Contains(err.Error(), "supported bounds") {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestGCResultValidateRejectsOutOfBoundsAccounting(t *testing.T) {
	t.Parallel()

	result := GCResult{
		SchemaVersion: CurrentGCResultSchemaVersion,
		PlanDigest:    "sha256:" + strings.Repeat("a", 64),
	}
	tests := []struct {
		name   string
		mutate func(*GCResult)
	}{
		{
			name: "blob bytes without blobs",
			mutate: func(result *GCResult) {
				result.DeletedBlobBytes = 1
			},
		},
		{
			name: "too many temporary files",
			mutate: func(result *GCResult) {
				result.DeletedTemporaryFiles = MaxTemporaryFiles + 1
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			broken := result
			test.mutate(&broken)
			if err := broken.Validate(); err == nil ||
				!strings.Contains(err.Error(), "supported bounds") {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestVerificationResultValidateRejectsOutOfBoundsAccounting(t *testing.T) {
	t.Parallel()

	result := VerificationResult{
		SchemaVersion:        CurrentVerificationSchemaVersion,
		StoreSchemaVersion:   CurrentStoreSchemaVersion,
		LayoutVersion:        CurrentLayoutVersion,
		URIBase:              "artifacts",
		Blobs:                1,
		BlobBytes:            1,
		ManagedBlobs:         1,
		ReferencedBlobs:      1,
		ReferenceOccurrences: 1,
		ReferenceDigest:      "sha256:" + strings.Repeat("a", 64),
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("valid verification result: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*VerificationResult)
		want   string
	}{
		{
			name: "blob bytes exceed content bound",
			mutate: func(result *VerificationResult) {
				result.BlobBytes = model.MaxArtifactBytes + 1
			},
			want: "counts are inconsistent",
		},
		{
			name: "reference scope exceeds limit",
			mutate: func(result *VerificationResult) {
				result.ReferenceOccurrences = MaxGCReferences
				result.ForeignReferences = 1
			},
			want: "counts are inconsistent",
		},
		{
			name: "maintenance states overlap",
			mutate: func(result *VerificationResult) {
				digest := "sha256:" + strings.Repeat("b", 64)
				result.PendingGCOperations = []string{digest}
				result.PendingGCCleanup = []string{digest}
				result.MaintenanceRequired = true
			},
			want: "overlapping",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			broken := result
			test.mutate(&broken)
			if err := broken.Validate(); err == nil ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestReadClaimsAtRejectsExcessiveClaimCount(t *testing.T) {
	path := t.TempDir()
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	artifactID := "sha256:" + strings.Repeat("a", 64)
	for index := 0; index <= MaxClaimsPerBlob; index++ {
		name := fmt.Sprintf("%064x.json", index)
		if err := os.WriteFile(filepath.Join(path, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := readClaimsAt(path, artifactID, 1); err == nil ||
		!strings.Contains(err.Error(), "maximum claim count") {
		t.Fatalf("readClaimsAt() error = %v", err)
	}
}

func TestDirectoryStoreGCProtectsLiveRecentAuditAndLegacyBlobs(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "artifacts")
	store := DirectoryStore{Path: root}
	historyMetadata := artifactMetadata(
		t,
		model.ArtifactRetentionHistory,
		model.ArtifactRedactionNotRequired,
	)
	auditMetadata := artifactMetadata(
		t,
		model.ArtifactRetentionAudit,
		model.ArtifactRedactionRedacted,
	)
	live := putArtifact(t, store, historyMetadata, "live")
	orphan := putArtifact(t, store, historyMetadata, "orphan")
	audit := putArtifact(t, store, auditMetadata, "audit")
	recent := putArtifact(t, store, historyMetadata, "recent")
	legacy := writeLegacyBlob(t, root, "legacy")

	now := time.Now().UTC().Round(0)
	old := now.Add(-2 * time.Hour)
	for _, ref := range []model.ArtifactRef{live, orphan, audit} {
		setBlobTime(t, root, ref.ID, old)
	}
	setBlobTime(t, root, legacy.ID, old)
	cutoff := now.Add(-time.Hour)

	policy := GCPolicy{Before: cutoff}
	plan, err := store.PlanGC(context.Background(), policy, []model.ArtifactRef{live})
	if err != nil {
		t.Fatalf("PlanGC() error = %v", err)
	}
	if len(plan.Blobs) != 1 || plan.Blobs[0].ID != orphan.ID {
		t.Fatalf("GC candidates = %#v", plan.Blobs)
	}
	if plan.Summary.ReferencedBlobs != 1 ||
		plan.Summary.ProtectedRecentBlobs != 1 ||
		plan.Summary.ProtectedAuditBlobs != 1 ||
		plan.Summary.ProtectedLegacyBlobs != 1 {
		t.Fatalf("GC summary = %#v", plan.Summary)
	}
	if _, err := store.Read(context.Background(), orphan); err != nil {
		t.Fatalf("dry-run removed orphan: %v", err)
	}

	result, err := store.ApplyGC(
		context.Background(),
		policy,
		[]model.ArtifactRef{live},
		plan.Digest,
	)
	if err != nil {
		t.Fatalf("ApplyGC() error = %v", err)
	}
	if result.DeletedBlobs != 1 || result.DeletedBlobBytes != orphan.SizeBytes ||
		result.Resumed || result.AlreadyApplied {
		t.Fatalf("GC result = %#v", result)
	}
	if _, err := store.Read(context.Background(), orphan); err == nil {
		t.Fatal("deleted orphan remains readable")
	}
	for _, ref := range []model.ArtifactRef{live, audit, recent, legacy} {
		if _, err := store.Read(context.Background(), ref); err != nil {
			t.Fatalf("protected artifact %s is unreadable: %v", ref.ID, err)
		}
	}
	verification, err := store.Verify(context.Background(), []model.ArtifactRef{live})
	if err != nil {
		t.Fatal(err)
	}
	if verification.Blobs != 4 || verification.UnreferencedBlobs != 3 ||
		verification.LegacyBlobs != 1 || verification.MaintenanceRequired {
		t.Fatalf("verification after GC = %#v", verification)
	}
}

func TestDirectoryStoreGCIncludesExplicitAuditLegacyAndTemporaryScope(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "artifacts")
	store := DirectoryStore{Path: root}
	audit := putArtifact(
		t,
		store,
		artifactMetadata(t, model.ArtifactRetentionAudit, model.ArtifactRedactionRedacted),
		"audit",
	)
	legacy := writeLegacyBlob(t, root, "legacy")
	temporaryPath := filepath.Join(root, ".artifact-crash.tmp")
	if err := os.WriteFile(temporaryPath, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-4 * time.Hour)
	setBlobTime(t, root, audit.ID, old)
	setBlobTime(t, root, legacy.ID, old)
	if err := os.Chtimes(temporaryPath, old, old); err != nil {
		t.Fatal(err)
	}
	policy := GCPolicy{
		Before:           time.Now().UTC().Add(-time.Hour),
		IncludeAudit:     true,
		IncludeLegacy:    true,
		IncludeTemporary: true,
	}
	plan, err := store.PlanGC(context.Background(), policy, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Blobs) != 2 || len(plan.TemporaryFiles) != 1 {
		t.Fatalf("plan = %#v", plan)
	}
	result, err := store.ApplyGC(context.Background(), policy, nil, plan.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if result.DeletedBlobs != 2 || result.DeletedTemporaryFiles != 1 {
		t.Fatalf("result = %#v", result)
	}
	verification, err := store.Verify(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if verification.Blobs != 0 || verification.TemporaryFiles != 0 ||
		verification.MaintenanceRequired {
		t.Fatalf("verification = %#v", verification)
	}
}

func TestDirectoryStoreGCRejectsStaleConfirmationAndMissingReference(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "artifacts")
	store := DirectoryStore{Path: root}
	metadata := artifactMetadata(
		t,
		model.ArtifactRetentionHistory,
		model.ArtifactRedactionNotRequired,
	)
	first := putArtifact(t, store, metadata, "first")
	old := time.Now().UTC().Add(-4 * time.Hour)
	setBlobTime(t, root, first.ID, old)
	policy := GCPolicy{Before: time.Now().UTC().Add(-time.Hour)}
	plan, err := store.PlanGC(context.Background(), policy, nil)
	if err != nil {
		t.Fatal(err)
	}
	second := putArtifact(t, store, metadata, "second")
	setBlobTime(t, root, second.ID, old)
	if _, err := store.ApplyGC(context.Background(), policy, nil, plan.Digest); err == nil ||
		!strings.Contains(err.Error(), "stale") {
		t.Fatalf("ApplyGC(stale) error = %v", err)
	}

	missing := first
	missing.ID = "sha256:" + strings.Repeat("f", 64)
	missing.URI = filepath.Base(root) + "/sha256/ff/" + strings.Repeat("f", 64)
	if _, err := store.Verify(context.Background(), []model.ArtifactRef{missing}); err == nil ||
		!strings.Contains(err.Error(), "missing") {
		t.Fatalf("Verify(missing) error = %v", err)
	}
}

func TestDirectoryStoreGCResumesAfterBlobRename(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "artifacts")
	base := DirectoryStore{Path: root}
	ref := putArtifact(
		t,
		base,
		artifactMetadata(t, model.ArtifactRetentionHistory, model.ArtifactRedactionNotRequired),
		"orphan",
	)
	recent := putArtifact(
		t,
		base,
		artifactMetadata(t, model.ArtifactRetentionHistory, model.ArtifactRedactionNotRequired),
		"recent",
	)
	setBlobTime(t, root, ref.ID, time.Now().UTC().Add(-4*time.Hour))
	policy := GCPolicy{Before: time.Now().UTC().Add(-time.Hour)}
	plan, err := base.PlanGC(context.Background(), policy, nil)
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected process loss")
	interrupted := DirectoryStore{
		Path: root,
		gcHook: func(step string, _ int) error {
			if step == gcStepAfterBlobRename {
				return injected
			}
			return nil
		},
	}
	if _, err := interrupted.ApplyGC(context.Background(), policy, nil, plan.Digest); !errors.Is(err, injected) {
		t.Fatalf("ApplyGC(interrupted) error = %v", err)
	}
	verification, err := base.Verify(context.Background(), nil)
	if err != nil {
		t.Fatalf("Verify(interrupted) error = %v", err)
	}
	if !verification.MaintenanceRequired ||
		len(verification.PendingGCOperations) != 1 ||
		verification.PendingGCOperations[0] != plan.Digest {
		t.Fatalf("interrupted verification = %#v", verification)
	}
	if _, err := base.Put(
		context.Background(),
		artifactMetadata(t, model.ArtifactRetentionHistory, model.ArtifactRedactionNotRequired),
		strings.NewReader("blocked-while-pending"),
	); err == nil || !strings.Contains(err.Error(), "pending GC") {
		t.Fatalf("Put(pending GC) error = %v", err)
	}
	result, err := base.ApplyGC(
		context.Background(),
		policy,
		[]model.ArtifactRef{recent},
		plan.Digest,
	)
	if err != nil {
		t.Fatalf("ApplyGC(resume) error = %v", err)
	}
	if !result.Resumed || result.AlreadyApplied || result.DeletedBlobs != 1 ||
		!result.ReferenceScopeChanged {
		t.Fatalf("resumed result = %#v", result)
	}
}

func TestDirectoryStoreGCRecoversInitializationCrashPrefixes(t *testing.T) {
	tests := []struct {
		name         string
		persistPlan  bool
		makeProgress bool
		makePlanTemp bool
	}{
		{name: "operation directory only"},
		{name: "metadata temporary file before plan", makePlanTemp: true},
		{name: "plan without progress or trash", persistPlan: true},
		{name: "plan and progress without trash", persistPlan: true, makeProgress: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "artifacts")
			store := DirectoryStore{Path: root}
			ref := putArtifact(
				t,
				store,
				artifactMetadata(
					t,
					model.ArtifactRetentionHistory,
					model.ArtifactRedactionNotRequired,
				),
				"initialization-crash",
			)
			setBlobTime(t, root, ref.ID, time.Now().UTC().Add(-4*time.Hour))
			policy := GCPolicy{Before: time.Now().UTC().Add(-time.Hour)}
			plan, err := store.PlanGC(context.Background(), policy, nil)
			if err != nil {
				t.Fatal(err)
			}

			operations, _, _, err := ensureGCBase(root)
			if err != nil {
				t.Fatal(err)
			}
			operationPath, err := ensurePrivateChildDirectory(
				operations,
				strings.TrimPrefix(plan.Digest, "sha256:"),
			)
			if err != nil {
				t.Fatal(err)
			}
			if tt.makePlanTemp {
				if err := os.WriteFile(
					filepath.Join(operationPath, ".artifact-metadata-123456789.tmp"),
					[]byte("partial plan"),
					0o600,
				); err != nil {
					t.Fatal(err)
				}
			}
			if tt.persistPlan {
				payload, err := marshalBoundedJSON(plan, maxGCPlanJSONBytes)
				if err != nil {
					t.Fatal(err)
				}
				if err := writeImmutableFile(
					context.Background(),
					operationPath,
					filepath.Join(operationPath, gcPlanFileName),
					payload,
					maxGCPlanJSONBytes,
				); err != nil {
					t.Fatal(err)
				}
			}
			if tt.makeProgress {
				if _, err := ensurePrivateChildDirectory(
					operationPath,
					gcProgressDirectory,
				); err != nil {
					t.Fatal(err)
				}
			}

			result, err := store.ApplyGC(
				context.Background(),
				policy,
				nil,
				plan.Digest,
			)
			if err != nil {
				t.Fatalf("ApplyGC() error = %v", err)
			}
			if !result.Resumed || result.AlreadyApplied || result.DeletedBlobs != 1 {
				t.Fatalf("recovered result = %#v", result)
			}
			verification, err := store.Verify(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if verification.MaintenanceRequired || verification.Blobs != 0 {
				t.Fatalf("verification = %#v", verification)
			}
		})
	}
}

func TestRecoverUnpublishedGCOperationRejectsUnexpectedOrUnboundedState(t *testing.T) {
	tests := []struct {
		name string
		fill func(t *testing.T, operationPath string)
		want string
	}{
		{
			name: "unexpected file",
			fill: func(t *testing.T, operationPath string) {
				t.Helper()
				if err := os.WriteFile(
					filepath.Join(operationPath, "unrecognized"),
					nil,
					0o600,
				); err != nil {
					t.Fatal(err)
				}
			},
			want: "state but no readable plan",
		},
		{
			name: "too many metadata temporary files",
			fill: func(t *testing.T, operationPath string) {
				t.Helper()
				for index := 0; index <= maxUnpublishedGCMetadataTemporaryFiles; index++ {
					path := filepath.Join(
						operationPath,
						fmt.Sprintf(".artifact-metadata-%d.tmp", index),
					)
					if err := os.WriteFile(path, nil, 0o600); err != nil {
						t.Fatal(err)
					}
				}
			},
			want: "maximum temporary file count",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			operationPath := filepath.Join(t.TempDir(), "operation")
			if err := os.Mkdir(operationPath, 0o700); err != nil {
				t.Fatal(err)
			}
			test.fill(t, operationPath)
			if err := recoverUnpublishedGCOperation(operationPath); err == nil ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("recoverUnpublishedGCOperation() error = %v, want %q", err, test.want)
			}
			if err := requireRealDirectory(operationPath); err != nil {
				t.Fatalf("rejected operation state was removed: %v", err)
			}
		})
	}
}

func TestDirectoryStoreGCRecoversAfterKilledProcess(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts")
	ready := filepath.Join(filepath.Dir(root), "gc-helper.ready")
	store := DirectoryStore{Path: root}
	ref := putArtifact(
		t,
		store,
		artifactMetadata(t, model.ArtifactRetentionHistory, model.ArtifactRedactionNotRequired),
		"killed-orphan",
	)
	setBlobTime(t, root, ref.ID, time.Now().UTC().Add(-4*time.Hour))
	policy := GCPolicy{Before: time.Now().UTC().Add(-time.Hour)}
	plan, err := store.PlanGC(context.Background(), policy, nil)
	if err != nil {
		t.Fatal(err)
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestArtifactGCKilledProcessHelper$")
	command.Env = append(
		os.Environ(),
		"PGDRILL_GC_HELPER=1",
		"PGDRILL_GC_STORE="+root,
		"PGDRILL_GC_BEFORE="+policy.Before.Format(time.RFC3339Nano),
		"PGDRILL_GC_DIGEST="+plan.Digest,
		"PGDRILL_GC_READY="+ready,
	)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()
	waitForArtifactProcessReady(t, command, ready, &output)
	if err := command.Process.Kill(); err != nil {
		t.Fatalf("kill artifact GC helper: %v", err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("killed artifact GC helper exited successfully")
	}

	interrupted, err := store.Verify(context.Background(), nil)
	if err != nil {
		t.Fatalf("Verify() killed artifact GC state error = %v", err)
	}
	if !interrupted.MaintenanceRequired ||
		len(interrupted.PendingGCOperations) != 1 ||
		interrupted.PendingGCOperations[0] != plan.Digest {
		t.Fatalf("killed artifact GC verification = %#v", interrupted)
	}
	resumed, err := store.ApplyGC(context.Background(), policy, nil, plan.Digest)
	if err != nil {
		t.Fatalf("ApplyGC() after killed process error = %v", err)
	}
	if !resumed.Resumed || resumed.AlreadyApplied || resumed.DeletedBlobs != 1 {
		t.Fatalf("killed artifact GC recovery result = %#v", resumed)
	}
	clean, err := store.Verify(context.Background(), nil)
	if err != nil {
		t.Fatalf("Verify() recovered artifact GC state error = %v", err)
	}
	if clean.MaintenanceRequired || clean.Blobs != 0 {
		t.Fatalf("recovered artifact GC verification = %#v", clean)
	}
}

func TestArtifactGCKilledProcessHelper(t *testing.T) {
	if os.Getenv("PGDRILL_GC_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	before, err := time.Parse(time.RFC3339Nano, os.Getenv("PGDRILL_GC_BEFORE"))
	if err != nil {
		t.Fatal(err)
	}
	store := DirectoryStore{
		Path: os.Getenv("PGDRILL_GC_STORE"),
		gcHook: func(step string, index int) error {
			if step != gcStepAfterBlobRename || index != 0 {
				return nil
			}
			if err := os.WriteFile(
				os.Getenv("PGDRILL_GC_READY"),
				[]byte("ready\n"),
				0o600,
			); err != nil {
				return err
			}
			select {}
		},
	}
	_, err = store.ApplyGC(
		context.Background(),
		GCPolicy{Before: before},
		nil,
		os.Getenv("PGDRILL_GC_DIGEST"),
	)
	t.Fatalf("artifact GC helper returned unexpectedly: %v", err)
}

func TestDirectoryStoreGCCleansCompletedPendingOperation(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "artifacts")
	base := DirectoryStore{Path: root}
	ref := putArtifact(
		t,
		base,
		artifactMetadata(t, model.ArtifactRetentionHistory, model.ArtifactRedactionNotRequired),
		"orphan",
	)
	setBlobTime(t, root, ref.ID, time.Now().UTC().Add(-4*time.Hour))
	policy := GCPolicy{Before: time.Now().UTC().Add(-time.Hour)}
	plan, err := base.PlanGC(context.Background(), policy, nil)
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected final cleanup loss")
	interrupted := DirectoryStore{
		Path: root,
		gcHook: func(step string, _ int) error {
			if step == gcStepAfterFinalizeRename {
				return injected
			}
			return nil
		},
	}
	if _, err := interrupted.ApplyGC(context.Background(), policy, nil, plan.Digest); !errors.Is(err, injected) {
		t.Fatalf("ApplyGC(interrupted) error = %v", err)
	}
	verification, err := base.Verify(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !verification.MaintenanceRequired ||
		len(verification.PendingGCCleanup) != 1 ||
		verification.PendingGCCleanup[0] != plan.Digest {
		t.Fatalf("verification = %#v", verification)
	}
	result, err := base.ApplyGC(context.Background(), policy, nil, plan.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Resumed || !result.AlreadyApplied || result.DeletedBlobs != 1 {
		t.Fatalf("result = %#v", result)
	}
}

func TestDirectoryStoreGCAndConcurrentDigestReuseRemainConsistent(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "artifacts")
	base := DirectoryStore{Path: root}
	metadata := artifactMetadata(
		t,
		model.ArtifactRetentionHistory,
		model.ArtifactRedactionNotRequired,
	)
	ref := putArtifact(t, base, metadata, "reused-content")
	setBlobTime(t, root, ref.ID, time.Now().UTC().Add(-4*time.Hour))
	policy := GCPolicy{Before: time.Now().UTC().Add(-time.Hour)}
	plan, err := base.PlanGC(context.Background(), policy, nil)
	if err != nil {
		t.Fatal(err)
	}

	renamed := make(chan struct{})
	release := make(chan struct{})
	gcDone := make(chan error, 1)
	interrupted := DirectoryStore{
		Path: root,
		gcHook: func(step string, _ int) error {
			if step == gcStepAfterBlobRename {
				close(renamed)
				<-release
			}
			return nil
		},
	}
	go func() {
		_, err := interrupted.ApplyGC(context.Background(), policy, nil, plan.Digest)
		gcDone <- err
	}()
	<-renamed

	type putResult struct {
		ref model.ArtifactRef
		err error
	}
	putDone := make(chan putResult, 1)
	go func() {
		newRef, err := base.Put(
			context.Background(),
			metadata,
			strings.NewReader("reused-content"),
		)
		putDone <- putResult{ref: newRef, err: err}
	}()
	close(release)
	if err := <-gcDone; err != nil {
		t.Fatalf("ApplyGC() error = %v", err)
	}
	reused := <-putDone
	if reused.err != nil {
		t.Fatalf("Put(reused) error = %v", reused.err)
	}
	if reused.ref.ID != ref.ID {
		t.Fatalf("reused ID = %s, want %s", reused.ref.ID, ref.ID)
	}
	if _, err := base.Read(context.Background(), reused.ref); err != nil {
		t.Fatalf("returned reused reference is unreadable: %v", err)
	}
	verification, err := base.Verify(
		context.Background(),
		[]model.ArtifactRef{reused.ref},
	)
	if err != nil {
		t.Fatal(err)
	}
	if verification.Blobs != 1 || verification.ReferencedBlobs != 1 ||
		verification.MaintenanceRequired {
		t.Fatalf("verification = %#v", verification)
	}
}

func TestDirectoryStoreVerificationRejectsSymlinkedClaimsAndMismatchedURIBase(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "artifacts")
	store := DirectoryStore{Path: root}
	ref := putArtifact(
		t,
		store,
		artifactMetadata(t, model.ArtifactRetentionHistory, model.ArtifactRedactionNotRequired),
		"claim",
	)
	if _, err := (DirectoryStore{Path: root, URIBase: "wrong"}).Verify(
		context.Background(),
		[]model.ArtifactRef{ref},
	); err == nil || !strings.Contains(err.Error(), "uri_base") {
		t.Fatalf("Verify(mismatched URI base) error = %v", err)
	}

	hexDigest := strings.TrimPrefix(ref.ID, "sha256:")
	claimDir := filepath.Join(
		root,
		claimDirectoryName,
		blobDirectoryName,
		hexDigest[:2],
		hexDigest,
	)
	entries, err := os.ReadDir(claimDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("read claim directory: entries=%v error=%v", entries, err)
	}
	claimPath := filepath.Join(claimDir, entries[0].Name())
	target := filepath.Join(t.TempDir(), "claim.json")
	payload, err := os.ReadFile(claimPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(claimPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, claimPath); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Verify(
		context.Background(),
		[]model.ArtifactRef{ref},
	); err == nil || !strings.Contains(err.Error(), "non-symbolic-link") {
		t.Fatalf("Verify(symlinked claim) error = %v", err)
	}
}

func waitForArtifactProcessReady(
	t *testing.T,
	command *exec.Cmd,
	path string,
	output *bytes.Buffer,
) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Lstat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("inspect artifact GC helper readiness: %v", err)
		}
		if command.ProcessState != nil {
			t.Fatalf("artifact GC helper exited before readiness:\n%s", output.String())
		}
		if time.Now().After(deadline) {
			t.Fatalf("artifact GC helper did not reach fault boundary:\n%s", output.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func putArtifact(
	t *testing.T,
	store DirectoryStore,
	metadata model.ArtifactMetadata,
	payload string,
) model.ArtifactRef {
	t.Helper()
	ref, err := store.Put(context.Background(), metadata, strings.NewReader(payload))
	if err != nil {
		t.Fatalf("Put(%q) error = %v", payload, err)
	}
	return ref
}

func artifactMetadata(
	t *testing.T,
	retention model.ArtifactRetentionClass,
	redaction model.ArtifactRedactionState,
) model.ArtifactMetadata {
	t.Helper()
	metadata, err := model.NewArtifactMetadata("text/plain", retention, redaction)
	if err != nil {
		t.Fatal(err)
	}
	return metadata
}

func setBlobTime(t *testing.T, root, id string, value time.Time) {
	t.Helper()
	hexDigest := strings.TrimPrefix(id, "sha256:")
	path := filepath.Join(root, blobDirectoryName, hexDigest[:2], hexDigest)
	if err := os.Chtimes(path, value, value); err != nil {
		t.Fatalf("age artifact %s: %v", id, err)
	}
}

func writeLegacyBlob(t *testing.T, root, payload string) model.ArtifactRef {
	t.Helper()
	metadata := artifactMetadata(
		t,
		model.ArtifactRetentionHistory,
		model.ArtifactRedactionNotRequired,
	)
	memory := NewMemoryStore()
	ref, err := memory.Put(context.Background(), metadata, strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	hexDigest := strings.TrimPrefix(ref.ID, "sha256:")
	ref.URI = filepath.Base(root) + "/sha256/" + hexDigest[:2] + "/" + hexDigest
	dir := filepath.Join(root, blobDirectoryName, hexDigest[:2])
	if err := ensureDirectory(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, hexDigest), []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	return ref
}
