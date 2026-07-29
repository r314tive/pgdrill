package filelock

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLockSerializesExclusiveHoldersAndHonorsContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	first, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	if err := Lock(context.Background(), first, Exclusive); err != nil {
		t.Fatalf("Lock(first) error = %v", err)
	}
	defer Unlock(first) //nolint:errcheck

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := Lock(ctx, second, Shared); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Lock(contended) error = %v, want context deadline", err)
	}
	if err := Unlock(first); err != nil {
		t.Fatalf("Unlock(first) error = %v", err)
	}
	if err := Lock(context.Background(), second, Exclusive); err != nil {
		t.Fatalf("Lock(second) error = %v", err)
	}
	if err := Unlock(second); err != nil {
		t.Fatalf("Unlock(second) error = %v", err)
	}
}

func TestOpenPrivateRejectsUnsafePaths(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "lock")
	file, err := OpenPrivate(path, os.O_CREATE|os.O_RDWR)
	if err != nil {
		t.Fatalf("OpenPrivate(create) error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenPrivate(path, os.O_RDWR)
	if err != nil {
		t.Fatalf("OpenPrivate(reopen) error = %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.WriteFile(outside, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Skipf("symbolic links are unavailable: %v", err)
	}
	if _, err := OpenPrivate(path, os.O_CREATE|os.O_RDWR); err == nil {
		t.Fatal("OpenPrivate() accepted symbolic link")
	}
	payload, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "unchanged" {
		t.Fatalf("outside file changed to %q", payload)
	}
}
