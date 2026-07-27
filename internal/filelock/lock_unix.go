//go:build unix

package filelock

import (
	"context"
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func Lock(ctx context.Context, file *os.File, mode Mode) error {
	flag := unix.LOCK_SH
	if mode == Exclusive {
		flag = unix.LOCK_EX
	}
	for {
		err := unix.Flock(int(file.Fd()), flag|unix.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return err
		}
		if err := waitForRetry(ctx); err != nil {
			return err
		}
	}
}

func Unlock(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}
