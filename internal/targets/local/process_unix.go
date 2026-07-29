//go:build unix

package local

import (
	"os"
	"syscall"
)

func terminateStartedProcess(process *os.Process) error {
	return process.Signal(syscall.SIGTERM)
}
