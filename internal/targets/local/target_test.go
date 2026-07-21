package local

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/command"
	"github.com/r314tive/pgdrill/internal/core"
	"github.com/r314tive/pgdrill/internal/model"
	"github.com/r314tive/pgdrill/internal/testkit/conformance"
)

func TestTargetConformance(t *testing.T) {
	conformance.NativeTarget(t, func(t *testing.T) conformance.NativeTargetCase {
		root := t.TempDir()
		workDir := filepath.Join(root, "restore")
		dataDir := filepath.Join(workDir, "data")
		postgresPath := filepath.Join(root, "postgres")
		writeExecutable(t, postgresPath, `#!/bin/sh
data_dir=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -D) data_dir="$2"; shift 2 ;;
    *) shift ;;
  esac
done
printf '%s\n' "$$" "$data_dir" 0 15432 127.0.0.1 127.0.0.1 '0 0' ready > "$data_dir/postmaster.pid"
trap 'rm -f "$data_dir/postmaster.pid"; exit 0' TERM INT
while true; do sleep 0.1; done
`)

		attempt := model.AttemptContext{
			Identity: model.AttemptIdentity{
				RunID:      t.Name(),
				AttemptID:  "attempt-1",
				SpecDigest: "sha256:" + strings.Repeat("a", 64),
			},
			Target: model.TargetSpec{
				Type:    model.RestoreTargetLocal,
				WorkDir: workDir,
			},
			RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetLatest},
		}
		return conformance.NativeTargetCase{
			NewTarget: func() core.RestoreTarget {
				return New(Config{
					RemoveWorkDir:   true,
					PostgresBinary:  postgresPath,
					Port:            15432,
					StartupTimeout:  2 * time.Second,
					ShutdownTimeout: 2 * time.Second,
				}, nil)
			},
			Attempt: attempt,
			Step: model.RestoreStep{
				Name: "write-recovery-config",
				Files: []model.FileSpec{{
					Path:    filepath.Join(dataDir, "postgresql.auto.conf"),
					Content: "recovery_target_timeline = 'latest'\n",
					Mode:    "0600",
				}},
			},
			Runtime: model.RuntimeConfig{
				DataDirectory: dataDir,
				Port:          15432,
			},
			AwaitStarted: func(t testing.TB) {
				t.Helper()
				path := filepath.Join(dataDir, "postmaster.pid")
				deadline := time.Now().Add(2 * time.Second)
				for time.Now().Before(deadline) {
					payload, err := os.ReadFile(path)
					if err == nil && strings.TrimSpace(string(payload)) != "" {
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
				t.Fatalf("controlled postgres did not publish %s", path)
			},
			AssertDestroyed: func(t testing.TB) {
				t.Helper()
				if _, err := os.Stat(workDir); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("owned work_dir still exists after cleanup: %v", err)
				}
			},
		}
	})
}

func TestPrepareCreatesWorkDirAndMarker(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "restore")
	target := New(Config{}, nil)

	err := prepareTarget(t, target, model.TargetSpec{
		Type:    model.RestoreTargetLocal,
		WorkDir: workDir,
	})
	if err != nil {
		t.Fatalf("prepare local target: %v", err)
	}

	markerPath := filepath.Join(workDir, markerFile)
	marker, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("read marker file: %v", err)
	}
	if target.ownerID == "" || string(marker) != ownershipMarker(target.ownerID) {
		t.Fatalf("unexpected ownership marker %q", marker)
	}
	info, err := os.Stat(markerPath)
	if err != nil {
		t.Fatalf("stat marker file: %v", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("ownership marker must not be group/world accessible: %s", info.Mode().Perm())
	}
}

func TestPrepareRejectsNonEmptyExistingWorkDir(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "restore")
	if err := os.Mkdir(workDir, 0o700); err != nil {
		t.Fatalf("create existing workdir: %v", err)
	}
	importantPath := filepath.Join(workDir, "important.txt")
	if err := os.WriteFile(importantPath, []byte("keep\n"), 0o600); err != nil {
		t.Fatalf("write existing file: %v", err)
	}
	target := New(Config{RemoveWorkDir: true}, nil)

	validateErr := target.Validate(context.Background(), model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: workDir})
	if validateErr == nil || !strings.Contains(validateErr.Error(), "must be empty") {
		t.Fatalf("expected read-only non-empty workdir rejection, got %v", validateErr)
	}
	if _, markerErr := os.Stat(filepath.Join(workDir, markerFile)); !errors.Is(markerErr, os.ErrNotExist) {
		t.Fatalf("validation must not create ownership marker, stat err=%v", markerErr)
	}
	err := prepareTarget(t, target, model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: workDir})
	if err == nil || !strings.Contains(err.Error(), "must be empty") {
		t.Fatalf("expected non-empty workdir rejection, got %v", err)
	}
	beginLocalOperation(t, target, model.OperationTargetCleanup, "cleanup-target", 1)
	if _, destroyErr := target.Destroy(context.Background()); destroyErr != nil {
		t.Fatalf("destroy after rejected prepare: %v", destroyErr)
	}
	data, readErr := os.ReadFile(importantPath)
	if readErr != nil || string(data) != "keep\n" {
		t.Fatalf("existing data changed after rejected prepare: data=%q err=%v", data, readErr)
	}
}

func TestValidateMissingWorkDirIsReadOnly(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "restore")
	target := New(Config{}, nil)

	if err := target.Validate(context.Background(), model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: workDir}); err != nil {
		t.Fatalf("validate missing workdir: %v", err)
	}
	if _, err := os.Stat(workDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("validation created workdir, stat err=%v", err)
	}
}

func TestPrepareRejectsSymlinkWorkDir(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatalf("create real workdir: %v", err)
	}
	workDir := filepath.Join(root, "restore")
	if err := os.Symlink(realDir, workDir); err != nil {
		t.Skipf("create workdir symlink: %v", err)
	}

	target := New(Config{}, nil)
	err := prepareTarget(t, target, model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: workDir})
	if err == nil || !strings.Contains(err.Error(), "must be a real directory") {
		t.Fatalf("expected symlink workdir rejection, got %v", err)
	}
}

func TestExecuteRunsCommandStep(t *testing.T) {
	workDir := t.TempDir()
	runner := &fakeRunner{result: successResult()}
	target := New(Config{
		DefaultTimeout: 30 * time.Second,
		Env: map[string]string{
			"BASE": "from-base",
			"SAME": "base",
		},
		RedactValues: []string{"base-secret"},
	}, runner)
	if err := prepareTarget(t, target, model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: workDir}); err != nil {
		t.Fatalf("prepare local target: %v", err)
	}
	beginLocalOperation(t, target, model.OperationRestoreStep, "fetch", 1)

	evidence, err := target.Execute(context.Background(), model.RestoreStep{
		Name: "fetch",
		Command: &model.CommandSpec{
			Tool:    model.ToolWALG,
			Args:    []string{"backup-fetch", "/restore/data", "base_1"},
			Timeout: "45s",
			Env: map[string]string{
				"SAME": "override",
				"STEP": "from-step",
			},
			Redactions: []string{"step-secret"},
		},
	})
	if err != nil {
		t.Fatalf("execute local target step: %v", err)
	}

	if got, want := runner.invocation.Path, "wal-g"; got != want {
		t.Fatalf("unexpected command path: got %q want %q", got, want)
	}
	if got, want := runner.invocation.Args, []string{"backup-fetch", "/restore/data", "base_1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected command args: got %#v want %#v", got, want)
	}
	if runner.invocation.WorkDir != workDir {
		t.Fatalf("unexpected workdir %q", runner.invocation.WorkDir)
	}
	if runner.invocation.Timeout != 45*time.Second {
		t.Fatalf("unexpected timeout %s", runner.invocation.Timeout)
	}
	if got := runner.invocation.Env["SAME"]; got != "override" {
		t.Fatalf("expected step env to override base env, got %q", got)
	}
	if got, want := runner.invocation.RedactValues, []string{"base-secret", "step-secret"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected redactions: got %#v want %#v", got, want)
	}
	if len(evidence) != 1 || evidence[0].Kind != model.EvidenceCommand {
		t.Fatalf("expected command evidence, got %#v", evidence)
	}
}

func TestExecuteWritesFileStep(t *testing.T) {
	workDir := t.TempDir()
	target := New(Config{}, nil)
	if err := prepareTarget(t, target, model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: workDir}); err != nil {
		t.Fatalf("prepare local target: %v", err)
	}
	beginLocalOperation(t, target, model.OperationRestoreStep, "recovery-config", 1)

	configPath := filepath.Join(workDir, "data", "postgresql.auto.conf")
	evidence, err := target.Execute(context.Background(), model.RestoreStep{
		Name: "recovery-config",
		Files: []model.FileSpec{{
			Path:    configPath,
			Content: "restore_command = 'wal-g wal-fetch \"%f\" \"%p\"'\n",
			Mode:    "0600",
			Append:  true,
		}},
	})
	if err != nil {
		t.Fatalf("execute file step: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read written config: %v", err)
	}
	if !strings.Contains(string(data), "restore_command") {
		t.Fatalf("unexpected config content %q", string(data))
	}
	if len(evidence) != 1 || evidence[0].Kind != model.EvidenceFile {
		t.Fatalf("expected file evidence, got %#v", evidence)
	}
	if evidence[0].Attributes["path"] != configPath {
		t.Fatalf("unexpected file evidence %#v", evidence[0].Attributes)
	}
}

func TestExecuteRejectsFileOutsideWorkDir(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "restore")
	target := New(Config{}, nil)
	if err := prepareTarget(t, target, model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: workDir}); err != nil {
		t.Fatalf("prepare local target: %v", err)
	}
	beginLocalOperation(t, target, model.OperationRestoreStep, "unsafe-file", 1)

	_, err := target.Execute(context.Background(), model.RestoreStep{
		Name: "unsafe-file",
		Files: []model.FileSpec{{
			Path:    filepath.Join(root, "outside.conf"),
			Content: "unsafe\n",
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "outside local target work_dir") {
		t.Fatalf("expected outside workdir error, got %v", err)
	}
}

func TestExecuteRejectsFileThroughSymlink(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "restore")
	outsideDir := filepath.Join(root, "outside")
	if err := os.Mkdir(outsideDir, 0o700); err != nil {
		t.Fatalf("create outside directory: %v", err)
	}
	target := New(Config{}, nil)
	if err := prepareTarget(t, target, model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: workDir}); err != nil {
		t.Fatalf("prepare local target: %v", err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(workDir, "data")); err != nil {
		t.Skipf("create target symlink: %v", err)
	}
	beginLocalOperation(t, target, model.OperationRestoreStep, "unsafe-symlink-file", 1)

	outsidePath := filepath.Join(outsideDir, "postgresql.auto.conf")
	_, err := target.Execute(context.Background(), model.RestoreStep{
		Name: "unsafe-symlink-file",
		Files: []model.FileSpec{{
			Path:    filepath.Join(workDir, "data", "postgresql.auto.conf"),
			Content: "unsafe\n",
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "traverses symbolic link") {
		t.Fatalf("expected symlink traversal error, got %v", err)
	}
	if _, statErr := os.Stat(outsidePath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("file step escaped through symlink, stat err=%v", statErr)
	}
}

func TestExecuteReturnsStructuredCommandFailure(t *testing.T) {
	runner := &fakeRunner{result: command.Result{
		Evidence: model.CommandEvidence{
			FinishedAt: time.Date(2026, 7, 6, 1, 2, 3, 0, time.UTC),
			ExitStatus: model.ExitStatus{
				Started:  true,
				Exited:   true,
				ExitCode: 64,
			},
		},
	}}
	target := New(Config{}, runner)
	if err := prepareTarget(t, target, model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: t.TempDir()}); err != nil {
		t.Fatalf("prepare local target: %v", err)
	}
	beginLocalOperation(t, target, model.OperationRestoreStep, "fetch", 1)

	evidence, err := target.Execute(context.Background(), model.RestoreStep{
		Name:    "fetch",
		Command: &model.CommandSpec{Path: "wal-g"},
	})
	if err == nil || !strings.Contains(err.Error(), "exit code 64") {
		t.Fatalf("expected structured failure, got %v", err)
	}
	if len(evidence) != 1 {
		t.Fatalf("expected evidence on command failure, got %#v", evidence)
	}
}

func TestStartPostgresStartsProcessAndDestroyStopsIt(t *testing.T) {
	dir := t.TempDir()
	workDir := filepath.Join(dir, "restore")
	dataDir := filepath.Join(workDir, "data")
	signalFile := filepath.Join(dir, "postgres-stopped")
	postgresPath := filepath.Join(dir, "postgres")
	writeExecutable(t, postgresPath, `#!/bin/sh
data_dir=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -D) data_dir="$2"; shift 2 ;;
    *) shift ;;
  esac
done
printf '%s\n' "$$" "$data_dir" 0 15432 127.0.0.1 127.0.0.1 '0 0' ready > "$data_dir/postmaster.pid"
trap 'rm -f "$data_dir/postmaster.pid"; echo stopped > "$PGDRILL_SIGNAL_FILE"; exit 0' TERM
while true; do sleep 1; done
`)

	target := New(Config{
		PostgresBinary:  postgresPath,
		StartupTimeout:  2 * time.Second,
		ShutdownTimeout: time.Second,
		Env: map[string]string{
			"PGDRILL_SIGNAL_FILE": signalFile,
		},
	}, nil)
	if err := prepareTarget(t, target, model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: workDir}); err != nil {
		t.Fatalf("prepare local target: %v", err)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("create data dir: %v", err)
	}
	beginLocalOperation(t, target, model.OperationPostgresStart, "start-postgres", 1)

	startedAt := time.Now()
	pg, evidence, err := target.StartPostgres(context.Background(), model.RuntimeConfig{
		DataDirectory: dataDir,
		Port:          15432,
	})
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	if pg.Host != "127.0.0.1" || pg.Port != 15432 {
		t.Fatalf("unexpected running postgres %#v", pg)
	}
	if !strings.Contains(pg.ConnString, "127.0.0.1:15432") {
		t.Fatalf("unexpected conn string %q", pg.ConnString)
	}
	if len(evidence) != 1 || evidence[0].Kind != model.EvidenceRuntime {
		t.Fatalf("expected runtime evidence, got %#v", evidence)
	}
	if evidence[0].Attributes["pid"] == "" {
		t.Fatalf("expected process pid evidence, got %#v", evidence[0].Attributes)
	}
	if evidence[0].Attributes["startup_status"] != "ready" {
		t.Fatalf("expected ready startup evidence, got %#v", evidence[0].Attributes)
	}
	if elapsed := time.Since(startedAt); elapsed >= time.Second {
		t.Fatalf("readiness should return before the 2s deadline, elapsed=%s", elapsed)
	}

	beginLocalOperation(t, target, model.OperationTargetCleanup, "cleanup-target", 2)
	destroyEvidence, err := target.Destroy(context.Background())
	if err != nil {
		t.Fatalf("destroy local target: %v", err)
	}
	if len(destroyEvidence) != 2 {
		t.Fatalf("expected postgres stop and cleanup evidence, got %#v", destroyEvidence)
	}
	if destroyEvidence[0].Attributes["postgres_shutdown"] == "" {
		t.Fatalf("unexpected postgres stop evidence %#v", destroyEvidence[0])
	}
}

func TestStartPostgresReportsEarlyExit(t *testing.T) {
	dir := t.TempDir()
	workDir := filepath.Join(dir, "restore")
	dataDir := filepath.Join(workDir, "data")
	postgresPath := filepath.Join(dir, "postgres")
	writeExecutable(t, postgresPath, `#!/bin/sh
exit 42
`)

	target := New(Config{
		PostgresBinary: postgresPath,
		StartupTimeout: 2 * time.Second,
	}, nil)
	if err := prepareTarget(t, target, model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: workDir}); err != nil {
		t.Fatalf("prepare local target: %v", err)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("create data dir: %v", err)
	}
	beginLocalOperation(t, target, model.OperationPostgresStart, "start-postgres", 1)

	_, evidence, err := target.StartPostgres(context.Background(), model.RuntimeConfig{DataDirectory: dataDir, Port: 15433})
	if err == nil || !strings.Contains(err.Error(), "postgres exited during startup") {
		t.Fatalf("expected early exit error, got %v", err)
	}
	if len(evidence) != 1 || evidence[0].Attributes["exit_error"] == "" {
		t.Fatalf("expected exit evidence, got %#v", evidence)
	}
}

func TestStartPostgresTimesOutWithoutReadyStatus(t *testing.T) {
	dir := t.TempDir()
	workDir := filepath.Join(dir, "restore")
	dataDir := filepath.Join(workDir, "data")
	postgresPath := filepath.Join(dir, "postgres")
	writeExecutable(t, postgresPath, `#!/bin/sh
data_dir=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -D) data_dir="$2"; shift 2 ;;
    *) shift ;;
  esac
done
printf '%s\n' "$$" "$data_dir" 0 15432 127.0.0.1 127.0.0.1 '0 0' starting > "$data_dir/postmaster.pid"
trap 'rm -f "$data_dir/postmaster.pid"; exit 0' TERM
while true; do sleep 1; done
`)

	target := New(Config{
		PostgresBinary:  postgresPath,
		StartupTimeout:  500 * time.Millisecond,
		ShutdownTimeout: time.Second,
		RemoveWorkDir:   true,
	}, nil)
	if err := prepareTarget(t, target, model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: workDir}); err != nil {
		t.Fatalf("prepare local target: %v", err)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("create data dir: %v", err)
	}
	beginLocalOperation(t, target, model.OperationPostgresStart, "start-postgres", 1)

	_, evidence, err := target.StartPostgres(context.Background(), model.RuntimeConfig{DataDirectory: dataDir, Port: 15432})
	if err == nil || !strings.Contains(err.Error(), "did not become ready within 500ms") {
		t.Fatalf("expected bounded readiness timeout, got %v", err)
	}
	if len(evidence) != 1 || evidence[0].Attributes["startup_status"] == "" || evidence[0].Attributes["startup_timeout"] != "500ms" {
		t.Fatalf("expected timeout readiness evidence, got %#v", evidence)
	}

	beginLocalOperation(t, target, model.OperationTargetCleanup, "cleanup-target", 2)
	if _, destroyErr := target.Destroy(context.Background()); destroyErr != nil {
		t.Fatalf("destroy timed-out postgres target: %v", destroyErr)
	}
}

func TestPostgresReadinessUsesOwnedPostmasterStatus(t *testing.T) {
	dataDir := t.TempDir()
	pid := os.Getpid()
	postmasterPID := filepath.Join(dataDir, "postmaster.pid")

	tests := []struct {
		name       string
		payload    string
		wantReady  bool
		wantStatus string
	}{
		{name: "invalid pid", payload: "not-a-pid\n", wantStatus: "postmaster.pid has an invalid pid"},
		{name: "different pid", payload: "1\n", wantStatus: "postmaster.pid belongs to another process"},
		{name: "missing status", payload: fmt.Sprintf("%d\n/data\n0\n5432\n127.0.0.1\n127.0.0.1\n0 0\n", pid), wantStatus: "postmaster status is empty"},
		{name: "starting", payload: fmt.Sprintf("%d\n/data\n0\n5432\n127.0.0.1\n127.0.0.1\n0 0\nstarting\n", pid), wantStatus: "starting"},
		{name: "ready", payload: fmt.Sprintf("%d\n/data\n0\n5432\n127.0.0.1\n127.0.0.1\n0 0\nready   \n", pid), wantReady: true, wantStatus: "ready"},
		{name: "standby", payload: fmt.Sprintf("%d\n/data\n0\n5432\n127.0.0.1\n127.0.0.1\n0 0\nstandby\n", pid), wantReady: true, wantStatus: "standby"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := os.WriteFile(postmasterPID, []byte(tt.payload), 0o600); err != nil {
				t.Fatalf("write postmaster.pid: %v", err)
			}
			ready, status, err := postgresReadiness(dataDir, pid)
			if err != nil {
				t.Fatalf("postgresReadiness() error = %v", err)
			}
			if ready != tt.wantReady || status != tt.wantStatus {
				t.Fatalf("postgresReadiness() = (%v, %q), want (%v, %q)", ready, status, tt.wantReady, tt.wantStatus)
			}
		})
	}
}

func TestStartPostgresRejectsDataDirectoryOutsideWorkDir(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "restore")
	outsideDataDir := filepath.Join(root, "outside-data")
	if err := os.Mkdir(outsideDataDir, 0o700); err != nil {
		t.Fatalf("create outside data directory: %v", err)
	}
	target := New(Config{}, nil)
	if err := prepareTarget(t, target, model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: workDir}); err != nil {
		t.Fatalf("prepare local target: %v", err)
	}
	beginLocalOperation(t, target, model.OperationPostgresStart, "start-postgres", 1)

	_, _, err := target.StartPostgres(context.Background(), model.RuntimeConfig{DataDirectory: outsideDataDir})
	if err == nil || !strings.Contains(err.Error(), "outside local target work_dir") {
		t.Fatalf("expected outside data directory rejection, got %v", err)
	}
}

func TestStartPostgresRejectsExistingLogPath(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "restore")
	dataDir := filepath.Join(workDir, "data")
	target := New(Config{}, nil)
	if err := prepareTarget(t, target, model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: workDir}); err != nil {
		t.Fatalf("prepare local target: %v", err)
	}
	if err := os.Mkdir(dataDir, 0o700); err != nil {
		t.Fatalf("create data directory: %v", err)
	}
	logPath := filepath.Join(workDir, "postgres.log")
	if err := os.WriteFile(logPath, []byte("do not replace\n"), 0o600); err != nil {
		t.Fatalf("create existing log: %v", err)
	}
	beginLocalOperation(t, target, model.OperationPostgresStart, "start-postgres", 1)

	_, _, err := target.StartPostgres(context.Background(), model.RuntimeConfig{DataDirectory: dataDir, Port: 15434})
	if err == nil || !strings.Contains(err.Error(), "file exists") {
		t.Fatalf("expected exclusive log creation failure, got %v", err)
	}
	data, readErr := os.ReadFile(logPath)
	if readErr != nil || string(data) != "do not replace\n" {
		t.Fatalf("existing log changed: data=%q err=%v", data, readErr)
	}
}

func TestDestroyRemovesWorkDirOnlyWhenConfigured(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "restore")
	target := New(Config{RemoveWorkDir: true}, nil)
	if err := prepareTarget(t, target, model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: workDir}); err != nil {
		t.Fatalf("prepare local target: %v", err)
	}

	beginLocalOperation(t, target, model.OperationTargetCleanup, "cleanup-target", 1)
	evidence, err := target.Destroy(context.Background())
	if err != nil {
		t.Fatalf("destroy local target: %v", err)
	}
	if _, err := os.Stat(workDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected workdir to be removed, stat err=%v", err)
	}
	if len(evidence) != 1 || evidence[0].Attributes["cleanup"] != "removed" {
		t.Fatalf("unexpected cleanup evidence %#v", evidence)
	}
}

func TestDestroyRejectsMismatchedOwnershipMarker(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "restore")
	target := New(Config{RemoveWorkDir: true}, nil)
	if err := prepareTarget(t, target, model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: workDir}); err != nil {
		t.Fatalf("prepare local target: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workDir, markerFile), []byte("forged\n"), 0o600); err != nil {
		t.Fatalf("replace ownership marker: %v", err)
	}

	beginLocalOperation(t, target, model.OperationTargetCleanup, "cleanup-target", 1)
	evidence, err := target.Destroy(context.Background())
	if err == nil || !strings.Contains(err.Error(), "mismatched ownership marker") {
		t.Fatalf("expected ownership mismatch error, got %v", err)
	}
	if _, statErr := os.Stat(workDir); statErr != nil {
		t.Fatalf("mismatched marker must preserve workdir: %v", statErr)
	}
	if len(evidence) != 1 || evidence[0].Attributes["cleanup"] != "refused" {
		t.Fatalf("expected refused cleanup evidence, got %#v", evidence)
	}
}

func TestDestroySkipsRemovalByDefault(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "restore")
	target := New(Config{}, nil)
	if err := prepareTarget(t, target, model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: workDir}); err != nil {
		t.Fatalf("prepare local target: %v", err)
	}

	beginLocalOperation(t, target, model.OperationTargetCleanup, "cleanup-target", 1)
	evidence, err := target.Destroy(context.Background())
	if err != nil {
		t.Fatalf("destroy local target: %v", err)
	}
	if _, err := os.Stat(workDir); err != nil {
		t.Fatalf("expected workdir to remain, stat err=%v", err)
	}
	if len(evidence) != 1 || evidence[0].Attributes["cleanup"] != "skipped" {
		t.Fatalf("unexpected cleanup evidence %#v", evidence)
	}
}

func TestReconcileProvesPreparedTargetAfterProcessLoss(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "restore")
	spec := model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: workDir}
	first := New(Config{}, nil)
	if err := prepareTarget(t, first, spec); err != nil {
		t.Fatalf("prepare local target: %v", err)
	}
	operation := first.operation

	recovered := New(Config{}, nil)
	if err := recovered.BindAttempt(first.attempt); err != nil {
		t.Fatalf("BindAttempt() error = %v", err)
	}
	if err := recovered.BeginOperation(operation); err != nil {
		t.Fatalf("BeginOperation() error = %v", err)
	}
	reconciliation, err := recovered.Reconcile(context.Background(), operationCheckpoint(operation))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if reconciliation.Disposition != model.ReconciliationCompleted || !recovered.prepared || recovered.ownerID != first.ownerID {
		t.Fatalf("unexpected reconciliation %#v recovered=%#v", reconciliation, recovered)
	}
}

func TestReconcileUsesRestoreStepReceiptAndRefusesUnprovenStep(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "restore")
	spec := model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: workDir}
	first := New(Config{}, nil)
	if err := prepareTarget(t, first, spec); err != nil {
		t.Fatalf("prepare local target: %v", err)
	}
	completed := beginLocalOperation(t, first, model.OperationRestoreStep, "write-config", 1)
	if _, err := first.Execute(context.Background(), model.RestoreStep{
		Name: "write-config",
		Files: []model.FileSpec{{
			Path:    filepath.Join(workDir, "data", "postgresql.auto.conf"),
			Content: "recovery_target = 'latest'\n",
		}},
	}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	recovered := New(Config{}, nil)
	if err := recovered.BindAttempt(first.attempt); err != nil {
		t.Fatalf("BindAttempt() error = %v", err)
	}
	if err := recovered.BeginOperation(completed); err != nil {
		t.Fatalf("BeginOperation(completed) error = %v", err)
	}
	result, err := recovered.Reconcile(context.Background(), operationCheckpoint(completed))
	if err != nil {
		t.Fatalf("Reconcile(completed) error = %v", err)
	}
	if result.Disposition != model.ReconciliationCompleted {
		t.Fatalf("completed step reconciliation = %#v", result)
	}

	unproven := beginLocalOperation(t, recovered, model.OperationRestoreStep, "unproven-command", 2)
	result, err = recovered.Reconcile(context.Background(), operationCheckpoint(unproven))
	if err != nil {
		t.Fatalf("Reconcile(unproven) error = %v", err)
	}
	if result.Disposition != model.ReconciliationUnknown {
		t.Fatalf("unproven step reconciliation = %#v, want unknown", result)
	}
}

func operationCheckpoint(operation model.Operation) model.OperationCheckpoint {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	return model.OperationCheckpoint{
		SchemaVersion: model.CurrentOperationCheckpointSchemaVersion,
		Operation:     operation,
		State:         model.OperationStateIntent,
		StartedAt:     now,
		UpdatedAt:     now,
	}
}

func prepareTarget(t *testing.T, target *Target, spec model.TargetSpec) error {
	t.Helper()
	attempt := model.AttemptContext{
		Identity: model.AttemptIdentity{
			RunID:      t.Name(),
			AttemptID:  "attempt-1",
			SpecDigest: "sha256:" + strings.Repeat("a", 64),
		},
		Target: spec,
	}
	if err := target.BindAttempt(attempt); err != nil {
		t.Fatalf("BindAttempt() error = %v", err)
	}
	beginLocalOperation(t, target, model.OperationTargetPrepare, "prepare-target", 0)
	return target.Prepare(context.Background(), spec)
}

func beginLocalOperation(t *testing.T, target *Target, kind model.OperationKind, name string, ordinal int) model.Operation {
	t.Helper()
	stage := map[model.OperationKind]model.DrillStage{
		model.OperationTargetPrepare: model.DrillStageTargetPreparation,
		model.OperationRestoreStep:   model.DrillStageRestoreExecution,
		model.OperationPostgresStart: model.DrillStagePostgresStart,
		model.OperationTargetCleanup: model.DrillStageTargetCleanup,
	}[kind]
	operation, err := model.NewOperation(target.attempt.Identity, stage, kind, name, ordinal)
	if err != nil {
		t.Fatalf("NewOperation() error = %v", err)
	}
	if err := target.BeginOperation(operation); err != nil {
		t.Fatalf("BeginOperation() error = %v", err)
	}
	return operation
}

type fakeRunner struct {
	invocation command.Invocation
	result     command.Result
	err        error
}

func (r *fakeRunner) Run(_ context.Context, inv command.Invocation) (command.Result, error) {
	r.invocation = inv
	return r.result, r.err
}

func successResult() command.Result {
	now := time.Date(2026, 7, 6, 1, 2, 3, 0, time.UTC)
	return command.Result{
		Evidence: model.CommandEvidence{
			Path:       "wal-g",
			Args:       []string{"backup-fetch", "/restore/data", "base_1"},
			StartedAt:  now.Add(-1 * time.Second),
			FinishedAt: now,
			ExitStatus: model.ExitStatus{
				Started:  true,
				Exited:   true,
				Success:  true,
				ExitCode: 0,
			},
		},
	}
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}
}
