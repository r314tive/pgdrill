package history

import (
	"context"
	"fmt"

	"github.com/r314tive/pgdrill/internal/model"
)

// WithArtifactReferences holds the history read lock while operation consumes
// a complete snapshot of every retained terminal-report artifact reference.
// Callers that mutate an artifact store use this boundary to prevent history
// writes or retention from changing liveness during their operation.
func (s DirectoryStore) WithArtifactReferences(
	ctx context.Context,
	operation func([]model.ArtifactRef) error,
) error {
	if operation == nil {
		return fmt.Errorf("artifact reference operation is required")
	}
	return s.withReadLock(ctx, func(root string) error {
		records, err := readAllRuns(root)
		if err != nil {
			return err
		}
		references := make([]model.ArtifactRef, 0)
		for _, record := range records {
			for _, attempt := range record.Attempts {
				if attempt.Report == nil {
					continue
				}
				references = append(references, attempt.Report.Artifacts...)
			}
		}
		return operation(references)
	})
}
