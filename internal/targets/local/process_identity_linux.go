//go:build linux

package local

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

const maxLinuxProcessStatBytes = 64 << 10

func processIdentity(pid int) (string, error) {
	if pid <= 0 {
		return "", fmt.Errorf("invalid process id %d", pid)
	}
	file, err := os.Open("/proc/" + strconv.Itoa(pid) + "/stat")
	if errors.Is(err, os.ErrNotExist) {
		return "", os.ErrProcessDone
	}
	if err != nil {
		return "", err
	}
	payload, readErr := io.ReadAll(io.LimitReader(file, maxLinuxProcessStatBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return "", readErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if len(payload) > maxLinuxProcessStatBytes {
		return "", fmt.Errorf("process stat exceeds %d bytes", maxLinuxProcessStatBytes)
	}

	// The comm field is parenthesized and may contain spaces or ')' characters.
	// Fields after the final ')' start at field 3; process start time is field 22.
	commEnd := strings.LastIndexByte(string(payload), ')')
	if commEnd < 0 || commEnd+1 >= len(payload) {
		return "", fmt.Errorf("process stat has no terminated comm field")
	}
	fields := strings.Fields(string(payload[commEnd+1:]))
	const startTimeIndex = 22 - 3
	if len(fields) <= startTimeIndex {
		return "", fmt.Errorf("process stat has no start time")
	}
	startTime := fields[startTimeIndex]
	if _, err := strconv.ParseUint(startTime, 10, 64); err != nil {
		return "", fmt.Errorf("parse process start time: %w", err)
	}
	return "linux-start-ticks:" + startTime, nil
}
