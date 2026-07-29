//go:build unix

package filelock

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenPrivateRejectsPermissiveUnixMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenPrivate(path, os.O_RDWR); err == nil {
		t.Fatal("OpenPrivate() accepted permissive Unix mode")
	}
}
