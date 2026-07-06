package local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/command"
	"github.com/r314tive/pgdrill/internal/model"
)

func TestPrepareCreatesWorkDirAndMarker(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "restore")
	target := New(Config{}, nil)

	err := target.Prepare(context.Background(), model.TargetSpec{
		Type:    model.RestoreTargetLocal,
		WorkDir: workDir,
	})
	if err != nil {
		t.Fatalf("prepare local target: %v", err)
	}

	if _, err := os.Stat(filepath.Join(workDir, markerFile)); err != nil {
		t.Fatalf("expected marker file: %v", err)
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
	if err := target.Prepare(context.Background(), model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: workDir}); err != nil {
		t.Fatalf("prepare local target: %v", err)
	}

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
	if err := target.Prepare(context.Background(), model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: workDir}); err != nil {
		t.Fatalf("prepare local target: %v", err)
	}

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
	if err := target.Prepare(context.Background(), model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: workDir}); err != nil {
		t.Fatalf("prepare local target: %v", err)
	}

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
	if err := target.Prepare(context.Background(), model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: t.TempDir()}); err != nil {
		t.Fatalf("prepare local target: %v", err)
	}

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
trap 'echo stopped > "$PGDRILL_SIGNAL_FILE"; exit 0' TERM
while true; do sleep 1; done
`)

	target := New(Config{
		PostgresBinary:  postgresPath,
		StartupTimeout:  50 * time.Millisecond,
		ShutdownTimeout: time.Second,
		Env: map[string]string{
			"PGDRILL_SIGNAL_FILE": signalFile,
		},
	}, nil)
	if err := target.Prepare(context.Background(), model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: workDir}); err != nil {
		t.Fatalf("prepare local target: %v", err)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("create data dir: %v", err)
	}

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
	if err := target.Prepare(context.Background(), model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: workDir}); err != nil {
		t.Fatalf("prepare local target: %v", err)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("create data dir: %v", err)
	}

	_, evidence, err := target.StartPostgres(context.Background(), model.RuntimeConfig{DataDirectory: dataDir, Port: 15433})
	if err == nil || !strings.Contains(err.Error(), "postgres exited during startup") {
		t.Fatalf("expected early exit error, got %v", err)
	}
	if len(evidence) != 1 || evidence[0].Attributes["exit_error"] == "" {
		t.Fatalf("expected exit evidence, got %#v", evidence)
	}
}

func TestDestroyRemovesWorkDirOnlyWhenConfigured(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "restore")
	target := New(Config{RemoveWorkDir: true}, nil)
	if err := target.Prepare(context.Background(), model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: workDir}); err != nil {
		t.Fatalf("prepare local target: %v", err)
	}

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

func TestDestroySkipsRemovalByDefault(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "restore")
	target := New(Config{}, nil)
	if err := target.Prepare(context.Background(), model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: workDir}); err != nil {
		t.Fatalf("prepare local target: %v", err)
	}

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
