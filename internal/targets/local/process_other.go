//go:build !unix && !windows

package local

import (
	"fmt"
	"os"
	"runtime"
)

func terminateStartedProcess(*os.Process) error {
	return fmt.Errorf("postgres process termination is unsupported on %s", runtime.GOOS)
}
