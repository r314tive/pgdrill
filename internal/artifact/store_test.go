package artifact

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/r314tive/pgdrill/internal/model"
)

func TestDirectoryStorePersistsDeduplicatesAndReadsArtifact(t *testing.T) {
	root := filepath.Join(t.TempDir(), "report.json.artifacts")
	store := DirectoryStore{Path: root}
	metadata := testMetadata(t)
	payload := []byte("apiVersion: postgresql.cnpg.io/v1\nkind: Cluster\n")

	first, err := store.Put(context.Background(), metadata, strings.NewReader(string(payload)))
	if err != nil {
		t.Fatalf("Put(first) error = %v", err)
	}
	second, err := store.Put(context.Background(), metadata, strings.NewReader(string(payload)))
	if err != nil {
		t.Fatalf("Put(second) error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("deduplicated references differ: first=%#v second=%#v", first, second)
	}
	if !strings.HasPrefix(first.URI, "report.json.artifacts/sha256/") || first.SizeBytes != int64(len(payload)) {
		t.Fatalf("unexpected artifact reference %#v", first)
	}

	loaded, err := store.Read(context.Background(), first)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if string(loaded) != string(payload) {
		t.Fatalf("Read() = %q, want %q", loaded, payload)
	}
	hexDigest := strings.TrimPrefix(first.ID, "sha256:")
	info, err := os.Stat(filepath.Join(root, "sha256", hexDigest[:2], hexDigest))
	if err != nil {
		t.Fatalf("stat artifact: %v", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("artifact file must be private, got %s", info.Mode().Perm())
	}
}

func TestDirectoryStoreRejectsOversizedArtifactWithoutPublishingBlob(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts")
	store := DirectoryStore{Path: root, MaxBytes: 4}
	_, err := store.Put(context.Background(), testMetadata(t), strings.NewReader("12345"))
	if err == nil || !strings.Contains(err.Error(), "exceeds 4 bytes") {
		t.Fatalf("Put() error = %v", err)
	}

	regularFiles := 0
	walkErr := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			regularFiles++
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk artifact root: %v", walkErr)
	}
	if regularFiles != 0 {
		t.Fatalf("oversized artifact published %d regular files", regularFiles)
	}
}

func TestDirectoryStoreCancellationDoesNotCreateStore(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (DirectoryStore{Path: root}).Put(ctx, testMetadata(t), strings.NewReader("payload"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Put() error = %v, want cancellation", err)
	}
	if _, statErr := os.Stat(root); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("canceled artifact write created root: %v", statErr)
	}
}

func TestDirectoryStoreDetectsCorruptedAndSymbolicLinkArtifacts(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts")
	store := DirectoryStore{Path: root}
	ref, err := store.Put(context.Background(), testMetadata(t), strings.NewReader("original"))
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	hexDigest := strings.TrimPrefix(ref.ID, "sha256:")
	artifactPath := filepath.Join(root, "sha256", hexDigest[:2], hexDigest)
	if err := os.WriteFile(artifactPath, []byte("tampered"), 0o600); err != nil {
		t.Fatalf("tamper artifact: %v", err)
	}
	if _, err := store.Read(context.Background(), ref); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("Read(tampered) error = %v", err)
	}

	if err := os.Remove(artifactPath); err != nil {
		t.Fatalf("remove artifact: %v", err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatalf("write symlink target: %v", err)
	}
	if err := os.Symlink(target, artifactPath); err != nil {
		t.Fatalf("create artifact symlink: %v", err)
	}
	if _, err := store.Read(context.Background(), ref); err == nil || !strings.Contains(err.Error(), "non-symbolic-link") {
		t.Fatalf("Read(symlink) error = %v", err)
	}
}

func TestDirectoryStoreConcurrentPutPublishesOneImmutableBlob(t *testing.T) {
	store := DirectoryStore{Path: filepath.Join(t.TempDir(), "artifacts")}
	metadata := testMetadata(t)
	const writers = 12
	refs := make([]model.ArtifactRef, writers)
	errs := make([]error, writers)
	var group sync.WaitGroup
	for index := range writers {
		group.Add(1)
		go func() {
			defer group.Done()
			refs[index], errs[index] = store.Put(context.Background(), metadata, strings.NewReader("same immutable payload"))
		}()
	}
	group.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("Put(%d) error = %v", index, err)
		}
		if refs[index] != refs[0] {
			t.Fatalf("Put(%d) ref = %#v, want %#v", index, refs[index], refs[0])
		}
	}
}

func TestMemoryStoreRequiresClassificationBeforeReadingContent(t *testing.T) {
	reader := &countingReader{reader: strings.NewReader("secret")}
	_, err := NewMemoryStore().Put(context.Background(), model.ArtifactMetadata{
		MediaType:      "text/plain",
		RetentionClass: model.ArtifactRetentionHistory,
		RedactionState: "unredacted",
	}, reader)
	if err == nil || !strings.Contains(err.Error(), "redaction_state") {
		t.Fatalf("Put() error = %v", err)
	}
	if reader.reads != 0 {
		t.Fatalf("invalid metadata consumed content %d times", reader.reads)
	}
}

func testMetadata(t *testing.T) model.ArtifactMetadata {
	t.Helper()
	metadata, err := model.NewArtifactMetadata("application/yaml", model.ArtifactRetentionHistory, model.ArtifactRedactionNotRequired)
	if err != nil {
		t.Fatalf("NewArtifactMetadata() error = %v", err)
	}
	return metadata
}

type countingReader struct {
	reader *strings.Reader
	reads  int
}

func (r *countingReader) Read(buffer []byte) (int, error) {
	r.reads++
	return r.reader.Read(buffer)
}
