package history

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r314tive/pgdrill/internal/model"
)

func TestWithArtifactReferencesReturnsCompleteLockedSnapshot(t *testing.T) {
	t.Parallel()

	store := DirectoryStore{Path: filepath.Join(t.TempDir(), "history")}
	first := validResult(t, "artifact-run", "attempt-1", model.DrillStatusPassed)
	addAuditArtifact(t, &first)
	saveHistoryReport(t, store, first)
	second := historyRetry(first, "attempt-2", first.StartedAt.AddDate(0, 0, 1))
	second.Artifacts = append([]model.ArtifactRef{}, first.Artifacts...)
	second.Evidence = append([]model.EvidenceRecord{}, first.Evidence...)
	saveHistoryReport(t, store, second)

	var references []model.ArtifactRef
	if err := store.WithArtifactReferences(
		context.Background(),
		func(snapshot []model.ArtifactRef) error {
			references = append([]model.ArtifactRef{}, snapshot...)
			snapshot[0].URI = "mutated"
			return nil
		},
	); err != nil {
		t.Fatalf("WithArtifactReferences() error = %v", err)
	}
	if len(references) != 2 || references[0].ID != first.Artifacts[0].ID {
		t.Fatalf("references = %#v", references)
	}
	if err := store.WithArtifactReferences(
		context.Background(),
		func(snapshot []model.ArtifactRef) error {
			if strings.Contains(snapshot[0].URI, "mutated") {
				t.Fatalf("snapshot mutation escaped callback")
			}
			return nil
		},
	); err != nil {
		t.Fatal(err)
	}
	if err := store.WithArtifactReferences(context.Background(), nil); err == nil {
		t.Fatal("nil artifact reference callback unexpectedly succeeded")
	}
}
