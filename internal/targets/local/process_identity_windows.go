//go:build windows

package local

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func processIdentity(pid int) (string, error) {
	if pid <= 0 {
		return "", fmt.Errorf("invalid process id %d", pid)
	}
	handle, err := windows.OpenProcess(
		windows.PROCESS_QUERY_LIMITED_INFORMATION,
		false,
		uint32(pid),
	)
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return "", os.ErrProcessDone
	}
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle) //nolint:errcheck
	return windowsProcessIdentity(handle)
}
