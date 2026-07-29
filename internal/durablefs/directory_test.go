package durablefs

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestMkdirAllCreatesPrivateNestedDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "one", "two")

	if err := MkdirAll(path, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("created directory mode = %v", info.Mode())
	}
}

func TestMkdirAllRejectsSymlinkComponent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require additional privileges")
	}
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(filepath.Join(target, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	if err := MkdirAll(filepath.Join(link, "child"), 0o700); err == nil {
		t.Fatal("MkdirAll() accepted a symlink path component")
	}
}

func TestMkdirAllRetriesParentSyncForExistingDirectory(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "child")
	originalSync := syncDirectory
	t.Cleanup(func() { syncDirectory = originalSync })
	fail := true
	syncCalls := 0
	syncDirectory = func(path string) error {
		if filepath.Clean(path) == filepath.Clean(root) {
			syncCalls++
			if fail {
				fail = false
				return os.ErrInvalid
			}
		}
		return originalSync(path)
	}

	if err := MkdirAll(path, 0o700); !errors.Is(err, os.ErrInvalid) {
		t.Fatalf("MkdirAll(first) error = %v, want injected sync failure", err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("directory was not created before sync failure: %v", err)
	}
	if err := MkdirAll(path, 0o700); err != nil {
		t.Fatalf("MkdirAll(retry) error = %v", err)
	}
	if syncCalls != 2 {
		t.Fatalf("parent sync calls = %d, want 2", syncCalls)
	}
}

func TestRemoveRetriesParentSyncWhenPathIsAlreadyAbsent(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "file")
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	originalSync := syncDirectory
	t.Cleanup(func() { syncDirectory = originalSync })
	fail := true
	syncCalls := 0
	syncDirectory = func(path string) error {
		if filepath.Clean(path) == filepath.Clean(root) {
			syncCalls++
			if fail {
				fail = false
				return os.ErrInvalid
			}
		}
		return originalSync(path)
	}

	if err := Remove(path); !errors.Is(err, os.ErrInvalid) {
		t.Fatalf("Remove(first) error = %v, want injected sync failure", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("file remains after remove: %v", err)
	}
	if err := Remove(path); err != nil {
		t.Fatalf("Remove(retry) error = %v", err)
	}
	if syncCalls != 2 {
		t.Fatalf("parent sync calls = %d, want 2", syncCalls)
	}
}

func TestSyncRenamePersistsDestinationBeforeSource(t *testing.T) {
	root := t.TempDir()
	sourceParent := filepath.Join(root, "active")
	targetParent := filepath.Join(root, "trash")
	originalSync := syncDirectory
	t.Cleanup(func() { syncDirectory = originalSync })
	var calls []string
	syncDirectory = func(path string) error {
		calls = append(calls, filepath.Clean(path))
		if filepath.Clean(path) == sourceParent {
			return os.ErrInvalid
		}
		return nil
	}

	err := SyncRename(
		filepath.Join(sourceParent, "entry"),
		filepath.Join(targetParent, "entry"),
	)
	if !errors.Is(err, os.ErrInvalid) {
		t.Fatalf("SyncRename() error = %v, want injected source sync failure", err)
	}
	want := []string{targetParent, sourceParent}
	if len(calls) != len(want) || calls[0] != want[0] || calls[1] != want[1] {
		t.Fatalf("SyncRename() calls = %#v, want %#v", calls, want)
	}
}

func TestRemoveAllRemovesTree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tree")
	if err := MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := RemoveAll(path); err != nil {
		t.Fatalf("RemoveAll() error = %v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("removed tree stat error = %v", err)
	}
}
