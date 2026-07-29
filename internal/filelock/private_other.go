//go:build !unix && !windows

package filelock

import "os"

func openPrivateFile(path string, flags int, mode os.FileMode) (*os.File, error) {
	return os.OpenFile(path, flags, mode)
}

func validatePrivatePathPermissions(path string, info os.FileInfo) error {
	return validatePrivateMode(path, info)
}

func validatePrivateDescriptorPermissions(path string, _ *os.File, info os.FileInfo) error {
	return validatePrivateMode(path, info)
}
