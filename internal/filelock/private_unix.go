//go:build unix

package filelock

import (
	"os"

	"golang.org/x/sys/unix"
)

func openPrivateFile(path string, flags int, mode os.FileMode) (*os.File, error) {
	return os.OpenFile(path, flags|unix.O_CLOEXEC|unix.O_NOFOLLOW, mode)
}

func validatePrivatePathPermissions(path string, info os.FileInfo) error {
	return validatePrivateMode(path, info)
}

func validatePrivateDescriptorPermissions(path string, _ *os.File, info os.FileInfo) error {
	return validatePrivateMode(path, info)
}
