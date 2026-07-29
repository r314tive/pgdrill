//go:build unix

package local

import (
	"errors"
	"os"
	"runtime"
	"syscall"
)

func testProcessIdentityMatches(pid int, expected string) (bool, error) {
	identity, err := processIdentity(pid)
	if errors.Is(err, os.ErrProcessDone) ||
		errors.Is(err, syscall.ESRCH) ||
		(runtime.GOOS == "darwin" && errors.Is(err, syscall.EIO)) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return identity == expected, nil
}

func terminateTestProcess(pid int) error {
	return signalTestProcess(pid, syscall.SIGTERM)
}

func killTestProcess(pid int) error {
	return signalTestProcess(pid, syscall.SIGKILL)
}

func signalTestProcess(pid int, signal syscall.Signal) error {
	err := syscall.Kill(pid, signal)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}
