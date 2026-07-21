package checkpoint

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
)

func TestDirectoryStorePersistsAndTransitionsCheckpoint(t *testing.T) {
	store := DirectoryStore{Path: t.TempDir()}
	operation := testOperation(t, "attempt-1", 0)
	startedAt := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	intent := model.OperationCheckpoint{
		SchemaVersion: model.CurrentOperationCheckpointSchemaVersion,
		Operation:     operation,
		State:         model.OperationStateIntent,
		StartedAt:     startedAt,
		UpdatedAt:     startedAt,
	}
	if err := store.Save(context.Background(), intent); err != nil {
		t.Fatalf("Save(intent) error = %v", err)
	}
	completed := intent
	completed.State = model.OperationStateSucceeded
	completed.UpdatedAt = startedAt.Add(time.Second)
	if err := store.Save(context.Background(), completed); err != nil {
		t.Fatalf("Save(completed) error = %v", err)
	}

	loaded, found, err := store.Load(context.Background(), operation)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !found || loaded.State != model.OperationStateSucceeded {
		t.Fatalf("Load() = (%#v, %t), want succeeded checkpoint", loaded, found)
	}
	listed, err := store.List(context.Background(), operation.Identity)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(listed) != 1 || listed[0].Operation.Key != operation.Key {
		t.Fatalf("List() = %#v", listed)
	}
}

func TestStoresRejectTerminalCheckpointRegression(t *testing.T) {
	stores := []Store{NewMemoryStore(), DirectoryStore{Path: t.TempDir()}}
	for _, store := range stores {
		operation := testOperation(t, "attempt-regression", 0)
		now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
		intent := model.OperationCheckpoint{
			SchemaVersion: model.CurrentOperationCheckpointSchemaVersion,
			Operation:     operation,
			State:         model.OperationStateIntent,
			StartedAt:     now,
			UpdatedAt:     now,
		}
		if err := store.Save(context.Background(), intent); err != nil {
			t.Fatalf("Save(intent) error = %v", err)
		}
		completed := intent
		completed.State = model.OperationStateSucceeded
		completed.UpdatedAt = now.Add(time.Second)
		if err := store.Save(context.Background(), completed); err != nil {
			t.Fatalf("Save(completed) error = %v", err)
		}
		regression := completed
		regression.State = model.OperationStateUnknown
		regression.UpdatedAt = now.Add(2 * time.Second)
		if err := store.Save(context.Background(), regression); err == nil || !strings.Contains(err.Error(), "invalid checkpoint transition") {
			t.Fatalf("Save(regression) error = %v", err)
		}
	}
}

func TestStoresRequireIntentAsFirstCheckpoint(t *testing.T) {
	stores := []Store{NewMemoryStore(), DirectoryStore{Path: t.TempDir()}}
	for _, store := range stores {
		operation := testOperation(t, "attempt-terminal-first", 0)
		now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
		err := store.Save(context.Background(), model.OperationCheckpoint{
			SchemaVersion: model.CurrentOperationCheckpointSchemaVersion,
			Operation:     operation,
			State:         model.OperationStateSucceeded,
			StartedAt:     now,
			UpdatedAt:     now,
		})
		if err == nil || !strings.Contains(err.Error(), "first checkpoint state must be") {
			t.Fatalf("Save(terminal-first) error = %v", err)
		}
	}
}

func TestDirectoryStoreSeparatesAttemptNamespaces(t *testing.T) {
	store := DirectoryStore{Path: t.TempDir()}
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	for index, attempt := range []string{"attempt-1", "attempt-2"} {
		operation := testOperation(t, attempt, index)
		if err := store.Save(context.Background(), model.OperationCheckpoint{
			SchemaVersion: model.CurrentOperationCheckpointSchemaVersion,
			Operation:     operation,
			State:         model.OperationStateIntent,
			StartedAt:     now,
			UpdatedAt:     now,
		}); err != nil {
			t.Fatalf("Save(%s) error = %v", attempt, err)
		}
		listed, err := store.List(context.Background(), operation.Identity)
		if err != nil {
			t.Fatalf("List(%s) error = %v", attempt, err)
		}
		if len(listed) != 1 || listed[0].Operation.Identity.AttemptID != attempt {
			t.Fatalf("List(%s) = %#v", attempt, listed)
		}
	}
}

func testOperation(t *testing.T, attempt string, ordinal int) model.Operation {
	t.Helper()
	operation, err := model.NewOperation(model.AttemptIdentity{
		RunID:      "run-1",
		AttemptID:  attempt,
		SpecDigest: "sha256:" + strings.Repeat("a", 64),
	}, model.DrillStageRestoreExecution, model.OperationRestoreStep, "restore", ordinal)
	if err != nil {
		t.Fatalf("NewOperation() error = %v", err)
	}
	return operation
}
