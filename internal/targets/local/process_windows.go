//go:build windows

package local

import "os"

func terminateStartedProcess(process *os.Process) error {
	return process.Kill()
}
