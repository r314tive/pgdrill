//go:build windows

package local

import "os"

func testProcessIdentityMatches(pid int, expected string) (bool, error) {
	identity, err := processIdentity(pid)
	if err != nil {
		if os.IsNotExist(err) || err == os.ErrProcessDone {
			return false, nil
		}
		return false, err
	}
	return identity == expected, nil
}

func terminateTestProcess(pid int) error {
	return killTestProcess(pid)
}

func killTestProcess(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	defer process.Release() //nolint:errcheck
	return process.Kill()
}
