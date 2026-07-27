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
