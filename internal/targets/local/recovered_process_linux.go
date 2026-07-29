//go:build linux

package local

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

type pidfdProcess struct {
	fd int
}

func openIdentityBoundProcess(pid int, expectedIdentity string) (recoveredProcessHandle, error) {
	if pid <= 0 || expectedIdentity == "" {
		return nil, fmt.Errorf("recovered process identity is incomplete")
	}
	fd, err := unix.PidfdOpen(pid, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil, os.ErrProcessDone
	}
	if err != nil {
		return nil, fmt.Errorf("open pidfd for process %d: %w", pid, err)
	}
	process := &pidfdProcess{fd: fd}
	identity, err := processIdentity(pid)
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

func (p *pidfdProcess) Running() (bool, error) {
	poll := []unix.PollFd{{Fd: int32(p.fd), Events: unix.POLLIN}}
	for {
		_, err := unix.Poll(poll, 0)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("poll recovered process pidfd: %w", err)
		}
		break
	}
	if poll[0].Revents&unix.POLLNVAL != 0 {
		return false, fmt.Errorf("recovered process pidfd is closed")
	}
	if poll[0].Revents&unix.POLLERR != 0 {
		return false, fmt.Errorf("poll recovered process pidfd reported an error")
	}
	return poll[0].Revents&(unix.POLLIN|unix.POLLHUP) == 0, nil
}

func (p *pidfdProcess) Terminate() error {
	return p.signal(unix.SIGTERM)
}

func (p *pidfdProcess) Kill() error {
	return p.signal(unix.SIGKILL)
}

func (p *pidfdProcess) signal(signal unix.Signal) error {
	err := unix.PidfdSendSignal(p.fd, signal, nil, 0)
	if errors.Is(err, unix.ESRCH) {
		return os.ErrProcessDone
	}
	if err != nil {
		return fmt.Errorf("signal recovered process through pidfd: %w", err)
	}
	return nil
}

func (p *pidfdProcess) Close() error {
	return unix.Close(p.fd)
}
