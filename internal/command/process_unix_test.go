//go:build unix

package command

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunnerBoundsInheritedOutputPipesAndKillsChildProcessGroup(t *testing.T) {
	const childDelay = 8 * time.Second

	runner := NewRunner(Options{WaitDelay: 20 * time.Millisecond})
	tempDir := t.TempDir()
	markerPath := filepath.Join(tempDir, "grandchild-finished")
	readyPath := filepath.Join(tempDir, "grandchild-ready")
	startedAt := time.Now()

	result, err := runner.Run(context.Background(), Invocation{
		Path: os.Args[0],
		Args: []string{
			"-test.run=TestHelperProcess",
			"--",
			"spawn-delayed-write",
			childDelay.String(),
		},
		Env: map[string]string{
			"PGDRILL_COMMAND_HELPER": "1",
			"PGDRILL_COMMAND_MARKER": markerPath,
			"PGDRILL_COMMAND_READY":  readyPath,
		},
	})

	if !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("Run() error = %v, want exec.ErrWaitDelay", err)
	}
	if elapsed := time.Since(startedAt); elapsed >= childDelay/2 {
		t.Fatalf("Run() elapsed %s, want substantially less than child delay %s", elapsed, childDelay)
	}
	if !result.Evidence.ExitStatus.Started || !result.Evidence.ExitStatus.Exited ||
		result.Evidence.ExitStatus.Success || result.Evidence.ExitStatus.Error == "" {
		t.Fatalf("unexpected wait-delay status %#v", result.Evidence.ExitStatus)
	}

	pidBytes, readErr := os.ReadFile(readyPath)
	if readErr != nil {
		t.Fatalf("read grandchild PID: %v", readErr)
	}
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if parseErr != nil {
		t.Fatalf("parse grandchild PID: %v", parseErr)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	})

	deadline := time.Now().Add(2 * time.Second)
	for processExists(pid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if processExists(pid) {
		t.Fatalf("grandchild process %d survived process-group cleanup", pid)
	}
	if _, statErr := os.Stat(markerPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("grandchild completed delayed write, stat error = %v", statErr)
	}
}

func processExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
