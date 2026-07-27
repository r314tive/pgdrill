//go:build windows

package local

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

const windowsStillActive = 259

func postgresProcessExists(pid int) (bool, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer windows.CloseHandle(handle) //nolint:errcheck
	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		return false, err
	}
	return exitCode == windowsStillActive, nil
}

func terminateStartedProcess(process *os.Process) error {
	return process.Kill()
}

func terminateProcessByPID(pid int) error {
	return terminateWindowsProcess(pid)
}

func killProcessByPID(pid int) error {
	return terminateWindowsProcess(pid)
}

func terminateWindowsProcess(pid int) error {
	exists, err := postgresProcessExists(pid)
	if err != nil {
		return err
	}
	if !exists {
		return os.ErrProcessDone
	}
	handle, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return os.ErrProcessDone
	}
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle) //nolint:errcheck
	if err := windows.TerminateProcess(handle, 1); err != nil {
		if exists, existsErr := postgresProcessExists(pid); existsErr == nil && !exists {
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}
