package checkpoint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
)

func TestDirectoryStorePersistsAndTransitionsCheckpoint(t *testing.T) {
	store := DirectoryStore{Path: privateCheckpointRoot(t)}
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

func TestDirectoryStoreSaveServicesCheckpointTemporaryFiles(t *testing.T) {
	store := DirectoryStore{Path: privateCheckpointRoot(t)}
	operation := testOperation(t, "attempt-temp-recovery", 0)
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	checkpoint := model.OperationCheckpoint{
		SchemaVersion: model.CurrentOperationCheckpointSchemaVersion,
		Operation:     operation,
		State:         model.OperationStateIntent,
		StartedAt:     now,
		UpdatedAt:     now,
	}
	if err := store.Save(context.Background(), checkpoint); err != nil {
		t.Fatal(err)
	}
	attemptDir := filepath.Join(store.Path, attemptDirectoryName(operation.Identity))
	for index := 0; index < 3; index++ {
		path := filepath.Join(attemptDir, fmt.Sprintf(".checkpoint-%d.tmp", index))
		if err := os.WriteFile(path, []byte("partial"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := store.Save(context.Background(), checkpoint); err != nil {
		t.Fatalf("Save() recovery error = %v", err)
	}
	entries, err := os.ReadDir(attemptDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if isCheckpointTemporaryFileName(entry.Name()) {
			t.Fatalf("temporary checkpoint remains after Save(): %s", entry.Name())
		}
	}
	loaded, found, err := store.Load(context.Background(), operation)
	if err != nil {
		t.Fatal(err)
	}
	if !found || !reflect.DeepEqual(loaded, checkpoint) {
		t.Fatalf("Load() = (%#v, %t), want unchanged checkpoint", loaded, found)
	}
}

func TestDirectoryStoreSaveBoundsCheckpointTemporaryFiles(t *testing.T) {
	store := DirectoryStore{Path: privateCheckpointRoot(t)}
	operation := testOperation(t, "attempt-temp-bound", 0)
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	checkpoint := model.OperationCheckpoint{
		SchemaVersion: model.CurrentOperationCheckpointSchemaVersion,
		Operation:     operation,
		State:         model.OperationStateIntent,
		StartedAt:     now,
		UpdatedAt:     now,
	}
	if err := store.Save(context.Background(), checkpoint); err != nil {
		t.Fatal(err)
	}
	attemptDir := filepath.Join(store.Path, attemptDirectoryName(operation.Identity))
	for index := 0; index <= maxCheckpointTemporaryFiles; index++ {
		path := filepath.Join(attemptDir, fmt.Sprintf(".checkpoint-%d.tmp", index))
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Save(context.Background(), checkpoint); err == nil ||
		!strings.Contains(err.Error(), "maximum temporary file count") {
		t.Fatalf("Save() error = %v, want bounded temporary-state refusal", err)
	}
}

func TestDirectoryStoreListRejectsFilenameOperationMismatch(t *testing.T) {
	store := DirectoryStore{Path: privateCheckpointRoot(t)}
	operation := testOperation(t, "attempt-filename-mismatch", 0)
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	if err := store.Save(context.Background(), model.OperationCheckpoint{
		SchemaVersion: model.CurrentOperationCheckpointSchemaVersion,
		Operation:     operation,
		State:         model.OperationStateIntent,
		StartedAt:     now,
		UpdatedAt:     now,
	}); err != nil {
		t.Fatal(err)
	}

	attemptDir := filepath.Join(store.Path, attemptDirectoryName(operation.Identity))
	payload, err := os.ReadFile(filepath.Join(attemptDir, operationFileName(operation)))
	if err != nil {
		t.Fatal(err)
	}
	wrongName := strings.Repeat("f", 64) + ".json"
	if wrongName == operationFileName(operation) {
		wrongName = strings.Repeat("e", 64) + ".json"
	}
	if err := os.WriteFile(filepath.Join(attemptDir, wrongName), payload, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := store.List(context.Background(), operation.Identity); err == nil ||
		!strings.Contains(err.Error(), "does not match embedded operation key") {
		t.Fatalf("List() error = %v, want filename/key mismatch", err)
	}
}

func TestDirectoryStoreSaveRejectsCorruptSiblingWithoutMutation(t *testing.T) {
	store := DirectoryStore{Path: privateCheckpointRoot(t)}
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	first := testOperation(t, "attempt-corrupt-sibling", 0)
	second := testOperation(t, "attempt-corrupt-sibling", 1)
	checkpoint := model.OperationCheckpoint{
		SchemaVersion: model.CurrentOperationCheckpointSchemaVersion,
		Operation:     first,
		State:         model.OperationStateIntent,
		StartedAt:     now,
		UpdatedAt:     now,
	}
	if err := store.Save(context.Background(), checkpoint); err != nil {
		t.Fatal(err)
	}
	attemptDir := filepath.Join(store.Path, attemptDirectoryName(first.Identity))
	if err := os.WriteFile(
		filepath.Join(attemptDir, operationFileName(first)),
		[]byte("{"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	checkpoint.Operation = second
	if err := store.Save(context.Background(), checkpoint); err == nil ||
		!strings.Contains(err.Error(), "decode operation checkpoint") {
		t.Fatalf("Save() error = %v, want corrupt sibling refusal", err)
	}
	if _, err := os.Lstat(filepath.Join(attemptDir, operationFileName(second))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new checkpoint exists after refused Save(), stat error = %v", err)
	}
}

func TestDirectoryStoreListRejectsUnexpectedEntry(t *testing.T) {
	store := DirectoryStore{Path: privateCheckpointRoot(t)}
	operation := testOperation(t, "attempt-unexpected-entry", 0)
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	if err := store.Save(context.Background(), model.OperationCheckpoint{
		SchemaVersion: model.CurrentOperationCheckpointSchemaVersion,
		Operation:     operation,
		State:         model.OperationStateIntent,
		StartedAt:     now,
		UpdatedAt:     now,
	}); err != nil {
		t.Fatal(err)
	}
	attemptDir := filepath.Join(store.Path, attemptDirectoryName(operation.Identity))
	if err := os.WriteFile(filepath.Join(attemptDir, "unexpected"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := store.List(context.Background(), operation.Identity); err == nil ||
		!strings.Contains(err.Error(), "unexpected entry") {
		t.Fatalf("List() error = %v, want unexpected entry refusal", err)
	}
}

func TestDirectoryStoreListDoesNotCreateMissingAttemptState(t *testing.T) {
	root := filepath.Join(t.TempDir(), "checkpoints")
	store := DirectoryStore{Path: root}
	identity := model.AttemptIdentity{
		RunID:      "missing-run",
		AttemptID:  "missing-attempt",
		SpecDigest: "sha256:" + strings.Repeat("a", 64),
	}

	checkpoints, err := store.List(context.Background(), identity)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(checkpoints) != 0 {
		t.Fatalf("List() checkpoints = %#v, want empty", checkpoints)
	}
	operation, err := model.NewOperation(
		identity,
		model.DrillStageRestoreExecution,
		model.OperationRestoreStep,
		"restore",
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Load(context.Background(), operation); err != nil || found {
		t.Fatalf("Load() = (_, %t, %v), want missing checkpoint", found, err)
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only List()/Load() created checkpoint state, stat error = %v", err)
	}
}

func TestDirectoryStoreRejectsSymlinkAttemptLock(t *testing.T) {
	root := privateCheckpointRoot(t)
	store := DirectoryStore{Path: root}
	operation := testOperation(t, "attempt-symlink-lock", 0)
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	if err := store.Save(context.Background(), model.OperationCheckpoint{
		SchemaVersion: model.CurrentOperationCheckpointSchemaVersion,
		Operation:     operation,
		State:         model.OperationStateIntent,
		StartedAt:     now,
		UpdatedAt:     now,
	}); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(root, attemptDirectoryName(operation.Identity), ".lock")
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside-lock")
	if err := os.WriteFile(outside, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, lockPath); err != nil {
		t.Fatal(err)
	}

	if _, err := store.List(context.Background(), operation.Identity); err == nil ||
		!strings.Contains(err.Error(), "must not be a symbolic link") {
		t.Fatalf("List() error = %v, want symlink refusal", err)
	}
	if _, _, err := store.Load(context.Background(), operation); err == nil ||
		!strings.Contains(err.Error(), "must not be a symbolic link") {
		t.Fatalf("Load() error = %v, want symlink refusal", err)
	}
}

func TestDirectoryStoreRejectsSymlinkAttemptDirectory(t *testing.T) {
	root := privateCheckpointRoot(t)
	operation := testOperation(t, "attempt-symlink-dir", 0)
	attemptPath := filepath.Join(root, attemptDirectoryName(operation.Identity))
	if err := os.Symlink(t.TempDir(), attemptPath); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	err := (DirectoryStore{Path: root}).Save(
		context.Background(),
		model.OperationCheckpoint{
			SchemaVersion: model.CurrentOperationCheckpointSchemaVersion,
			Operation:     operation,
			State:         model.OperationStateIntent,
			StartedAt:     now,
			UpdatedAt:     now,
		},
	)
	if err == nil || !strings.Contains(err.Error(), "not a real directory") {
		t.Fatalf("Save() error = %v, want symlink directory refusal", err)
	}
}

func TestDirectoryStoreRejectsSymlinkRoot(t *testing.T) {
	realRoot := t.TempDir()
	root := filepath.Join(t.TempDir(), "checkpoint-link")
	if err := os.Symlink(realRoot, root); err != nil {
		t.Fatal(err)
	}
	store := DirectoryStore{Path: root}
	operation := testOperation(t, "attempt-symlink-root", 0)
	if _, err := store.List(context.Background(), operation.Identity); err == nil ||
		!strings.Contains(err.Error(), "not a real directory") {
		t.Fatalf("List() error = %v, want symlink root refusal", err)
	}
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	err := store.Save(context.Background(), model.OperationCheckpoint{
		SchemaVersion: model.CurrentOperationCheckpointSchemaVersion,
		Operation:     operation,
		State:         model.OperationStateIntent,
		StartedAt:     now,
		UpdatedAt:     now,
	})
	if err == nil || !strings.Contains(err.Error(), "not a real directory") {
		t.Fatalf("Save() error = %v, want symlink root refusal", err)
	}
}

func TestDirectoryStoreRejectsPermissiveState(t *testing.T) {
	tests := []struct {
		name string
		path func(root, attemptDir string, operation model.Operation) string
		mode os.FileMode
	}{
		{
			name: "root directory",
			path: func(root, _ string, _ model.Operation) string { return root },
			mode: 0o755,
		},
		{
			name: "attempt directory",
			path: func(_, attemptDir string, _ model.Operation) string { return attemptDir },
			mode: 0o755,
		},
		{
			name: "lock file",
			path: func(_, attemptDir string, _ model.Operation) string {
				return filepath.Join(attemptDir, ".lock")
			},
			mode: 0o644,
		},
		{
			name: "checkpoint file",
			path: func(_, attemptDir string, operation model.Operation) string {
				return filepath.Join(attemptDir, operationFileName(operation))
			},
			mode: 0o644,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "checkpoints")
			store := DirectoryStore{Path: root}
			operation := testOperation(t, "attempt-permissions-"+strings.ReplaceAll(test.name, " ", "-"), 0)
			now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
			if err := store.Save(context.Background(), model.OperationCheckpoint{
				SchemaVersion: model.CurrentOperationCheckpointSchemaVersion,
				Operation:     operation,
				State:         model.OperationStateIntent,
				StartedAt:     now,
				UpdatedAt:     now,
			}); err != nil {
				t.Fatal(err)
			}
			attemptDir := filepath.Join(root, attemptDirectoryName(operation.Identity))
			if err := os.Chmod(test.path(root, attemptDir, operation), test.mode); err != nil {
				t.Fatal(err)
			}

			if _, _, err := store.Load(context.Background(), operation); err == nil ||
				!strings.Contains(err.Error(), "not private") {
				t.Fatalf("Load() error = %v, want private-permission refusal", err)
			}
		})
	}
}

func TestStoresRejectTerminalCheckpointRegression(t *testing.T) {
	stores := []Store{NewMemoryStore(), DirectoryStore{Path: privateCheckpointRoot(t)}}
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
	stores := []Store{NewMemoryStore(), DirectoryStore{Path: privateCheckpointRoot(t)}}
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
	store := DirectoryStore{Path: privateCheckpointRoot(t)}
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

func TestStoresRejectAttemptCheckpointCapacityOverflow(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)

	t.Run("memory", func(t *testing.T) {
		store := NewMemoryStore()
		for ordinal := 0; ordinal < model.MaxOperationsPerReport; ordinal++ {
			operation := testOperation(t, "attempt-memory-capacity", ordinal)
			if err := store.Save(context.Background(), model.OperationCheckpoint{
				SchemaVersion: model.CurrentOperationCheckpointSchemaVersion,
				Operation:     operation,
				State:         model.OperationStateIntent,
				StartedAt:     now,
				UpdatedAt:     now,
			}); err != nil {
				t.Fatalf("Save(%d) error = %v", ordinal, err)
			}
		}
		overflow := testOperation(t, "attempt-memory-capacity", model.MaxOperationsPerReport)
		if err := store.Save(context.Background(), model.OperationCheckpoint{
			SchemaVersion: model.CurrentOperationCheckpointSchemaVersion,
			Operation:     overflow,
			State:         model.OperationStateIntent,
			StartedAt:     now,
			UpdatedAt:     now,
		}); err == nil || !strings.Contains(err.Error(), "maximum count") {
			t.Fatalf("Save(overflow) error = %v", err)
		}
	})

	t.Run("directory", func(t *testing.T) {
		root := privateCheckpointRoot(t)
		store := DirectoryStore{Path: root}
		first := testOperation(t, "attempt-directory-capacity", 0)
		if err := store.Save(context.Background(), model.OperationCheckpoint{
			SchemaVersion: model.CurrentOperationCheckpointSchemaVersion,
			Operation:     first,
			State:         model.OperationStateIntent,
			StartedAt:     now,
			UpdatedAt:     now,
		}); err != nil {
			t.Fatal(err)
		}
		attemptDir := filepath.Join(root, attemptDirectoryName(first.Identity))
		for ordinal := 1; ordinal < model.MaxOperationsPerReport; ordinal++ {
			operation := testOperation(t, "attempt-directory-capacity", ordinal)
			payload, err := json.Marshal(model.OperationCheckpoint{
				SchemaVersion: model.CurrentOperationCheckpointSchemaVersion,
				Operation:     operation,
				State:         model.OperationStateIntent,
				StartedAt:     now,
				UpdatedAt:     now,
			})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(attemptDir, operationFileName(operation))
			if err := os.WriteFile(path, payload, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		overflow := testOperation(t, "attempt-directory-capacity", model.MaxOperationsPerReport)
		if err := store.Save(context.Background(), model.OperationCheckpoint{
			SchemaVersion: model.CurrentOperationCheckpointSchemaVersion,
			Operation:     overflow,
			State:         model.OperationStateIntent,
			StartedAt:     now,
			UpdatedAt:     now,
		}); err == nil || !strings.Contains(err.Error(), "maximum count") {
			t.Fatalf("Save(overflow) error = %v", err)
		}
	})
}

func TestSortCheckpointsUsesOrdinalThenCanonicalTieBreakers(t *testing.T) {
	identity := model.AttemptIdentity{
		RunID:      "run-sort",
		AttemptID:  "attempt-sort",
		SpecDigest: "sha256:" + strings.Repeat("b", 64),
	}
	operations := make([]model.Operation, 0, 3)
	for _, input := range []struct {
		stage   model.DrillStage
		kind    model.OperationKind
		name    string
		ordinal int
	}{
		{model.DrillStageTargetCleanup, model.OperationTargetCleanup, "cleanup", 2},
		{model.DrillStageRestoreExecution, model.OperationRestoreStep, "restore", 1},
		{model.DrillStageTargetPreparation, model.OperationTargetPrepare, "prepare", 0},
	} {
		operation, err := model.NewOperation(
			identity,
			input.stage,
			input.kind,
			input.name,
			input.ordinal,
		)
		if err != nil {
			t.Fatal(err)
		}
		operations = append(operations, operation)
	}
	checkpoints := make([]model.OperationCheckpoint, len(operations))
	for index, operation := range operations {
		checkpoints[index].Operation = operation
	}
	sortCheckpoints(checkpoints)
	for index, checkpoint := range checkpoints {
		if checkpoint.Operation.Ordinal != index {
			t.Fatalf("sorted ordinal %d = %d", index, checkpoint.Operation.Ordinal)
		}
	}
}

func privateCheckpointRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "checkpoints")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatalf("create private checkpoint root: %v", err)
	}
	return root
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
