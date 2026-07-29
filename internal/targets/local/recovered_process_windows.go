//go:build windows

package local

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"golang.org/x/sys/windows"
)

type windowsRecoveredProcess struct {
	handle windows.Handle
}

func openIdentityBoundProcess(pid int, expectedIdentity string) (recoveredProcessHandle, error) {
	if pid <= 0 || expectedIdentity == "" {
		return nil, fmt.Errorf("recovered process identity is incomplete")
	}
	handle, err := windows.OpenProcess(
		windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE,
		false,
		uint32(pid),
	)
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return nil, os.ErrProcessDone
	}
	if err != nil {
		return nil, fmt.Errorf("open recovered process %d: %w", pid, err)
	}
	process := &windowsRecoveredProcess{handle: handle}
	identity, err := windowsProcessIdentity(handle)
	if err != nil {
		_ = process.Close()
		return nil, err
	}
	if identity != expectedIdentity {
		_ = process.Close()
		return nil, fmt.Errorf("recovered process %d identity does not match its receipt", pid)
	}
	running, err := process.Running()
	if err != nil {
		_ = process.Close()
		return nil, err
	}
	if !running {
		_ = process.Close()
		return nil, os.ErrProcessDone
	}
	return process, nil
}

func windowsProcessIdentity(handle windows.Handle) (string, error) {
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &created, &exited, &kernel, &user); err != nil {
		return "", err
	}
	return "windows-created-ns:" + strconv.FormatInt(created.Nanoseconds(), 10), nil
}

func (p *windowsRecoveredProcess) Running() (bool, error) {
	status, err := windows.WaitForSingleObject(p.handle, 0)
	if err != nil {
		return false, err
	}
	switch status {
	case uint32(windows.WAIT_TIMEOUT):
		return true, nil
	case uint32(windows.WAIT_OBJECT_0):
		return false, nil
	default:
		return false, fmt.Errorf("wait for recovered process returned status %d", status)
	}
}

func (p *windowsRecoveredProcess) Terminate() error {
	return p.terminate()
}

func (p *windowsRecoveredProcess) Kill() error {
	return p.terminate()
}

func (p *windowsRecoveredProcess) terminate() error {
	running, err := p.Running()
	if err != nil {
		return err
	}
	if !running {
		return os.ErrProcessDone
	}
	if err := windows.TerminateProcess(p.handle, 1); err != nil {
		running, runningErr := p.Running()
		if runningErr == nil && !running {
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}

func (p *windowsRecoveredProcess) Close() error {
	return windows.CloseHandle(p.handle)
}
