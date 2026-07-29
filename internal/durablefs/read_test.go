package durablefs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadDirBounded(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a", "b", "c"} {
		if err := os.WriteFile(filepath.Join(root, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	entries, err := ReadDirBounded(root, 3)
	if err != nil {
		t.Fatalf("ReadDirBounded(exact) error = %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("ReadDirBounded(exact) entries = %d, want 3", len(entries))
	}
	if _, err := ReadDirBounded(root, 2); err == nil ||
		!strings.Contains(err.Error(), "exceeds maximum entry count 2") {
		t.Fatalf("ReadDirBounded(over limit) error = %v", err)
	} else {
		var limitErr *DirectoryLimitError
		if !errors.As(err, &limitErr) || limitErr.Path != root || limitErr.Limit != 2 {
			t.Fatalf("ReadDirBounded(over limit) typed error = %#v", err)
		}
	}
	if _, err := ReadDirBounded(root, -1); err == nil {
		t.Fatal("ReadDirBounded() accepted a negative limit")
	}
	if _, err := ReadDirBounded(root, int(^uint(0)>>1)); err == nil ||
		!strings.Contains(err.Error(), "too large") {
		t.Fatalf("ReadDirBounded(MaxInt) error = %v", err)
	}
}
