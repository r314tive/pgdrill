//go:build unix

package local

import (
	"errors"
	"os"
	"syscall"
)

func postgresProcessExists(pid int) (bool, error) {
	err := syscall.Kill(pid, 0)
	if errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func terminateStartedProcess(process *os.Process) error {
	return process.Signal(syscall.SIGTERM)
}

func terminateProcessByPID(pid int) error {
	return signalProcessByPID(pid, syscall.SIGTERM)
}

func killProcessByPID(pid int) error {
	return signalProcessByPID(pid, syscall.SIGKILL)
}

func signalProcessByPID(pid int, signal syscall.Signal) error {
	err := syscall.Kill(pid, signal)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}
