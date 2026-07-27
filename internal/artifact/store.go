package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/r314tive/pgdrill/internal/filelock"
	"github.com/r314tive/pgdrill/internal/model"
)

type Sink interface {
	Put(ctx context.Context, metadata model.ArtifactMetadata, content io.Reader) (model.ArtifactRef, error)
}

type Store interface {
	Sink
	Read(ctx context.Context, ref model.ArtifactRef) ([]byte, error)
}

func PathForReport(reportPath string) string {
	return filepath.Clean(reportPath) + ".artifacts"
}

type MemoryStore struct {
	mu    sync.RWMutex
	blobs map[string][]byte
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{blobs: map[string][]byte{}}
}

func (s *MemoryStore) Put(ctx context.Context, metadata model.ArtifactMetadata, content io.Reader) (model.ArtifactRef, error) {
	if s == nil {
		return model.ArtifactRef{}, fmt.Errorf("memory artifact store is required")
	}
	payload, err := readAllBounded(ctx, metadata, content, model.MaxArtifactBytes)
	if err != nil {
		return model.ArtifactRef{}, err
	}
	digest := sha256.Sum256(payload)
	hexDigest := hex.EncodeToString(digest[:])
	ref, err := model.NewArtifactRef(
		"sha256:"+hexDigest,
		"memory://sha256/"+hexDigest,
		int64(len(payload)),
		metadata,
	)
	if err != nil {
		return model.ArtifactRef{}, fmt.Errorf("create artifact reference: %w", err)
	}
	s.mu.Lock()
	if s.blobs == nil {
		s.blobs = map[string][]byte{}
	}
	if _, exists := s.blobs[ref.ID]; !exists {
		s.blobs[ref.ID] = append([]byte(nil), payload...)
	}
	s.mu.Unlock()
	return ref, nil
}

func (s *MemoryStore) Read(ctx context.Context, ref model.ArtifactRef) ([]byte, error) {
	if s == nil {
		return nil, fmt.Errorf("memory artifact store is required")
	}
	if err := ref.Validate(); err != nil {
		return nil, fmt.Errorf("validate artifact reference: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	hexDigest := strings.TrimPrefix(ref.ID, "sha256:")
	if ref.URI != "memory://sha256/"+hexDigest {
		return nil, fmt.Errorf("artifact %s uri does not belong to the memory store", ref.ID)
	}
	s.mu.RLock()
	payload, found := s.blobs[ref.ID]
	payload = append([]byte(nil), payload...)
	s.mu.RUnlock()
	if !found {
		return nil, fmt.Errorf("artifact %q was not found", ref.ID)
	}
	if err := verifyPayload(ref, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// DirectoryStore keeps immutable blobs under sha256/<prefix>/<digest>. URIBase
// is a portable path relative to the report that owns the references. When it
// is empty, the artifact directory base name is used.
type DirectoryStore struct {
	Path     string
	URIBase  string
	MaxBytes int64
	gcHook   func(step string, index int) error
}

func (s DirectoryStore) Put(ctx context.Context, metadata model.ArtifactMetadata, content io.Reader) (model.ArtifactRef, error) {
	settings, err := s.settings()
	if err != nil {
		return model.ArtifactRef{}, err
	}
	if err := metadata.Validate(); err != nil {
		return model.ArtifactRef{}, fmt.Errorf("validate artifact metadata: %w", err)
	}
	if content == nil {
		return model.ArtifactRef{}, fmt.Errorf("artifact content is required")
	}
	if err := ctx.Err(); err != nil {
		return model.ArtifactRef{}, err
	}
	var ref model.ArtifactRef
	err = withStoreLock(ctx, settings.root, filelock.Exclusive, true, func() error {
		state, err := inspectGCState(settings.root)
		if err != nil {
			return err
		}
		if err := requireCleanGCStateValues(state); err != nil {
			return err
		}
		if err := ensureStoreMetadata(ctx, settings); err != nil {
			return err
		}
		file, err := os.CreateTemp(settings.root, ".artifact-*.tmp")
		if err != nil {
			return fmt.Errorf("create temporary artifact: %w", err)
		}
		tmpPath := file.Name()
		defer os.Remove(tmpPath) //nolint:errcheck
		if err := file.Chmod(0o600); err != nil {
			_ = file.Close()
			return fmt.Errorf("chmod temporary artifact: %w", err)
		}

		hasher := sha256.New()
		limited := io.LimitReader(&contextReader{ctx: ctx, reader: content}, settings.maxBytes+1)
		size, copyErr := io.CopyBuffer(io.MultiWriter(file, hasher), limited, make([]byte, 32<<10))
		if copyErr != nil {
			_ = file.Close()
			return fmt.Errorf("write temporary artifact: %w", copyErr)
		}
		if size > settings.maxBytes {
			_ = file.Close()
			return fmt.Errorf("artifact exceeds %d bytes", settings.maxBytes)
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return fmt.Errorf("sync temporary artifact: %w", err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("close temporary artifact: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		hexDigest := hex.EncodeToString(hasher.Sum(nil))
		ref, err = model.NewArtifactRef(
			"sha256:"+hexDigest,
			settings.uri(hexDigest),
			size,
			metadata,
		)
		if err != nil {
			return fmt.Errorf("create artifact reference: %w", err)
		}
		digestRoot := filepath.Join(settings.root, blobDirectoryName)
		if err := ensureDirectory(digestRoot); err != nil {
			return fmt.Errorf("create artifact digest directory: %w", err)
		}
		finalDir := filepath.Join(digestRoot, hexDigest[:2])
		if err := ensureDirectory(finalDir); err != nil {
			return fmt.Errorf("create content-addressed artifact directory: %w", err)
		}
		finalPath := filepath.Join(finalDir, hexDigest)
		exists, err := privateRegularFileExists(finalPath)
		if err != nil {
			return err
		}
		if !exists {
			count, err := countStoredBlobs(digestRoot)
			if err != nil {
				return err
			}
			if count >= MaxStoreBlobs {
				return fmt.Errorf(
					"artifact store exceeds maximum blob count %d",
					MaxStoreBlobs,
				)
			}
		}
		if err := os.Link(tmpPath, finalPath); err != nil {
			if !errors.Is(err, os.ErrExist) {
				return fmt.Errorf("publish artifact %s: %w", ref.ID, err)
			}
			if err := verifyArtifactFile(ctx, finalPath, ref); err != nil {
				return fmt.Errorf("verify existing artifact %s: %w", ref.ID, err)
			}
		} else if err := syncDirectory(finalDir); err != nil {
			return fmt.Errorf("sync artifact directory: %w", err)
		}
		if err := writeBlobClaim(ctx, settings, ref); err != nil {
			return err
		}
		if err := observeBlob(finalPath); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return model.ArtifactRef{}, err
	}
	return ref, nil
}

func (s DirectoryStore) Read(ctx context.Context, ref model.ArtifactRef) ([]byte, error) {
	settings, err := s.settings()
	if err != nil {
		return nil, err
	}
	if err := ref.Validate(); err != nil {
		return nil, fmt.Errorf("validate artifact reference: %w", err)
	}
	hexDigest := strings.TrimPrefix(ref.ID, "sha256:")
	if ref.URI != settings.uri(hexDigest) {
		return nil, fmt.Errorf("artifact %s uri does not belong to store %q", ref.ID, settings.uriBase)
	}
	var payload []byte
	err = withStoreLock(ctx, settings.root, filelock.Shared, false, func() error {
		if _, _, err := readStoreMetadata(settings); err != nil {
			return err
		}
		digestRoot := filepath.Join(settings.root, blobDirectoryName)
		prefixDir := filepath.Join(digestRoot, hexDigest[:2])
		if err := requireRealDirectory(digestRoot); err != nil {
			return fmt.Errorf("inspect artifact digest directory: %w", err)
		}
		if err := requireRealDirectory(prefixDir); err != nil {
			return fmt.Errorf("inspect artifact prefix directory: %w", err)
		}
		artifactPath := filepath.Join(prefixDir, hexDigest)
		if err := verifyArtifactFile(ctx, artifactPath, ref); err != nil {
			return err
		}
		file, err := os.Open(artifactPath)
		if err != nil {
			return fmt.Errorf("open artifact %s: %w", ref.ID, err)
		}
		defer file.Close()
		payload, err = io.ReadAll(io.LimitReader(&contextReader{ctx: ctx, reader: file}, ref.SizeBytes+1))
		if err != nil {
			return fmt.Errorf("read artifact %s: %w", ref.ID, err)
		}
		return verifyPayload(ref, payload)
	})
	if err != nil {
		return nil, err
	}
	return payload, nil
}

type directorySettings struct {
	root     string
	uriBase  string
	maxBytes int64
}

func (s directorySettings) uri(hexDigest string) string {
	return path.Join(s.uriBase, "sha256", hexDigest[:2], hexDigest)
}

func (s DirectoryStore) settings() (directorySettings, error) {
	if strings.TrimSpace(s.Path) == "" {
		return directorySettings{}, fmt.Errorf("artifact store path is required")
	}
	root := filepath.Clean(s.Path)
	uriBase := strings.TrimSpace(s.URIBase)
	if uriBase == "" {
		uriBase = filepath.Base(root)
	}
	if uriBase == "" || uriBase == "." || strings.HasPrefix(uriBase, "/") || strings.Contains(uriBase, `\`) || path.Clean(uriBase) != uriBase || strings.HasPrefix(uriBase, "../") {
		return directorySettings{}, fmt.Errorf("artifact uri base must be a canonical relative path")
	}
	maxBytes := s.MaxBytes
	if maxBytes == 0 {
		maxBytes = model.MaxArtifactBytes
	}
	if maxBytes < 1 || maxBytes > model.MaxArtifactBytes {
		return directorySettings{}, fmt.Errorf("artifact max bytes must be between 1 and %d", model.MaxArtifactBytes)
	}
	return directorySettings{root: root, uriBase: uriBase, maxBytes: maxBytes}, nil
}

func readAllBounded(ctx context.Context, metadata model.ArtifactMetadata, content io.Reader, maxBytes int64) ([]byte, error) {
	if err := metadata.Validate(); err != nil {
		return nil, fmt.Errorf("validate artifact metadata: %w", err)
	}
	if content == nil {
		return nil, fmt.Errorf("artifact content is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	payload, err := io.ReadAll(io.LimitReader(&contextReader{ctx: ctx, reader: content}, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read artifact content: %w", err)
	}
	if int64(len(payload)) > maxBytes {
		return nil, fmt.Errorf("artifact exceeds %d bytes", maxBytes)
	}
	return payload, nil
}

func verifyArtifactFile(ctx context.Context, artifactPath string, ref model.ArtifactRef) error {
	info, err := os.Lstat(artifactPath)
	if err != nil {
		return fmt.Errorf("inspect artifact %s: %w", ref.ID, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("artifact %s is not a regular non-symbolic-link file", ref.ID)
	}
	if info.Size() != ref.SizeBytes || info.Size() > model.MaxArtifactBytes {
		return fmt.Errorf("artifact %s size does not match its reference", ref.ID)
	}
	file, err := os.Open(artifactPath)
	if err != nil {
		return fmt.Errorf("open artifact %s: %w", ref.ID, err)
	}
	hasher := sha256.New()
	_, copyErr := io.CopyBuffer(hasher, &contextReader{ctx: ctx, reader: file}, make([]byte, 32<<10))
	closeErr := file.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return fmt.Errorf("hash artifact %s: %w", ref.ID, err)
	}
	if "sha256:"+hex.EncodeToString(hasher.Sum(nil)) != ref.ID {
		return fmt.Errorf("artifact %s content digest does not match its reference", ref.ID)
	}
	return nil
}

func verifyPayload(ref model.ArtifactRef, payload []byte) error {
	if int64(len(payload)) != ref.SizeBytes {
		return fmt.Errorf("artifact %s size does not match its reference", ref.ID)
	}
	digest := sha256.Sum256(payload)
	if "sha256:"+hex.EncodeToString(digest[:]) != ref.ID {
		return fmt.Errorf("artifact %s content digest does not match its reference", ref.ID)
	}
	return nil
}

func ensureDirectory(directoryPath string) error {
	if err := os.MkdirAll(directoryPath, 0o700); err != nil {
		return err
	}
	return requireRealDirectory(directoryPath)
}

func requireRealDirectory(directoryPath string) error {
	info, err := os.Lstat(directoryPath)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("path is not a real directory: %s", directoryPath)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("directory permissions %o are not private: %s", info.Mode().Perm(), directoryPath)
	}
	return nil
}

func syncDirectory(directoryPath string) error {
	directory, err := os.Open(directoryPath)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

var _ Store = (*MemoryStore)(nil)
var _ Store = DirectoryStore{}
