package artifact

import (
	"context"
	"errors"
	"os"
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
