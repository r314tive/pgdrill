//go:build unix

package local

import (
	"fmt"
	"os"
	"syscall"
)

func regularFileLinkCount(file *os.File) (uint64, error) {
	info, err := file.Stat()
	if err != nil {
		return 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("file metadata does not expose link count")
	}
	return uint64(stat.Nlink), nil
}
