package artifact

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/r314tive/pgdrill/internal/filelock"
	"github.com/r314tive/pgdrill/internal/model"
)

func withStoreLock(
	ctx context.Context,
	root string,
	mode filelock.Mode,
	createRoot bool,
	operation func() error,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if createRoot {
		if err := ensureDirectory(root); err != nil {
			return fmt.Errorf("create artifact store: %w", err)
		}
	} else if err := requireRealDirectory(root); err != nil {
		return fmt.Errorf("inspect artifact store: %w", err)
	}

	lockPath := filepath.Join(root, storeLockFileName)
	if info, err := os.Lstat(lockPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("artifact store lock must be a regular non-symbolic-link file")
		}
		if info.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("artifact store lock permissions %o are not private", info.Mode().Perm())
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect artifact store lock: %w", err)
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open artifact store lock: %w", err)
	}
	defer lock.Close()
	if err := filelock.Lock(ctx, lock, mode); err != nil {
		return fmt.Errorf("lock artifact store: %w", err)
	}
	defer filelock.Unlock(lock) //nolint:errcheck
	if err := ctx.Err(); err != nil {
		return err
	}
	return operation()
}

func ensureStoreMetadata(ctx context.Context, settings directorySettings) error {
	path := filepath.Join(settings.root, storeMetadataFileName)
	stored, err := readJSONFile[StoreMetadata](path, maxStoreJSONBytes)
	if err == nil {
		if err := stored.validate(settings.uriBase); err != nil {
			return err
		}
		if stored.SchemaVersion == LegacyStoreSchemaVersion {
			return fmt.Errorf(
				"artifact store schema_version %q is read-only; copy retained blobs into a stable store before writing",
				stored.SchemaVersion,
			)
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read artifact store metadata: %w", err)
	}
	metadata := StoreMetadata{
		SchemaVersion: CurrentStoreSchemaVersion,
		LayoutVersion: CurrentLayoutVersion,
		URIBase:       settings.uriBase,
	}
	payload, err := marshalBoundedJSON(metadata, maxStoreJSONBytes)
	if err != nil {
		return err
	}
	if err := writeImmutableFile(ctx, settings.root, path, payload, maxStoreJSONBytes); err != nil {
		return fmt.Errorf("persist artifact store metadata: %w", err)
	}
	stored, err = readJSONFile[StoreMetadata](path, maxStoreJSONBytes)
	if err != nil {
		return fmt.Errorf("read artifact store metadata: %w", err)
	}
	return stored.validate(settings.uriBase)
}

func readStoreMetadata(settings directorySettings) (StoreMetadata, bool, error) {
	metadata, err := readJSONFile[StoreMetadata](
		filepath.Join(settings.root, storeMetadataFileName),
		maxStoreJSONBytes,
	)
	if errors.Is(err, os.ErrNotExist) {
		return StoreMetadata{}, false, nil
	}
	if err != nil {
		return StoreMetadata{}, false, fmt.Errorf("read artifact store metadata: %w", err)
	}
	if err := metadata.validate(settings.uriBase); err != nil {
		return StoreMetadata{}, false, err
	}
	return metadata, true, nil
}

func writeBlobClaim(
	ctx context.Context,
	settings directorySettings,
	ref model.ArtifactRef,
) error {
	claim := newBlobClaim(ref)
	claimDigest, payload, err := claim.digest()
	if err != nil {
		return err
	}
	hexDigest := strings.TrimPrefix(ref.ID, "sha256:")
	claimRoot := filepath.Join(settings.root, claimDirectoryName)
	digestRoot := filepath.Join(claimRoot, blobDirectoryName)
	prefixRoot := filepath.Join(digestRoot, hexDigest[:2])
	blobRoot := filepath.Join(prefixRoot, hexDigest)
	for _, directory := range []string{claimRoot, digestRoot, prefixRoot, blobRoot} {
		if err := ensureDirectory(directory); err != nil {
			return fmt.Errorf("create artifact claim directory: %w", err)
		}
	}
	claimPath := filepath.Join(
		blobRoot,
		strings.TrimPrefix(claimDigest, "sha256:")+".json",
	)
	if _, err := os.Lstat(claimPath); errors.Is(err, os.ErrNotExist) {
		entries, readErr := os.ReadDir(blobRoot)
		if readErr != nil {
			return fmt.Errorf("count artifact blob claims: %w", readErr)
		}
		if len(entries) >= MaxClaimsPerBlob {
			return fmt.Errorf(
				"artifact %s exceeds maximum claim count %d",
				ref.ID,
				MaxClaimsPerBlob,
			)
		}
	} else if err != nil {
		return fmt.Errorf("inspect artifact blob claim: %w", err)
	}
	if err := writeImmutableFile(ctx, blobRoot, claimPath, payload, maxClaimJSONBytes); err != nil {
		return fmt.Errorf("persist artifact blob claim: %w", err)
	}
	return nil
}

func observeBlob(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("artifact blob is not a regular non-symbolic-link file")
	}
	now := time.Now().UTC()
	if err := os.Chtimes(path, now, now); err != nil {
		return fmt.Errorf("update artifact observation time: %w", err)
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open observed artifact: %w", err)
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return fmt.Errorf("sync observed artifact: %w", err)
	}
	return syncDirectory(filepath.Dir(path))
}

func marshalBoundedJSON(value any, maxBytes int) ([]byte, error) {
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	payload = append(payload, '\n')
	if len(payload) > maxBytes {
		return nil, fmt.Errorf("JSON document exceeds %d bytes", maxBytes)
	}
	return payload, nil
}

func readJSONFile[T any](path string, maxBytes int) (T, error) {
	var zero T
	linkInfo, err := os.Lstat(path)
	if err != nil {
		return zero, err
	}
	if linkInfo.Mode()&os.ModeSymlink != 0 || !linkInfo.Mode().IsRegular() {
		return zero, fmt.Errorf("%s must be a private regular non-symbolic-link file", path)
	}
	if linkInfo.Mode().Perm()&0o077 != 0 {
		return zero, fmt.Errorf("%s must be a private regular file", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return zero, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return zero, err
	}
	if !os.SameFile(linkInfo, info) || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return zero, fmt.Errorf("%s must be a private regular file", path)
	}
	payload, err := io.ReadAll(io.LimitReader(file, int64(maxBytes)+1))
	if err != nil {
		return zero, err
	}
	if len(payload) > maxBytes {
		return zero, fmt.Errorf("%s exceeds %d bytes", path, maxBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var value T
	if err := decoder.Decode(&value); err != nil {
		return zero, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return zero, fmt.Errorf("%s contains multiple JSON values", path)
		}
		return zero, err
	}
	return value, nil
}

func writeImmutableFile(
	ctx context.Context,
	dir, path string,
	payload []byte,
	maxBytes int,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(payload) > maxBytes {
		return fmt.Errorf("immutable artifact metadata exceeds %d bytes", maxBytes)
	}
	if err := requireRealDirectory(dir); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".artifact-metadata-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := file.Name()
	defer os.Remove(tmpPath) //nolint:errcheck
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Link(tmpPath, path); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		if err := requirePrivateRegularFile(path, maxBytes); err != nil {
			return err
		}
		existingFile, readErr := os.Open(path)
		if readErr != nil {
			return readErr
		}
		existing, readErr := io.ReadAll(io.LimitReader(existingFile, int64(maxBytes)+1))
		closeErr := existingFile.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return err
		}
		if !bytes.Equal(existing, payload) {
			return fmt.Errorf("immutable file %s already contains different content", path)
		}
		return nil
	}
	return syncDirectory(dir)
}
