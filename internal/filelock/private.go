package filelock

import (
	"errors"
	"fmt"
	"os"
)

// OpenPrivate opens a file only when the path and opened descriptor refer
// to the same private regular file. Platform implementations prevent following
// the final path component where the operating system supports it.
func OpenPrivate(path string, flags int) (*os.File, error) {
	info, err := os.Lstat(path)
	switch {
	case err == nil:
		if err := validatePrivateFileInfo(path, info); err != nil {
			return nil, err
		}
		if err := validatePrivatePathPermissions(path, info); err != nil {
			return nil, err
		}
	case errors.Is(err, os.ErrNotExist):
		if flags&os.O_CREATE == 0 {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("inspect private file %s: %w", path, err)
	}

	file, err := openPrivateFile(path, flags, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open private file %s: %w", path, err)
	}
	pathInfo, pathErr := os.Lstat(path)
	fileInfo, fileErr := file.Stat()
	if err := errors.Join(pathErr, fileErr); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("bind private file %s: %w", path, err)
	}
	if err := validatePrivateFileInfo(path, pathInfo); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := validatePrivatePathPermissions(path, pathInfo); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := validatePrivateFileInfo(path, fileInfo); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := validatePrivateDescriptorPermissions(path, file, fileInfo); err != nil {
		_ = file.Close()
		return nil, err
	}
	if !os.SameFile(pathInfo, fileInfo) {
		_ = file.Close()
		return nil, fmt.Errorf("private file changed while opening: %s", path)
	}
	return file, nil
}

func validatePrivateFileInfo(path string, info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf(
			"private file must not be a symbolic link; it is not a regular non-symbolic-link file: %s",
			path,
		)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("private file is not a regular non-symbolic-link file: %s", path)
	}
	return nil
}

func validatePrivateMode(path string, info os.FileInfo) error {
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf(
			"private file permissions %o are not private: %s",
			info.Mode().Perm(),
			path,
		)
	}
	return nil
}
