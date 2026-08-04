//go:build windows

package local

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

const windowsRecoveredProcessHelper = "PGDRILL_WINDOWS_RECOVERED_PROCESS_HELPER"

func TestWindowsIdentityBoundRecoveredProcess(t *testing.T) {
	if os.Getenv(windowsRecoveredProcessHelper) == "1" {
		time.Sleep(time.Minute)
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestWindowsIdentityBoundRecoveredProcess$")
	cmd.Env = append(os.Environ(), windowsRecoveredProcessHelper+"=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start controlled child process: %v", err)
	}
	waited := false
	t.Cleanup(func() {
		if waited {
			return
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	identity, err := processIdentity(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("read controlled child identity: %v", err)
	}
	wrong, err := openIdentityBoundProcess(cmd.Process.Pid, identity+"-stale")
	if err == nil || wrong != nil {
		t.Fatalf("open with stale identity = %#v, %v", wrong, err)
	}

	process, err := openIdentityBoundProcess(cmd.Process.Pid, identity)
	if err != nil {
		t.Fatalf("open identity-bound process: %v", err)
	}
	t.Cleanup(func() { _ = process.Close() })
	running, err := process.Running()
	if err != nil || !running {
		t.Fatalf("controlled child running = %t, %v", running, err)
	}
	if err := process.Terminate(); err != nil {
		t.Fatalf("terminate identity-bound process: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		running, err = process.Running()
		if err != nil {
			t.Fatalf("observe terminated process: %v", err)
		}
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("identity-bound process remained active after termination")
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = cmd.Wait()
	waited = true
}

func testProcessIdentityMatches(pid int, expected string) (bool, error) {
	identity, err := processIdentity(pid)
	if err != nil {
		if os.IsNotExist(err) || err == os.ErrProcessDone {
			return false, nil
		}
		return false, err
	}
	return identity == expected, nil
}

func terminateTestProcess(pid int) error {
	return killTestProcess(pid)
}

func killTestProcess(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	defer process.Release() //nolint:errcheck
	return process.Kill()
}
