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

	"github.com/r314tive/pgdrill/internal/durablefs"
	"github.com/r314tive/pgdrill/internal/filelock"
	"github.com/r314tive/pgdrill/internal/jsonutil"
	"github.com/r314tive/pgdrill/internal/model"
)

const (
	maxCheckpointFileBytes      = 64 << 10
	maxCheckpointTemporaryFiles = 128
	maxAttemptDirectoryEntries  = model.MaxOperationsPerReport + maxCheckpointTemporaryFiles + 1
)

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
	} else {
		if checkpoint.State != model.OperationStateIntent {
			return fmt.Errorf("first checkpoint state must be %q", model.OperationStateIntent)
		}
		count := 0
		for _, existing := range s.checkpoints {
			if existing.Operation.Identity == checkpoint.Operation.Identity {
				count++
			}
		}
		if count >= model.MaxOperationsPerReport {
			return fmt.Errorf(
				"attempt checkpoints exceed maximum count %d",
				model.MaxOperationsPerReport,
			)
		}
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
	return s.withAttemptLock(ctx, checkpoint.Operation.Identity, filelock.Exclusive, func(dir string) error {
		checkpoints, err := readAttemptCheckpoints(dir, checkpoint.Operation.Identity)
		if err != nil {
			return err
		}
		var previous model.OperationCheckpoint
		found := false
		for _, existing := range checkpoints {
			if existing.Operation.Key == checkpoint.Operation.Key {
				previous = existing
				found = true
				break
			}
		}
		if found {
			if err := validateTransition(previous, checkpoint); err != nil {
				return err
			}
		} else {
			if checkpoint.State != model.OperationStateIntent {
				return fmt.Errorf("first checkpoint state must be %q", model.OperationStateIntent)
			}
			if len(checkpoints) >= model.MaxOperationsPerReport {
				return fmt.Errorf(
					"attempt checkpoints exceed maximum count %d",
					model.MaxOperationsPerReport,
				)
			}
		}
		path := filepath.Join(dir, operationFileName(checkpoint.Operation))
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
	err := s.withExistingAttemptLock(ctx, operation.Identity, func(dir string) error {
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
	err := s.withExistingAttemptLock(ctx, identity, func(dir string) error {
		var err error
		result, err = readAttemptCheckpoints(dir, identity)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s DirectoryStore) withExistingAttemptLock(
	ctx context.Context,
	identity model.AttemptIdentity,
	operation func(string) error,
) error {
	if strings.TrimSpace(s.Path) == "" {
		return fmt.Errorf("checkpoint store path is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	root := filepath.Clean(s.Path)
	rootInfo, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect checkpoint store directory %s: %w", root, err)
	}
	if err := validateCheckpointDirectoryInfo(root, rootInfo); err != nil {
		return err
	}
	dir := filepath.Join(root, attemptDirectoryName(identity))
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect attempt checkpoint directory %s: %w", dir, err)
	}
	if err := validateCheckpointDirectoryInfo(dir, info); err != nil {
		return err
	}
	lockPath := filepath.Join(dir, ".lock")
	lock, err := openAttemptLock(lockPath, false)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := filelock.Lock(ctx, lock, filelock.Shared); err != nil {
		return fmt.Errorf("lock attempt checkpoints: %w", err)
	}
	defer filelock.Unlock(lock) //nolint:errcheck
	if err := ctx.Err(); err != nil {
		return err
	}
	return operation(dir)
}

func (s DirectoryStore) withAttemptLock(ctx context.Context, identity model.AttemptIdentity, mode filelock.Mode, operation func(string) error) error {
	if strings.TrimSpace(s.Path) == "" {
		return fmt.Errorf("checkpoint store path is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	root := filepath.Clean(s.Path)
	if err := durablefs.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("create checkpoint store directory %s: %w", root, err)
	}
	if err := ensureRealCheckpointDirectory(root); err != nil {
		return err
	}
	dir := filepath.Join(root, attemptDirectoryName(identity))
	if err := durablefs.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create attempt checkpoint directory %s: %w", dir, err)
	}
	if err := ensureRealCheckpointDirectory(dir); err != nil {
		return err
	}
	lock, err := openAttemptLock(filepath.Join(dir, ".lock"), true)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := filelock.Lock(ctx, lock, mode); err != nil {
		return fmt.Errorf("lock attempt checkpoints: %w", err)
	}
	defer filelock.Unlock(lock) //nolint:errcheck
	if err := ctx.Err(); err != nil {
		return err
	}
	if mode == filelock.Exclusive {
		if err := cleanupCheckpointTemporaryFiles(dir); err != nil {
			return err
		}
	}
	return operation(dir)
}

func ensureRealCheckpointDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect checkpoint directory %s: %w", path, err)
	}
	return validateCheckpointDirectoryInfo(path, info)
}

func validateCheckpointDirectoryInfo(path string, info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("checkpoint path is not a real directory: %s", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("checkpoint directory permissions %o are not private: %s", info.Mode().Perm(), path)
	}
	return nil
}

func openAttemptLock(path string, create bool) (*os.File, error) {
	flags := os.O_RDWR
	if create {
		flags |= os.O_CREATE
	}
	file, err := filelock.OpenPrivate(path, flags)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("attempt checkpoint lock is missing: %s", path)
	}
	if err != nil {
		return nil, fmt.Errorf("open attempt checkpoint lock: %w", err)
	}
	return file, nil
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
	if !linkInfo.Mode().IsRegular() {
		return model.OperationCheckpoint{}, false, fmt.Errorf("operation checkpoint is not a regular file: %s", path)
	}
	if linkInfo.Mode().Perm()&0o077 != 0 {
		return model.OperationCheckpoint{}, false, fmt.Errorf(
			"operation checkpoint permissions %o are not private: %s",
			linkInfo.Mode().Perm(),
			path,
		)
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
	if !info.Mode().IsRegular() || !os.SameFile(linkInfo, info) {
		return model.OperationCheckpoint{}, false, fmt.Errorf("operation checkpoint changed while opening: %s", path)
	}
	if info.Size() > maxCheckpointFileBytes {
		return model.OperationCheckpoint{}, false, fmt.Errorf("operation checkpoint %s exceeds %d bytes", path, maxCheckpointFileBytes)
	}
	payload, err := io.ReadAll(io.LimitReader(file, maxCheckpointFileBytes+1))
	if err != nil {
		return model.OperationCheckpoint{}, false, fmt.Errorf("read operation checkpoint %s: %w", path, err)
	}
	if len(payload) > maxCheckpointFileBytes {
		return model.OperationCheckpoint{}, false, fmt.Errorf("operation checkpoint %s exceeds %d bytes", path, maxCheckpointFileBytes)
	}
	var checkpoint model.OperationCheckpoint
	if err := jsonutil.DecodeOneStrict(payload, &checkpoint); err != nil {
		return model.OperationCheckpoint{}, false, fmt.Errorf("decode operation checkpoint %s: %w", path, err)
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

func cleanupCheckpointTemporaryFiles(dir string) error {
	entries, err := durablefs.ReadDirBounded(dir, maxAttemptDirectoryEntries)
	if err != nil {
		return fmt.Errorf("read attempt checkpoint directory for temporary recovery: %w", err)
	}
	paths := make([]string, 0)
	for _, entry := range entries {
		if !isCheckpointTemporaryFileName(entry.Name()) {
			continue
		}
		if len(paths) >= maxCheckpointTemporaryFiles {
			return fmt.Errorf(
				"attempt checkpoints exceed maximum temporary file count %d",
				maxCheckpointTemporaryFiles,
			)
		}
		path := filepath.Join(dir, entry.Name())
		if err := validateCheckpointTemporaryFile(path); err != nil {
			return err
		}
		paths = append(paths, path)
	}
	if len(paths) == 0 {
		return nil
	}
	var removeErr error
	removed := false
	for _, path := range paths {
		if err := os.Remove(path); err != nil {
			removeErr = fmt.Errorf("remove temporary checkpoint %s: %w", path, err)
			break
		}
		removed = true
	}
	var syncErr error
	if removed {
		if err := syncDirectory(dir); err != nil {
			syncErr = fmt.Errorf("sync checkpoint directory after temporary recovery: %w", err)
		}
	}
	return errors.Join(removeErr, syncErr)
}

func validateCheckpointTemporaryFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect temporary checkpoint %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("temporary checkpoint is not a regular non-symbolic-link file: %s", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf(
			"temporary checkpoint permissions %o are not private: %s",
			info.Mode().Perm(),
			path,
		)
	}
	if info.Size() > maxCheckpointFileBytes {
		return fmt.Errorf("temporary checkpoint %s exceeds %d bytes", path, maxCheckpointFileBytes)
	}
	return nil
}

func isCheckpointTemporaryFileName(name string) bool {
	const (
		prefix = ".checkpoint-"
		suffix = ".tmp"
	)
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return false
	}
	random := strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix)
	if random == "" {
		return false
	}
	for _, character := range random {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func syncDirectory(path string) error {
	return durablefs.SyncDirectory(path)
}

func attemptDirectoryName(identity model.AttemptIdentity) string {
	payload, _ := json.Marshal(identity)
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func operationFileName(operation model.Operation) string {
	return strings.TrimPrefix(operation.Key, "sha256:") + ".json"
}

func readAttemptCheckpoints(
	dir string,
	identity model.AttemptIdentity,
) ([]model.OperationCheckpoint, error) {
	entries, err := durablefs.ReadDirBounded(dir, maxAttemptDirectoryEntries)
	if err != nil {
		return nil, fmt.Errorf("read attempt checkpoint directory %s: %w", dir, err)
	}
	result := make([]model.OperationCheckpoint, 0, min(len(entries), model.MaxOperationsPerReport))
	seen := make(map[string]struct{}, len(entries))
	temporaryCount := 0
	for _, entry := range entries {
		switch {
		case entry.Name() == ".lock":
			if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("attempt checkpoint lock is not a regular file")
			}
			continue
		case isCheckpointTemporaryFileName(entry.Name()):
			temporaryCount++
			if temporaryCount > maxCheckpointTemporaryFiles {
				return nil, fmt.Errorf(
					"attempt checkpoints exceed maximum temporary file count %d",
					maxCheckpointTemporaryFiles,
				)
			}
			if err := validateCheckpointTemporaryFile(filepath.Join(dir, entry.Name())); err != nil {
				return nil, err
			}
			continue
		case entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json"):
			return nil, fmt.Errorf(
				"attempt checkpoint directory contains unexpected entry %q",
				entry.Name(),
			)
		}
		checkpoint, found, err := readCheckpoint(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		if !found || checkpoint.Operation.Identity != identity {
			return nil, fmt.Errorf("checkpoint file %s has mismatched attempt identity", entry.Name())
		}
		if entry.Name() != operationFileName(checkpoint.Operation) {
			return nil, fmt.Errorf(
				"checkpoint file %s does not match embedded operation key %q",
				entry.Name(),
				checkpoint.Operation.Key,
			)
		}
		if _, exists := seen[checkpoint.Operation.Key]; exists {
			return nil, fmt.Errorf("checkpoint operation key %q is duplicated", checkpoint.Operation.Key)
		}
		seen[checkpoint.Operation.Key] = struct{}{}
		if len(result) >= model.MaxOperationsPerReport {
			return nil, fmt.Errorf(
				"attempt checkpoints exceed maximum count %d",
				model.MaxOperationsPerReport,
			)
		}
		result = append(result, checkpoint)
	}
	sortCheckpoints(result)
	return result, nil
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
