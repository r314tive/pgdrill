package history

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r314tive/pgdrill/internal/model"
)

const (
	compatibilityFixtureMaxBytes = 128 << 20
	compatibilityFixtureSHA256   = "dc44cbb9a86f2911f049ca09bb3ff505915a8e86780794a0b0fe4e6791084d5b"
)

func TestDirectoryStoreReadsPreGACompatibilityFloor(t *testing.T) {
	t.Parallel()

	archivePath := filepath.Join("testdata", PreGACompatibilityFloor, "history-store.tar.gz")
	payload, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("read compatibility fixture: %v", err)
	}
	sum := sha256.Sum256(payload)
	if digest := hex.EncodeToString(sum[:]); digest != compatibilityFixtureSHA256 {
		t.Fatalf("compatibility fixture SHA-256 = %s, want %s", digest, compatibilityFixtureSHA256)
	}
	storePath := extractHistoryFixture(t, archivePath)
	store := DirectoryStore{Path: storePath}
	verification, err := store.Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify() compatibility fixture error = %v", err)
	}
	if verification.StoreSchemaVersion != LegacyStoreSchemaVersion ||
		!verification.MigrationRequired ||
		verification.MaintenanceRequired {
		t.Fatalf("Verify() compatibility fixture = %#v", verification)
	}

	summaries, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List() compatibility fixture error = %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("List() compatibility fixture attempts = %d, want 2", len(summaries))
	}
	for _, summary := range summaries {
		if summary.Status != model.DrillStatusPassed || !summary.ReportAvailable || summary.EventCount != 26 {
			t.Fatalf("compatibility fixture summary = %#v", summary)
		}
		record, err := store.ShowAttempt(context.Background(), summary.RunID, summary.AttemptID)
		if err != nil {
			t.Fatalf("ShowAttempt(%q) compatibility fixture error = %v", summary.RunID, err)
		}
		if len(record.Attempts) != 1 {
			t.Fatalf("ShowAttempt(%q) attempts = %d, want 1", summary.RunID, len(record.Attempts))
		}
		attempt := record.Attempts[0]
		if attempt.Report == nil || attempt.Report.Status != model.DrillStatusPassed {
			t.Fatalf("ShowAttempt(%q) report = %#v", summary.RunID, attempt.Report)
		}
		if len(attempt.Events) != 26 || attempt.Events[len(attempt.Events)-1].Type != model.RunEventFinished {
			t.Fatalf("ShowAttempt(%q) events = %#v", summary.RunID, attempt.Events)
		}
		if !strings.Contains(attempt.Report.PGDrillVersion, PreGACompatibilityFloor) {
			t.Fatalf(
				"ShowAttempt(%q) pgdrill_version = %q, want compatibility floor %q",
				summary.RunID,
				attempt.Report.PGDrillVersion,
				PreGACompatibilityFloor,
			)
		}
	}
}

func extractHistoryFixture(t *testing.T, archivePath string) string {
	t.Helper()

	archive, err := os.Open(archivePath)
	if err != nil {
		t.Fatalf("open compatibility fixture: %v", err)
	}
	defer archive.Close()
	compressed, err := gzip.NewReader(archive)
	if err != nil {
		t.Fatalf("open compressed compatibility fixture: %v", err)
	}
	defer compressed.Close()

	destination := t.TempDir()
	reader := tar.NewReader(compressed)
	var totalBytes int64
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read compatibility fixture: %v", err)
		}
		cleanName := filepath.Clean(filepath.FromSlash(header.Name))
		if cleanName != "history" && !strings.HasPrefix(cleanName, "history"+string(filepath.Separator)) {
			t.Fatalf("compatibility fixture contains unsafe path %q", header.Name)
		}
		target := filepath.Join(destination, cleanName)
		if !strings.HasPrefix(target, destination+string(filepath.Separator)) {
			t.Fatalf("compatibility fixture path escapes destination: %q", header.Name)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				t.Fatalf("create compatibility fixture directory: %v", err)
			}
			if err := os.Chmod(target, 0o700); err != nil {
				t.Fatalf("chmod compatibility fixture directory: %v", err)
			}
		case tar.TypeReg:
			totalBytes += header.Size
			if totalBytes > compatibilityFixtureMaxBytes {
				t.Fatalf("compatibility fixture exceeds %d bytes", compatibilityFixtureMaxBytes)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				t.Fatalf("create compatibility fixture parent: %v", err)
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				t.Fatalf("create compatibility fixture file: %v", err)
			}
			written, copyErr := io.CopyN(file, reader, header.Size)
			closeErr := file.Close()
			if copyErr != nil {
				t.Fatalf("extract compatibility fixture file: %v", copyErr)
			}
			if closeErr != nil {
				t.Fatalf("close compatibility fixture file: %v", closeErr)
			}
			if written != header.Size {
				t.Fatalf("extract compatibility fixture file: wrote %d bytes, want %d", written, header.Size)
			}
		default:
			t.Fatalf("compatibility fixture contains unsupported entry %q (type %d)", header.Name, header.Typeflag)
		}
	}
	storePath := filepath.Join(destination, "history")
	if err := requireDirectory(storePath); err != nil {
		t.Fatalf("validate extracted compatibility fixture: %v", err)
	}
	return storePath
}

func ExamplePreGACompatibilityFloor() {
	fmt.Println(PreGACompatibilityFloor)
	// Output: v0.3.0-alpha.1
}
