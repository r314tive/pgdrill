package checkpoint

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/r314tive/pgdrill/internal/model"
)

const maxCheckpointFileBytes = 64 << 10

type Store interface {
	Save(ctx context.Context, checkpoint model.OperationCheckpoint) error
	Load(ctx context.Context, operation model.Operation) (model.OperationCheckpoint, bool, error)
	List(ctx context.Context, identity model.AttemptIdentity) ([]model.OperationCheckpoint, error)
}

func PathForReport(reportPath string) string {
	return filepath.Clean(reportPath) + ".checkpoints"
}

// MemoryStore is intentionally volatile. It is useful for embedding tests,
// while executable paths must use DirectoryStore for crash reconciliation.
type MemoryStore struct {
	mu          sync.RWMutex
	checkpoints map[string]model.OperationCheckpoint
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{checkpoints: map[string]model.OperationCheckpoint{}}
}

func (s *MemoryStore) Save(ctx context.Context, checkpoint model.OperationCheckpoint) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkpoint.Validate(); err != nil {
		return fmt.Errorf("validate operation checkpoint: %w", err)
	}
	if s == nil {
		return fmt.Errorf("memory checkpoint store is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.checkpoints == nil {
		s.checkpoints = map[string]model.OperationCheckpoint{}
	}
	if previous, ok := s.checkpoints[checkpoint.Operation.Key]; ok {
		if err := validateTransition(previous, checkpoint); err != nil {
			return err
		}
	} else if checkpoint.State != model.OperationStateIntent {
		return fmt.Errorf("first checkpoint state must be %q", model.OperationStateIntent)
	}
	s.checkpoints[checkpoint.Operation.Key] = checkpoint
	return nil
}

func (s *MemoryStore) Load(ctx context.Context, operation model.Operation) (model.OperationCheckpoint, bool, error) {
	if err := ctx.Err(); err != nil {
		return model.OperationCheckpoint{}, false, err
	}
	if err := operation.Validate(); err != nil {
		return model.OperationCheckpoint{}, false, fmt.Errorf("validate operation: %w", err)
	}
	if s == nil {
		return model.OperationCheckpoint{}, false, fmt.Errorf("memory checkpoint store is required")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	checkpoint, ok := s.checkpoints[operation.Key]
	if ok && !reflect.DeepEqual(checkpoint.Operation, operation) {
		return model.OperationCheckpoint{}, false, fmt.Errorf("checkpoint key %q belongs to another operation", operation.Key)
	}
	return checkpoint, ok, nil
}

func (s *MemoryStore) List(ctx context.Context, identity model.AttemptIdentity) ([]model.OperationCheckpoint, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := identity.Validate(); err != nil {
		return nil, fmt.Errorf("validate attempt identity: %w", err)
	}
	if s == nil {
		return nil, fmt.Errorf("memory checkpoint store is required")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := []model.OperationCheckpoint{}
	for _, checkpoint := range s.checkpoints {
		if checkpoint.Operation.Identity == identity {
			result = append(result, checkpoint)
		}
	}
	sortCheckpoints(result)
	return result, nil
}

// DirectoryStore persists one atomically replaced JSON document per operation.
// An attempt-scoped advisory lock serializes transitions across local
// processes. The resulting current-state journal is deliberately independent
// from the terminal report.
type DirectoryStore struct {
	Path string
}

func (s DirectoryStore) Save(ctx context.Context, checkpoint model.OperationCheckpoint) error {
	if err := checkpoint.Validate(); err != nil {
		return fmt.Errorf("validate operation checkpoint: %w", err)
	}
	return s.withAttemptLock(ctx, checkpoint.Operation.Identity, unix.LOCK_EX, func(dir string) error {
		path := filepath.Join(dir, operationFileName(checkpoint.Operation))
		previous, found, err := readCheckpoint(path)
		if err != nil {
			return err
		}
		if found {
			if err := validateTransition(previous, checkpoint); err != nil {
				return err
			}
		} else if checkpoint.State != model.OperationStateIntent {
			return fmt.Errorf("first checkpoint state must be %q", model.OperationStateIntent)
		}
		payload, err := json.MarshalIndent(checkpoint, "", "  ")
		if err != nil {
			return fmt.Errorf("encode operation checkpoint: %w", err)
		}
		payload = append(payload, '\n')
		if len(payload) > maxCheckpointFileBytes {
			return fmt.Errorf("operation checkpoint exceeds %d bytes", maxCheckpointFileBytes)
		}
		return replaceFile(ctx, dir, path, payload)
	})
}

func (s DirectoryStore) Load(ctx context.Context, operation model.Operation) (model.OperationCheckpoint, bool, error) {
	if err := operation.Validate(); err != nil {
		return model.OperationCheckpoint{}, false, fmt.Errorf("validate operation: %w", err)
	}
	var checkpoint model.OperationCheckpoint
	var found bool
	err := s.withAttemptLock(ctx, operation.Identity, unix.LOCK_SH, func(dir string) error {
		var err error
		checkpoint, found, err = readCheckpoint(filepath.Join(dir, operationFileName(operation)))
		return err
	})
	if err != nil {
		return model.OperationCheckpoint{}, false, err
	}
	if found && !reflect.DeepEqual(checkpoint.Operation, operation) {
		return model.OperationCheckpoint{}, false, fmt.Errorf("checkpoint key %q belongs to another operation", operation.Key)
	}
	return checkpoint, found, nil
}

func (s DirectoryStore) List(ctx context.Context, identity model.AttemptIdentity) ([]model.OperationCheckpoint, error) {
	if err := identity.Validate(); err != nil {
		return nil, fmt.Errorf("validate attempt identity: %w", err)
	}
	result := []model.OperationCheckpoint{}
	err := s.withAttemptLock(ctx, identity, unix.LOCK_SH, func(dir string) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return fmt.Errorf("read attempt checkpoint directory %s: %w", dir, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			checkpoint, found, err := readCheckpoint(filepath.Join(dir, entry.Name()))
			if err != nil {
				return err
			}
			if !found || checkpoint.Operation.Identity != identity {
				return fmt.Errorf("checkpoint file %s has mismatched attempt identity", entry.Name())
			}
			result = append(result, checkpoint)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sortCheckpoints(result)
	return result, nil
}

func (s DirectoryStore) withAttemptLock(ctx context.Context, identity model.AttemptIdentity, mode int, operation func(string) error) error {
	if strings.TrimSpace(s.Path) == "" {
		return fmt.Errorf("checkpoint store path is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	root := filepath.Clean(s.Path)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("create checkpoint store directory %s: %w", root, err)
	}
	dir := filepath.Join(root, attemptDirectoryName(identity))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create attempt checkpoint directory %s: %w", dir, err)
	}
	lock, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open attempt checkpoint lock: %w", err)
	}
	defer lock.Close()
	if err := flockContext(ctx, int(lock.Fd()), mode); err != nil {
		return fmt.Errorf("lock attempt checkpoints: %w", err)
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN) //nolint:errcheck
	if err := ctx.Err(); err != nil {
		return err
	}
	return operation(dir)
}

func flockContext(ctx context.Context, fd int, mode int) error {
	for {
		err := unix.Flock(fd, mode|unix.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func validateTransition(previous, next model.OperationCheckpoint) error {
	if !reflect.DeepEqual(previous.Operation, next.Operation) {
		return fmt.Errorf("checkpoint operation is immutable")
	}
	if next.StartedAt != previous.StartedAt {
		return fmt.Errorf("checkpoint started_at is immutable")
	}
	if next.UpdatedAt.Before(previous.UpdatedAt) {
		return fmt.Errorf("checkpoint updated_at must not move backwards")
	}
	if previous.State == next.State {
		return nil
	}
	switch previous.State {
	case model.OperationStateIntent:
		if next.State.IsTerminal() {
			return nil
		}
	case model.OperationStateUnknown:
		if next.State == model.OperationStateSucceeded || next.State == model.OperationStateFailed {
			return nil
		}
	}
	return fmt.Errorf("invalid checkpoint transition %q -> %q", previous.State, next.State)
}

func readCheckpoint(path string) (model.OperationCheckpoint, bool, error) {
	linkInfo, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return model.OperationCheckpoint{}, false, nil
	}
	if err != nil {
		return model.OperationCheckpoint{}, false, fmt.Errorf("inspect operation checkpoint %s: %w", path, err)
	}
	if linkInfo.Mode()&os.ModeSymlink != 0 {
		return model.OperationCheckpoint{}, false, fmt.Errorf("operation checkpoint must not be a symbolic link: %s", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return model.OperationCheckpoint{}, false, fmt.Errorf("open operation checkpoint %s: %w", path, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return model.OperationCheckpoint{}, false, fmt.Errorf("stat operation checkpoint %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return model.OperationCheckpoint{}, false, fmt.Errorf("operation checkpoint is not a regular file: %s", path)
	}
	if info.Size() > maxCheckpointFileBytes {
		return model.OperationCheckpoint{}, false, fmt.Errorf("operation checkpoint %s exceeds %d bytes", path, maxCheckpointFileBytes)
	}
	decoder := json.NewDecoder(io.LimitReader(file, maxCheckpointFileBytes+1))
	decoder.DisallowUnknownFields()
	var checkpoint model.OperationCheckpoint
	if err := decoder.Decode(&checkpoint); err != nil {
		return model.OperationCheckpoint{}, false, fmt.Errorf("decode operation checkpoint %s: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return model.OperationCheckpoint{}, false, fmt.Errorf("operation checkpoint %s contains multiple JSON values", path)
		}
		return model.OperationCheckpoint{}, false, fmt.Errorf("decode operation checkpoint trailing data %s: %w", path, err)
	}
	if err := checkpoint.Validate(); err != nil {
		return model.OperationCheckpoint{}, false, fmt.Errorf("validate operation checkpoint %s: %w", path, err)
	}
	return checkpoint, true, nil
}

func replaceFile(ctx context.Context, dir, path string, payload []byte) error {
	file, err := os.CreateTemp(dir, ".checkpoint-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary checkpoint: %w", err)
	}
	tmpPath := file.Name()
	defer os.Remove(tmpPath) //nolint:errcheck
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("chmod temporary checkpoint: %w", err)
	}
	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		return fmt.Errorf("write temporary checkpoint: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync temporary checkpoint: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temporary checkpoint: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace operation checkpoint %s: %w", path, err)
	}
	return syncDirectory(dir)
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func attemptDirectoryName(identity model.AttemptIdentity) string {
	payload, _ := json.Marshal(identity)
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func operationFileName(operation model.Operation) string {
	return strings.TrimPrefix(operation.Key, "sha256:") + ".json"
}

func sortCheckpoints(checkpoints []model.OperationCheckpoint) {
	sort.Slice(checkpoints, func(i, j int) bool {
		left := checkpoints[i].Operation
		right := checkpoints[j].Operation
		if left.Ordinal != right.Ordinal {
			return left.Ordinal < right.Ordinal
		}
		if left.Stage != right.Stage {
			return left.Stage < right.Stage
		}
		return left.Key < right.Key
	})
}
