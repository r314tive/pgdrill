//go:build darwin

package local

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

func processIdentity(pid int) (string, error) {
	if pid <= 0 {
		return "", fmt.Errorf("invalid process id %d", pid)
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if errors.Is(err, syscall.ESRCH) {
		return "", os.ErrProcessDone
	}
	if err != nil {
		return "", err
	}
	if info.Proc.P_pid != int32(pid) {
		return "", os.ErrProcessDone
	}
	started := info.Proc.P_starttime
	return "darwin-start-time:" +
		strconv.FormatInt(started.Sec, 10) + ":" +
		strconv.FormatInt(int64(started.Usec), 10), nil
}
