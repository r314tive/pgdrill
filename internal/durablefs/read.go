package durablefs

import (
	"errors"
	"fmt"
	"io"
	"os"
)

type DirectoryLimitError struct {
	Path  string
	Limit int
}

func (e *DirectoryLimitError) Error() string {
	if e == nil {
		return "directory entry limit exceeded"
	}
	return fmt.Sprintf(
		"directory %s exceeds maximum entry count %d",
		e.Path,
		e.Limit,
	)
}

// ReadDirBounded reads at most limit directory entries and fails before
// materializing an unbounded directory listing.
func ReadDirBounded(path string, limit int) ([]os.DirEntry, error) {
	if limit < 0 {
		return nil, fmt.Errorf("directory entry limit must not be negative")
	}
	if limit == int(^uint(0)>>1) {
		return nil, fmt.Errorf("directory entry limit is too large")
	}
	directory, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	entries, readErr := directory.ReadDir(limit + 1)
	closeErr := directory.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, errors.Join(readErr, closeErr)
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(entries) > limit {
		return nil, &DirectoryLimitError{Path: path, Limit: limit}
	}
	return entries, nil
}
