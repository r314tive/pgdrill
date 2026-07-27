//go:build !unix && !windows

package local

import (
	"fmt"
	"os"
	"runtime"
)

func postgresProcessExists(int) (bool, error) {
	return false, fmt.Errorf("postgres process inspection is unsupported on %s", runtime.GOOS)
}

func terminateStartedProcess(*os.Process) error {
	return fmt.Errorf("postgres process termination is unsupported on %s", runtime.GOOS)
}

func terminateProcessByPID(int) error {
	return fmt.Errorf("postgres process termination is unsupported on %s", runtime.GOOS)
}

func killProcessByPID(int) error {
	return fmt.Errorf("postgres process termination is unsupported on %s", runtime.GOOS)
}
