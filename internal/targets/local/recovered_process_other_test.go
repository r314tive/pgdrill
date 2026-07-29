//go:build !unix && !windows

package local

import (
	"fmt"
	"runtime"
)

func testProcessIdentityMatches(int, string) (bool, error) {
	return false, fmt.Errorf("test process inspection is unsupported on %s", runtime.GOOS)
}

func terminateTestProcess(int) error {
	return fmt.Errorf("test process signalling is unsupported on %s", runtime.GOOS)
}

func killTestProcess(int) error {
	return fmt.Errorf("test process signalling is unsupported on %s", runtime.GOOS)
}
