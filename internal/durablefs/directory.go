package durablefs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

var syncDirectory = SyncDirectory

// MkdirAll creates each missing path component and persists every new
// directory entry in its parent before returning.
func MkdirAll(path string, mode fs.FileMode) error {
	path = filepath.Clean(path)
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("%s is not a real directory", path)
		}
		if err := validateDirectoryChain(path); err != nil {
			return err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return nil
		}
		return syncDirectory(parent)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	parent := filepath.Dir(path)
	if parent != path {
		if err := MkdirAll(parent, mode); err != nil {
			return err
		}
	}
	if err := os.Mkdir(path, mode); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("%s is not a real directory", path)
		}
	}
	return syncDirectory(parent)
}

func validateDirectoryChain(path string) error {
	path = filepath.Clean(path)
	parent := filepath.Dir(path)
	if parent != path {
		if err := validateDirectoryChain(parent); err != nil {
			return err
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		// macOS exposes system paths such as /var and /tmp through root-owned
		// aliases. Only a filesystem-root child is outside an unprivileged
		// caller's ownership boundary.
		if filepath.Dir(parent) != parent {
			return fmt.Errorf("%s is not a real directory", path)
		}
		info, err = os.Stat(path)
		if err != nil {
			return err
		}
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a real directory", path)
	}
	return nil
}

func SyncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	return errors.Join(syncErr, closeErr)
}

// SyncRename persists a completed rename. The destination directory must be
// durable before the source directory deletion is made durable.
func SyncRename(source, target string) error {
	sourceParent := filepath.Dir(filepath.Clean(source))
	targetParent := filepath.Dir(filepath.Clean(target))
	if err := syncDirectory(targetParent); err != nil {
		return err
	}
	if sourceParent == targetParent {
		return nil
	}
	return syncDirectory(sourceParent)
}

func Remove(path string) error {
	err := os.Remove(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return syncParentIfPresent(path)
		}
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func RemoveAll(path string) error {
	if _, err := os.Lstat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return syncParentIfPresent(path)
		}
		return err
	}
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func syncParentIfPresent(path string) error {
	parent := filepath.Dir(filepath.Clean(path))
	info, err := os.Lstat(parent)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%s is not a real directory", parent)
	}
	return syncDirectory(parent)
}
