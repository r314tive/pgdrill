package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
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
				target := New(Config{
					RemoveWorkDir:   true,
					PostgresBinary:  postgresPath,
					Port:            15432,
					StartupTimeout:  2 * time.Second,
					ShutdownTimeout: 2 * time.Second,
				}, nil)
				if runtime.GOOS == "darwin" {
					target.openRecoveredProcess = openTestIdentityBoundProcess
				}
				return target
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

func TestVerifyRecoveryTargetRejectsAttemptTargetMismatchBeforeCommand(t *testing.T) {
	runner := &fakeRunner{}
	target := New(Config{PSQLBinary: "/custom/bin/psql"}, runner)
	err := target.BindAttempt(model.AttemptContext{
		Identity: model.AttemptIdentity{
			RunID:      "run-1",
			AttemptID:  "attempt-1",
			SpecDigest: "sha256:" + strings.Repeat("a", 64),
		},
		Target: model.TargetSpec{
			Type:    model.RestoreTargetLocal,
			WorkDir: filepath.Join(t.TempDir(), "restore"),
		},
		RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetLatest},
	})
	if err != nil {
		t.Fatalf("BindAttempt() error = %v", err)
	}

	_, err = target.VerifyRecoveryTarget(
		context.Background(),
		model.RunningPostgres{ConnString: "postgres://verify"},
		model.RecoveryTarget{Type: model.RecoveryTargetImmediate},
	)

	if err == nil || !strings.Contains(err.Error(), "does not match local target attempt binding") {
		t.Fatalf("VerifyRecoveryTarget() error = %v, want attempt target mismatch", err)
	}
	if runner.invocation.Path != "" {
		t.Fatalf("target mismatch executed command %#v", runner.invocation)
	}
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

func TestPrepareMakesExistingEmptyWorkDirPrivate(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "restore")
	if err := os.Mkdir(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := New(Config{}, nil)
	spec := model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: workDir}
	if err := target.Validate(context.Background(), spec); err != nil {
		t.Fatalf("validate empty workdir: %v", err)
	}
	before, err := os.Lstat(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if before.Mode().Perm() != 0o755 {
		t.Fatalf("read-only validation changed workdir mode to %o", before.Mode().Perm())
	}
	if err := prepareTarget(t, target, spec); err != nil {
		t.Fatalf("prepare local target: %v", err)
	}
	after, err := os.Lstat(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if after.Mode().Perm() != 0o700 {
		t.Fatalf("prepared workdir mode = %o, want 700", after.Mode().Perm())
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

func TestExecuteFileStepAppendAndOverwrite(t *testing.T) {
	for _, test := range []struct {
		name    string
		append  bool
		initial string
		content string
		want    string
	}{
		{
			name:    "append",
			append:  true,
			initial: "first\n",
			content: "second\n",
			want:    "first\nsecond\n",
		},
		{
			name:    "overwrite",
			initial: "long stale contents\n",
			content: "new\n",
			want:    "new\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			workDir := t.TempDir()
			target := New(Config{}, nil)
			if err := prepareTarget(t, target, model.TargetSpec{
				Type:    model.RestoreTargetLocal,
				WorkDir: workDir,
			}); err != nil {
				t.Fatalf("prepare local target: %v", err)
			}
			path := filepath.Join(workDir, "data", "postgresql.auto.conf")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatalf("create data directory: %v", err)
			}
			if err := os.WriteFile(path, []byte(test.initial), 0o600); err != nil {
				t.Fatalf("seed target file: %v", err)
			}
			beginLocalOperation(t, target, model.OperationRestoreStep, test.name, 1)

			_, err := target.Execute(context.Background(), model.RestoreStep{
				Name: test.name,
				Files: []model.FileSpec{{
					Path:    path,
					Content: test.content,
					Mode:    "0600",
					Append:  test.append,
				}},
			})
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read target file: %v", err)
			}
			if got := string(data); got != test.want {
				t.Fatalf("target contents = %q, want %q", got, test.want)
			}
		})
	}
}

func TestExecuteFileStepOverwriteReplacesHardlinkWithoutModifyingExternalFile(t *testing.T) {
	rootDir := t.TempDir()
	workDir := filepath.Join(rootDir, "restore")
	externalPath := filepath.Join(rootDir, "external.conf")
	targetPath := filepath.Join(workDir, "data", "postgresql.auto.conf")
	target := New(Config{}, nil)
	if err := prepareTarget(t, target, model.TargetSpec{
		Type:    model.RestoreTargetLocal,
		WorkDir: workDir,
	}); err != nil {
		t.Fatalf("prepare local target: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o700); err != nil {
		t.Fatalf("create data directory: %v", err)
	}
	if err := os.WriteFile(externalPath, []byte("external\n"), 0o600); err != nil {
		t.Fatalf("write external file: %v", err)
	}
	if err := os.Link(externalPath, targetPath); err != nil {
		t.Skipf("create same-filesystem hardlink: %v", err)
	}
	beginLocalOperation(t, target, model.OperationRestoreStep, "overwrite-hardlink", 1)

	_, err := target.Execute(context.Background(), model.RestoreStep{
		Name: "overwrite-hardlink",
		Files: []model.FileSpec{{
			Path:    targetPath,
			Content: "replacement\n",
			Mode:    "0600",
		}},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	external, err := os.ReadFile(externalPath)
	if err != nil {
		t.Fatalf("read external file: %v", err)
	}
	if got := string(external); got != "external\n" {
		t.Fatalf("external contents = %q, want unchanged contents", got)
	}
	replacement, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read replacement file: %v", err)
	}
	if got := string(replacement); got != "replacement\n" {
		t.Fatalf("replacement contents = %q", got)
	}
	externalInfo, err := os.Stat(externalPath)
	if err != nil {
		t.Fatal(err)
	}
	replacementInfo, err := os.Stat(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(externalInfo, replacementInfo) {
		t.Fatal("overwrite retained the external file inode")
	}
}

func TestExecuteFileStepAppendRejectsHardlinkWithoutModifyingExternalFile(t *testing.T) {
	rootDir := t.TempDir()
	workDir := filepath.Join(rootDir, "restore")
	externalPath := filepath.Join(rootDir, "external.conf")
	targetPath := filepath.Join(workDir, "data", "postgresql.auto.conf")
	target := New(Config{}, nil)
	if err := prepareTarget(t, target, model.TargetSpec{
		Type:    model.RestoreTargetLocal,
		WorkDir: workDir,
	}); err != nil {
		t.Fatalf("prepare local target: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o700); err != nil {
		t.Fatalf("create data directory: %v", err)
	}
	if err := os.WriteFile(externalPath, []byte("external\n"), 0o600); err != nil {
		t.Fatalf("write external file: %v", err)
	}
	if err := os.Link(externalPath, targetPath); err != nil {
		t.Skipf("create same-filesystem hardlink: %v", err)
	}
	beginLocalOperation(t, target, model.OperationRestoreStep, "append-hardlink", 1)

	_, err := target.Execute(context.Background(), model.RestoreStep{
		Name: "append-hardlink",
		Files: []model.FileSpec{{
			Path:    targetPath,
			Content: "appended\n",
			Mode:    "0600",
			Append:  true,
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "link count 2") {
		t.Fatalf("Execute() error = %v, want hardlink rejection", err)
	}
	for _, path := range []string{externalPath, targetPath} {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read %s: %v", path, readErr)
		}
		if got := string(data); got != "external\n" {
			t.Fatalf("%s contents = %q, want unchanged contents", path, got)
		}
	}
}

func TestExecuteFileStepAppendRejectsNonPrivateFile(t *testing.T) {
	workDir := t.TempDir()
	targetPath := filepath.Join(workDir, "data", "postgresql.auto.conf")
	target := New(Config{}, nil)
	if err := prepareTarget(t, target, model.TargetSpec{
		Type:    model.RestoreTargetLocal,
		WorkDir: workDir,
	}); err != nil {
		t.Fatalf("prepare local target: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o700); err != nil {
		t.Fatalf("create data directory: %v", err)
	}
	if err := os.WriteFile(targetPath, []byte("shared\n"), 0o644); err != nil {
		t.Fatalf("write append target: %v", err)
	}
	if err := os.Chmod(targetPath, 0o644); err != nil {
		t.Fatalf("set non-private mode: %v", err)
	}
	beginLocalOperation(t, target, model.OperationRestoreStep, "append-shared", 1)

	_, err := target.Execute(context.Background(), model.RestoreStep{
		Name: "append-shared",
		Files: []model.FileSpec{{
			Path:    targetPath,
			Content: "appended\n",
			Mode:    "0600",
			Append:  true,
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "not a private regular file") {
		t.Fatalf("Execute() error = %v, want non-private file rejection", err)
	}
	data, readErr := os.ReadFile(targetPath)
	if readErr != nil {
		t.Fatalf("read append target: %v", readErr)
	}
	if got := string(data); got != "shared\n" {
		t.Fatalf("append target contents = %q, want unchanged contents", got)
	}
}

func TestParseTargetFileMode(t *testing.T) {
	for _, test := range []struct {
		value   string
		want    os.FileMode
		wantErr bool
	}{
		{value: "", want: 0o600},
		{value: "0000", want: 0},
		{value: "0777", want: 0o777},
		{value: "0780", wantErr: true},
		{value: "1000", wantErr: true},
		{value: "invalid", wantErr: true},
	} {
		t.Run(test.value, func(t *testing.T) {
			got, err := parseTargetFileMode(test.value)
			if test.wantErr {
				if err == nil {
					t.Fatalf("parseTargetFileMode(%q) unexpectedly succeeded", test.value)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseTargetFileMode(%q) error = %v", test.value, err)
			}
			if got != test.want {
				t.Fatalf("parseTargetFileMode(%q) = %o, want %o", test.value, got, test.want)
			}
		})
	}
}

func TestBindRootFileDetectsPathReplacement(t *testing.T) {
	workDir := t.TempDir()
	path := filepath.Join(workDir, "target")
	movedPath := filepath.Join(workDir, "moved")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatalf("write original file: %v", err)
	}
	root, err := os.OpenRoot(workDir)
	if err != nil {
		t.Fatalf("OpenRoot() error = %v", err)
	}
	defer root.Close()
	file, err := root.OpenFile("target", os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("OpenFile() error = %v", err)
	}
	defer file.Close()

	if err := os.Rename(path, movedPath); err != nil {
		t.Fatalf("move opened file: %v", err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
		t.Fatalf("write replacement file: %v", err)
	}
	if err := bindRootFile(root, "target", file); err == nil ||
		!strings.Contains(err.Error(), "changed while opening or writing") {
		t.Fatalf("bindRootFile() error = %v, want path replacement rejection", err)
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
	argsFile := filepath.Join(dir, "postgres-args")
	postgresPath := filepath.Join(dir, "postgres")
	writeExecutable(t, postgresPath, `#!/bin/sh
printf '%s\n' "$@" > "$PGDRILL_ARGS_FILE"
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
			"PGDRILL_ARGS_FILE":   argsFile,
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
	if evidence[0].Attributes["archive_mode"] != "off" {
		t.Fatalf("expected disabled archive mode evidence, got %#v", evidence[0].Attributes)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read postgres args: %v", err)
	}
	if !strings.Contains(string(args), "-c\narchive_mode=off\n") {
		t.Fatalf("postgres args do not disable archive mode:\n%s", args)
	}
	if elapsed := time.Since(startedAt); elapsed >= 2500*time.Millisecond {
		t.Fatalf("readiness exceeded the startup budget plus persistence slack: %s", elapsed)
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
printf 'startup failed with %s\n' "$PGDRILL_STARTUP_TOKEN" >&2
exit 42
`)

	target := New(Config{
		PostgresBinary: postgresPath,
		StartupTimeout: 2 * time.Second,
		Env: map[string]string{
			"PGDRILL_STARTUP_TOKEN": "startup-secret",
		},
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
	logTail := evidence[0].Attributes["postgres_log_tail"]
	if !strings.Contains(logTail, "startup failed with [REDACTED]") || strings.Contains(logTail, "startup-secret") {
		t.Fatalf("expected redacted postgres log evidence, got %q", logTail)
	}
	if evidence[0].Attributes["postgres_log_bytes"] == "" {
		t.Fatalf("expected postgres log byte count, got %#v", evidence[0].Attributes)
	}
}

func TestStartPostgresRedactsInheritedSensitiveEnvironment(t *testing.T) {
	const secret = "parent-pg-password"
	t.Setenv("PGPASSWORD", secret)

	dir := t.TempDir()
	workDir := filepath.Join(dir, "restore")
	dataDir := filepath.Join(workDir, "data")
	postgresPath := filepath.Join(dir, "postgres")
	writeExecutable(t, postgresPath, `#!/bin/sh
printf 'inherited password: %s\n' "$PGPASSWORD" >&2
exit 42
`)

	target := New(Config{
		PostgresBinary: postgresPath,
		StartupTimeout: 2 * time.Second,
	}, nil)
	if err := prepareTarget(t, target, model.TargetSpec{
		Type:    model.RestoreTargetLocal,
		WorkDir: workDir,
	}); err != nil {
		t.Fatalf("prepare local target: %v", err)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("create data dir: %v", err)
	}
	beginLocalOperation(t, target, model.OperationPostgresStart, "start-postgres", 1)

	_, evidence, err := target.StartPostgres(
		context.Background(),
		model.RuntimeConfig{DataDirectory: dataDir, Port: 15433},
	)
	if err == nil || !strings.Contains(err.Error(), "postgres exited during startup") {
		t.Fatalf("expected early exit error, got %v", err)
	}
	if len(evidence) != 1 {
		t.Fatalf("expected one startup evidence record, got %#v", evidence)
	}
	logTail := evidence[0].Attributes["postgres_log_tail"]
	if !strings.Contains(logTail, "inherited password: [REDACTED]") {
		t.Fatalf("expected inherited password redaction, got %q", logTail)
	}
	if strings.Contains(logTail, secret) {
		t.Fatalf("startup evidence leaked inherited password: %q", logTail)
	}
}

func TestCapturePostgresLogBoundsTailAfterRedaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "postgres.log")
	payload := strings.Repeat("x", maxPostgresLogBytes+1024) + "startup-token\n"
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatalf("write postgres log: %v", err)
	}

	target := New(Config{Env: map[string]string{"API_TOKEN": "startup-token"}}, nil)
	evidence := runtimeEvidence("postgres-start", map[string]string{}, time.Now().UTC())
	writer, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := openBoundLogReader(path, writer)
	if err != nil {
		_ = writer.Close()
		t.Fatal(err)
	}
	defer reader.Close()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	target.capturePostgresLog(
		&evidence,
		reader,
		mergeProcessEnvironment(os.Environ(), target.cfg.Env),
	)

	logTail := evidence.Attributes["postgres_log_tail"]
	if len(logTail) > maxPostgresLogBytes {
		t.Fatalf("postgres log tail length = %d, want <= %d", len(logTail), maxPostgresLogBytes)
	}
	if !strings.HasSuffix(logTail, "[REDACTED]\n") || strings.Contains(logTail, "startup-token") {
		t.Fatalf("expected bounded redacted tail, got suffix %q", logTail[len(logTail)-32:])
	}
	if evidence.Attributes["postgres_log_truncated"] != "true" {
		t.Fatalf("expected truncation metadata, got %#v", evidence.Attributes)
	}
	if got, want := evidence.Attributes["postgres_log_bytes"], strconv.Itoa(len(payload)); got != want {
		t.Fatalf("postgres_log_bytes = %q, want %q", got, want)
	}
}

func TestCapturePostgresLogUsesBoundDescriptorAfterPathReplacement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not permit replacing the open log path")
	}
	path := filepath.Join(t.TempDir(), "postgres.log")
	if err := os.WriteFile(path, []byte("owned startup log\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writer, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := openBoundLogReader(path, writer)
	if err != nil {
		_ = writer.Close()
		t.Fatal(err)
	}
	defer reader.Close()
	if err := os.Remove(path); err != nil {
		_ = writer.Close()
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement-secret\n"), 0o600); err != nil {
		_ = writer.Close()
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	target := New(Config{}, nil)
	evidence := runtimeEvidence("postgres-start", map[string]string{}, time.Now().UTC())
	target.capturePostgresLog(&evidence, reader, nil)
	logTail := evidence.Attributes["postgres_log_tail"]
	if logTail != "owned startup log\n" || strings.Contains(logTail, "replacement-secret") {
		t.Fatalf("captured log tail = %q, want descriptor-bound original", logTail)
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
		{name: "missing status", payload: fmt.Sprintf("%d\n%s\n0\n5432\n127.0.0.1\n127.0.0.1\n0 0\n", pid, dataDir), wantStatus: "postmaster status is empty"},
		{name: "starting", payload: fmt.Sprintf("%d\n%s\n0\n5432\n127.0.0.1\n127.0.0.1\n0 0\nstarting\n", pid, dataDir), wantStatus: "starting"},
		{name: "ready", payload: fmt.Sprintf("%d\n%s\n0\n5432\n127.0.0.1\n127.0.0.1\n0 0\nready   \n", pid, dataDir), wantReady: true, wantStatus: "ready"},
		{name: "standby", payload: fmt.Sprintf("%d\n%s\n0\n5432\n127.0.0.1\n127.0.0.1\n0 0\nstandby\n", pid, dataDir), wantReady: true, wantStatus: "standby"},
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

func TestPostgresProcessMatchesRequiresCurrentProcessIdentity(t *testing.T) {
	dataDir := t.TempDir()
	pid := os.Getpid()
	identity := currentProcessIdentity(t, pid)
	if err := os.WriteFile(
		filepath.Join(dataDir, "postmaster.pid"),
		[]byte(fmt.Sprintf("%d\n%s\n", pid, dataDir)),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	active, err := postgresProcessMatches(dataDir, pid, identity)
	if err != nil || !active {
		t.Fatalf("postgresProcessMatches(current) = %v, %v", active, err)
	}
	active, err = postgresProcessMatches(dataDir, pid, identity+"-stale")
	if err != nil || active {
		t.Fatalf("postgresProcessMatches(stale) = %v, %v", active, err)
	}
}

func TestPostgresProcessMatchesRefusesLiveIdentityWithoutPostmasterProof(t *testing.T) {
	dataDir := t.TempDir()
	pid := os.Getpid()
	identity := currentProcessIdentity(t, pid)

	active, err := postgresProcessMatches(dataDir, pid, identity)
	if active || err == nil || !strings.Contains(err.Error(), "live receipt-bound process") {
		t.Fatalf("postgresProcessMatches() = %v, %v, want fail-closed live process", active, err)
	}
}

func TestQuarantineOwnedWorkDirRejectsReplacementIdentity(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "restore")
	quarantine := filepath.Join(root, ".pgdrill-delete-test")
	preserved := filepath.Join(root, "preserved-original")
	ownerID := "owner-test"
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeOwnershipMarker(filepath.Join(source, markerFile), ownerID); err != nil {
		t.Fatal(err)
	}
	expected, err := os.Lstat(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(source, preserved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeOwnershipMarker(filepath.Join(source, markerFile), ownerID); err != nil {
		t.Fatal(err)
	}
	replacementPayload := filepath.Join(source, "replacement-data")
	if err := os.WriteFile(replacementPayload, []byte("must-survive"), 0o600); err != nil {
		t.Fatal(err)
	}

	err = quarantineOwnedWorkDir(source, quarantine, expected, ownerID)
	if err == nil || !strings.Contains(err.Error(), "changed before quarantine") {
		t.Fatalf("quarantineOwnedWorkDir() error = %v", err)
	}
	if payload, readErr := os.ReadFile(replacementPayload); readErr != nil ||
		string(payload) != "must-survive" {
		t.Fatalf("replacement directory was removed or changed: %q, %v", payload, readErr)
	}
	if _, statErr := os.Lstat(preserved); statErr != nil {
		t.Fatalf("original directory disappeared: %v", statErr)
	}
	if _, statErr := os.Lstat(quarantine); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("quarantine was not restored, stat error = %v", statErr)
	}
}

func TestCleanupReconcilesQuarantinedWorkDirAfterProcessLoss(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "restore")
	first := New(Config{RemoveWorkDir: true}, nil)
	if err := prepareTarget(t, first, model.TargetSpec{
		Type:    model.RestoreTargetLocal,
		WorkDir: workDir,
	}); err != nil {
		t.Fatal(err)
	}
	operation := beginLocalOperation(
		t,
		first,
		model.OperationTargetCleanup,
		"cleanup-target",
		1,
	)
	quarantine := first.cleanupQuarantinePath()
	if err := os.Rename(workDir, quarantine); err != nil {
		t.Fatal(err)
	}

	recovered := New(Config{RemoveWorkDir: true}, nil)
	if err := recovered.BindAttempt(first.attempt); err != nil {
		t.Fatal(err)
	}
	if err := recovered.BeginOperation(operation); err != nil {
		t.Fatal(err)
	}
	reconciliation, err := recovered.Reconcile(
		context.Background(),
		operationCheckpoint(operation),
	)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if reconciliation.Disposition != model.ReconciliationNotApplied || !recovered.prepared {
		t.Fatalf("quarantine reconciliation = %#v, prepared = %t", reconciliation, recovered.prepared)
	}
	evidence, err := recovered.Destroy(context.Background())
	if err != nil {
		t.Fatalf("Destroy() error = %v", err)
	}
	if len(evidence) != 1 || evidence[0].Attributes["cleanup"] != "recovered-and-removed" {
		t.Fatalf("cleanup evidence = %#v", evidence)
	}
	if _, err := os.Lstat(quarantine); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("quarantine remains after cleanup, stat error = %v", err)
	}
}

func TestRuntimePortRejectsInvalidExplicitAndConfiguredPorts(t *testing.T) {
	for _, port := range []int{-1, 65536} {
		target := New(Config{}, nil)
		if _, err := target.runtimePort(port); err == nil {
			t.Fatalf("runtimePort(%d) accepted invalid explicit port", port)
		}

		target = New(Config{Port: port}, nil)
		if _, err := target.runtimePort(0); err == nil {
			t.Fatalf("runtimePort(0) accepted invalid configured port %d", port)
		}
	}

	target := New(Config{Port: 15432}, nil)
	if got, err := target.runtimePort(0); err != nil || got != 15432 {
		t.Fatalf("runtimePort(configured) = %d, %v", got, err)
	}
	target = New(Config{}, nil)
	if got, err := target.runtimePort(15433); err != nil || got != 15433 {
		t.Fatalf("runtimePort(explicit) = %d, %v", got, err)
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

func TestDestroyRejectsSymbolicLinkOwnershipMarker(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "restore")
	target := New(Config{RemoveWorkDir: true}, nil)
	if err := prepareTarget(t, target, model.TargetSpec{
		Type:    model.RestoreTargetLocal,
		WorkDir: workDir,
	}); err != nil {
		t.Fatalf("prepare local target: %v", err)
	}
	markerPath := filepath.Join(workDir, markerFile)
	if err := os.Remove(markerPath); err != nil {
		t.Fatal(err)
	}
	externalMarker := filepath.Join(t.TempDir(), "marker")
	if err := os.WriteFile(
		externalMarker,
		[]byte(ownershipMarker(target.ownerID)),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(externalMarker, markerPath); err != nil {
		t.Skipf("create ownership marker symlink: %v", err)
	}

	beginLocalOperation(t, target, model.OperationTargetCleanup, "cleanup-target", 1)
	evidence, err := target.Destroy(context.Background())
	if err == nil || !strings.Contains(err.Error(), "non-symbolic-link") {
		t.Fatalf("Destroy(symlink marker) error = %v", err)
	}
	if _, err := os.Lstat(workDir); err != nil {
		t.Fatalf("unsafe workdir was removed: %v", err)
	}
	if len(evidence) != 1 || evidence[0].Attributes["cleanup"] != "refused" {
		t.Fatalf("cleanup evidence = %#v", evidence)
	}
}

func TestDestroyRefusesRecoveredProcessWhenOwnershipChanges(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "restore")
	target := New(Config{RemoveWorkDir: true}, nil)
	if err := prepareTarget(t, target, model.TargetSpec{
		Type:    model.RestoreTargetLocal,
		WorkDir: workDir,
	}); err != nil {
		t.Fatalf("prepare local target: %v", err)
	}
	target.recovered = &recoveredPostgres{
		pid:           999999,
		dataDirectory: filepath.Join(workDir, "data"),
		logPath:       filepath.Join(workDir, "postgres.log"),
		port:          15432,
		receipt: operationReceipt{
			OperationKey: "sha256:" + strings.Repeat("b", 64),
			CompletedAt:  time.Now().UTC(),
		},
	}
	if err := os.WriteFile(
		filepath.Join(workDir, markerFile),
		[]byte("changed\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	beginLocalOperation(t, target, model.OperationTargetCleanup, "cleanup-target", 1)
	evidence, err := target.Destroy(context.Background())
	if err == nil || !strings.Contains(err.Error(), "ownership marker") {
		t.Fatalf("Destroy(changed ownership) error = %v", err)
	}
	if _, err := os.Lstat(workDir); err != nil {
		t.Fatalf("workdir was removed after ownership loss: %v", err)
	}
	if len(evidence) != 1 ||
		evidence[0].Attributes["postgres_shutdown"] != "ownership_unproven" {
		t.Fatalf("shutdown evidence = %#v", evidence)
	}
}

func TestStopRecoveredPostgresUsesOneIdentityBoundHandle(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "restore")
	dataDir := filepath.Join(workDir, "data")
	target := New(Config{ShutdownTimeout: time.Second}, nil)
	if err := prepareTarget(t, target, model.TargetSpec{
		Type:    model.RestoreTargetLocal,
		WorkDir: workDir,
	}); err != nil {
		t.Fatalf("prepare local target: %v", err)
	}
	if err := os.Mkdir(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}

	const pid = 4242
	const identity = "test-process-identity"
	if err := os.WriteFile(
		filepath.Join(dataDir, "postmaster.pid"),
		[]byte(fmt.Sprintf("%d\n%s\n", pid, dataDir)),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	operation := beginLocalOperation(
		t,
		target,
		model.OperationPostgresStart,
		"start-postgres",
		1,
	)
	running := model.RunningPostgres{
		ConnString:    "postgresql://127.0.0.1:15432/postgres?sslmode=disable",
		DataDirectory: dataDir,
		Host:          "127.0.0.1",
		Port:          15432,
	}
	receipt := operationReceipt{
		OperationKey:    operation.Key,
		CompletedAt:     time.Now().UTC(),
		Postgres:        &running,
		PID:             pid,
		ProcessIdentity: identity,
		LogPath:         filepath.Join(workDir, "postgres.log"),
	}
	if err := os.WriteFile(receipt.LogPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := target.writeOperationReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	target.recovered = &recoveredPostgres{
		pid:           pid,
		dataDirectory: dataDir,
		logPath:       receipt.LogPath,
		port:          running.Port,
		receipt:       receipt,
	}

	handle := &fakeRecoveredProcessHandle{running: true}
	openCalls := 0
	target.openRecoveredProcess = func(gotPID int, gotIdentity string) (recoveredProcessHandle, error) {
		openCalls++
		if gotPID != pid || gotIdentity != identity {
			t.Fatalf("open identity-bound process = (%d, %q)", gotPID, gotIdentity)
		}
		return handle, nil
	}

	evidence, err := target.stopRecoveredPostgres(context.Background())
	if err != nil {
		t.Fatalf("stopRecoveredPostgres() error = %v", err)
	}
	if openCalls != 1 || handle.terminateCalls != 1 || handle.killCalls != 0 {
		t.Fatalf(
			"identity-bound lifecycle = opens:%d terminate:%d kill:%d",
			openCalls,
			handle.terminateCalls,
			handle.killCalls,
		)
	}
	if handle.closeCalls != 1 {
		t.Fatalf("identity-bound handle close calls = %d, want 1", handle.closeCalls)
	}
	if evidence.Attributes["postgres_shutdown"] != "terminated" {
		t.Fatalf("shutdown evidence = %#v", evidence.Attributes)
	}
}

func TestStopRecoveredPostgresDoesNotSignalUnprovenIdentity(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "restore")
	dataDir := filepath.Join(workDir, "data")
	target := New(Config{}, nil)
	if err := prepareTarget(t, target, model.TargetSpec{
		Type:    model.RestoreTargetLocal,
		WorkDir: workDir,
	}); err != nil {
		t.Fatalf("prepare local target: %v", err)
	}
	if err := os.Mkdir(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	operation := beginLocalOperation(
		t,
		target,
		model.OperationPostgresStart,
		"start-postgres",
		1,
	)
	running := model.RunningPostgres{
		ConnString:    "postgresql://127.0.0.1:15432/postgres?sslmode=disable",
		DataDirectory: dataDir,
		Host:          "127.0.0.1",
		Port:          15432,
	}
	receipt := operationReceipt{
		OperationKey:    operation.Key,
		CompletedAt:     time.Now().UTC(),
		Postgres:        &running,
		PID:             4242,
		ProcessIdentity: "original-identity",
		LogPath:         filepath.Join(workDir, "postgres.log"),
	}
	if err := os.WriteFile(receipt.LogPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := target.writeOperationReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	target.recovered = &recoveredPostgres{
		pid:           receipt.PID,
		dataDirectory: dataDir,
		logPath:       receipt.LogPath,
		port:          running.Port,
		receipt:       receipt,
	}
	target.openRecoveredProcess = func(int, string) (recoveredProcessHandle, error) {
		return nil, fmt.Errorf("recovered process identity does not match its receipt")
	}

	evidence, err := target.stopRecoveredPostgres(context.Background())
	if err == nil || !strings.Contains(err.Error(), "identity does not match") {
		t.Fatalf("stopRecoveredPostgres() error = %v", err)
	}
	if evidence.Attributes["postgres_shutdown"] != "inspect_failed" {
		t.Fatalf("shutdown evidence = %#v", evidence.Attributes)
	}
}

func TestRecoveredSignallingFailsClosedWhenUnsupported(t *testing.T) {
	if runtime.GOOS == "linux" || runtime.GOOS == "windows" {
		t.Skip("platform provides identity-bound recovered process signalling")
	}
	handle, err := openIdentityBoundProcess(4242, "recorded-identity")
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("openIdentityBoundProcess() = %#v, %v", handle, err)
	}
	if handle != nil {
		t.Fatalf("unsupported platform returned process handle %#v", handle)
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

func TestFindRecoveredPostgresRejectsMultipleActiveReceipts(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "restore")
	target := New(Config{}, nil)
	if err := prepareTarget(t, target, model.TargetSpec{
		Type:    model.RestoreTargetLocal,
		WorkDir: workDir,
	}); err != nil {
		t.Fatalf("prepare local target: %v", err)
	}
	logPath := filepath.Join(workDir, "postgres.log")
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	processIdentity := currentProcessIdentity(t, os.Getpid())

	for index := 1; index <= 2; index++ {
		dataDirectory := filepath.Join(workDir, fmt.Sprintf("data-%d", index))
		if err := os.Mkdir(dataDirectory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(dataDirectory, "postmaster.pid"),
			[]byte(fmt.Sprintf("%d\n%s\n", os.Getpid(), dataDirectory)),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		operation := beginLocalOperation(
			t,
			target,
			model.OperationPostgresStart,
			fmt.Sprintf("start-postgres-%d", index),
			index,
		)
		port := 15431 + index
		if err := target.writeOperationReceipt(operationReceipt{
			OperationKey: operation.Key,
			CompletedAt:  time.Now().UTC(),
			Postgres: &model.RunningPostgres{
				ConnString: fmt.Sprintf(
					"postgresql://127.0.0.1:%d/postgres?sslmode=disable",
					port,
				),
				DataDirectory: dataDirectory,
				Host:          "127.0.0.1",
				Port:          port,
			},
			PID:             os.Getpid(),
			ProcessIdentity: processIdentity,
			LogPath:         logPath,
		}); err != nil {
			t.Fatal(err)
		}
	}

	recovered, err := target.findRecoveredPostgres()
	if err == nil || !strings.Contains(err.Error(), "multiple active postgres receipts") {
		t.Fatalf("findRecoveredPostgres() = %#v, %v", recovered, err)
	}
	if recovered != nil {
		t.Fatalf("ambiguous recovery returned process %#v", recovered)
	}
}

func TestDestroyRefusesUnresolvedPostgresLaunchIntent(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "restore")
	dataDir := filepath.Join(workDir, "data")
	target := New(Config{RemoveWorkDir: true}, nil)
	if err := prepareTarget(t, target, model.TargetSpec{
		Type:    model.RestoreTargetLocal,
		WorkDir: workDir,
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(workDir, "postgres.log")
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	operation := beginLocalOperation(
		t,
		target,
		model.OperationPostgresStart,
		"start-postgres",
		1,
	)
	if err := target.writeOperationReceipt(operationReceipt{
		OperationKey: operation.Key,
		State:        postgresReceiptLaunchIntent,
		RecordedAt:   time.Now().UTC(),
		Postgres: &model.RunningPostgres{
			ConnString:    "postgresql://127.0.0.1:15432/postgres?sslmode=disable",
			DataDirectory: dataDir,
			Host:          "127.0.0.1",
			Port:          15432,
		},
		LogPath: logPath,
	}); err != nil {
		t.Fatal(err)
	}

	beginLocalOperation(t, target, model.OperationTargetCleanup, "cleanup-target", 2)
	evidence, err := target.Destroy(context.Background())

	if err == nil || !strings.Contains(err.Error(), "unresolved postgres launch intent") {
		t.Fatalf("Destroy() error = %v", err)
	}
	if _, statErr := os.Stat(workDir); statErr != nil {
		t.Fatalf("unresolved launch intent must preserve workdir: %v", statErr)
	}
	if len(evidence) != 1 || evidence[0].Attributes["cleanup"] != "refused" {
		t.Fatalf("cleanup refusal evidence = %#v", evidence)
	}
}

func TestReconcileProcessStartedReceiptDoesNotProveReadiness(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "restore")
	dataDir := filepath.Join(workDir, "data")
	first := New(Config{}, nil)
	if err := prepareTarget(t, first, model.TargetSpec{
		Type:    model.RestoreTargetLocal,
		WorkDir: workDir,
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(dataDir, "postmaster.pid"),
		[]byte(fmt.Sprintf("%d\n%s\n", os.Getpid(), dataDir)),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(workDir, "postgres.log")
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	operation := beginLocalOperation(
		t,
		first,
		model.OperationPostgresStart,
		"start-postgres",
		1,
	)
	if err := first.writeOperationReceipt(operationReceipt{
		OperationKey: operation.Key,
		State:        postgresReceiptProcessStarted,
		RecordedAt:   time.Now().UTC(),
		Postgres: &model.RunningPostgres{
			ConnString:    "postgresql://127.0.0.1:15432/postgres?sslmode=disable",
			DataDirectory: dataDir,
			Host:          "127.0.0.1",
			Port:          15432,
		},
		PID:             os.Getpid(),
		ProcessIdentity: currentProcessIdentity(t, os.Getpid()),
		LogPath:         logPath,
	}); err != nil {
		t.Fatal(err)
	}

	recovered := New(Config{}, nil)
	if err := recovered.BindAttempt(first.attempt); err != nil {
		t.Fatal(err)
	}
	if err := recovered.BeginOperation(operation); err != nil {
		t.Fatal(err)
	}
	result, err := recovered.Reconcile(
		context.Background(),
		operationCheckpoint(operation),
	)

	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.Disposition != model.ReconciliationUnknown || result.Postgres != nil {
		t.Fatalf("process-started reconciliation = %#v", result)
	}
	if recovered.recovered == nil || recovered.recovered.pid != os.Getpid() {
		t.Fatalf("active process ownership was not recovered: %#v", recovered.recovered)
	}
}

func TestReconcileDoesNotDuplicateAlreadyTrackedPostgresProcess(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "restore")
	dataDir := filepath.Join(workDir, "data")
	target := New(Config{}, nil)
	if err := prepareTarget(t, target, model.TargetSpec{
		Type:    model.RestoreTargetLocal,
		WorkDir: workDir,
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(dataDir, "postmaster.pid"),
		[]byte(fmt.Sprintf("%d\n%s\n", os.Getpid(), dataDir)),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(workDir, "postgres.log")
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	operation := beginLocalOperation(
		t,
		target,
		model.OperationPostgresStart,
		"start-postgres",
		1,
	)
	if err := target.writeOperationReceipt(operationReceipt{
		OperationKey: operation.Key,
		CompletedAt:  time.Now().UTC(),
		Postgres: &model.RunningPostgres{
			ConnString:    "postgresql://127.0.0.1:15432/postgres?sslmode=disable",
			DataDirectory: dataDir,
			Host:          "127.0.0.1",
			Port:          15432,
		},
		PID:             os.Getpid(),
		ProcessIdentity: currentProcessIdentity(t, os.Getpid()),
		LogPath:         logPath,
	}); err != nil {
		t.Fatal(err)
	}
	target.postgres = &postgresProcess{
		cmd: &exec.Cmd{Process: &os.Process{Pid: os.Getpid()}},
	}

	result, err := target.Reconcile(context.Background(), operationCheckpoint(operation))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.Disposition != model.ReconciliationCompleted || target.recovered != nil {
		t.Fatalf("Reconcile() = %#v, recovered = %#v", result, target.recovered)
	}
}

func TestReconcileReportsLegacyPostgresReceiptFailClosed(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "restore")
	dataDir := filepath.Join(workDir, "data")
	target := New(Config{}, nil)
	if err := prepareTarget(t, target, model.TargetSpec{
		Type:    model.RestoreTargetLocal,
		WorkDir: workDir,
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(workDir, "postgres.log")
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	operation := beginLocalOperation(
		t,
		target,
		model.OperationPostgresStart,
		"start-postgres",
		1,
	)
	legacy := operationReceipt{
		OperationKey: operation.Key,
		CompletedAt:  time.Now().UTC(),
		Postgres: &model.RunningPostgres{
			ConnString:    "postgresql://127.0.0.1:15432/postgres?sslmode=disable",
			DataDirectory: dataDir,
			Host:          "127.0.0.1",
			Port:          15432,
		},
		PID:     os.Getpid(),
		LogPath: logPath,
	}
	payload, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(workDir, receiptDirectory), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target.operationReceiptPath(operation), payload, 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := target.Reconcile(context.Background(), operationCheckpoint(operation))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.Disposition != model.ReconciliationUnknown ||
		!strings.Contains(result.Message, "legacy postgres receipt lacks process identity") {
		t.Fatalf("legacy receipt reconciliation = %#v", result)
	}
}

func TestFindRecoveredPostgresRejectsUnexpectedReceiptEntry(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "restore")
	target := New(Config{}, nil)
	if err := prepareTarget(t, target, model.TargetSpec{
		Type:    model.RestoreTargetLocal,
		WorkDir: workDir,
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(workDir, receiptDirectory), 0o700); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(workDir, receiptDirectory, "unexpected")
	if err := os.WriteFile(entry, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := target.findRecoveredPostgres(); err == nil ||
		!strings.Contains(err.Error(), "unexpected entry") {
		t.Fatalf("findRecoveredPostgres() error = %v", err)
	}
}

func TestStartPostgresRemovesLaunchIntentAfterDefinitiveStartFailure(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "restore")
	dataDir := filepath.Join(workDir, "data")
	target := New(Config{
		PostgresBinary: filepath.Join(t.TempDir(), "missing-postgres"),
	}, nil)
	if err := prepareTarget(t, target, model.TargetSpec{
		Type:    model.RestoreTargetLocal,
		WorkDir: workDir,
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	operation := beginLocalOperation(
		t,
		target,
		model.OperationPostgresStart,
		"start-postgres",
		1,
	)

	_, _, err := target.StartPostgres(
		context.Background(),
		model.RuntimeConfig{DataDirectory: dataDir, Port: 15432},
	)

	if err == nil || !strings.Contains(err.Error(), "start postgres") {
		t.Fatalf("StartPostgres() error = %v", err)
	}
	if receipt, found, readErr := target.readOperationReceipt(operation); readErr != nil || found {
		t.Fatalf("launch receipt after definitive failure = %#v, found=%v, err=%v", receipt, found, readErr)
	}
}

func TestDestroyHonorsCanceledContextBeforeRemoval(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "restore")
	target := New(Config{RemoveWorkDir: true}, nil)
	if err := prepareTarget(t, target, model.TargetSpec{
		Type:    model.RestoreTargetLocal,
		WorkDir: workDir,
	}); err != nil {
		t.Fatal(err)
	}
	beginLocalOperation(t, target, model.OperationTargetCleanup, "cleanup-target", 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := target.Destroy(ctx)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Destroy() error = %v", err)
	}
	if _, statErr := os.Stat(workDir); statErr != nil {
		t.Fatalf("canceled cleanup removed workdir: %v", statErr)
	}
}

func TestWaitForRecoveredProcessHonorsContext(t *testing.T) {
	process := &fakeRecoveredProcessHandle{running: true}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	startedAt := time.Now()

	stopped, err := waitForRecoveredProcess(ctx, process, time.Second)

	if stopped || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waitForRecoveredProcess() = %v, %v", stopped, err)
	}
	if elapsed := time.Since(startedAt); elapsed > 250*time.Millisecond {
		t.Fatalf("context cancellation was not prompt: %s", elapsed)
	}
}

func TestShutdownErrorStatusClassifiesCancellation(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{err: context.Canceled, want: "context_canceled"},
		{err: context.DeadlineExceeded, want: "context_canceled"},
		{err: errors.New("process inspection failed"), want: "inspect_failed"},
	}
	for _, test := range tests {
		if got := shutdownErrorStatus(test.err); got != test.want {
			t.Fatalf("shutdownErrorStatus(%v) = %q, want %q", test.err, got, test.want)
		}
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

func TestReconcileRejectsPostgresReceiptOutsideOwnedWorkDir(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "restore")
	first := New(Config{}, nil)
	spec := model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: workDir}
	if err := prepareTarget(t, first, spec); err != nil {
		t.Fatalf("prepare local target: %v", err)
	}
	operation := beginLocalOperation(
		t,
		first,
		model.OperationPostgresStart,
		"start-postgres",
		1,
	)
	outsideDataDir := filepath.Join(root, "outside-data")
	if err := os.Mkdir(outsideDataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(workDir, "postgres.log")
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	processIdentity := currentProcessIdentity(t, os.Getpid())
	if err := first.writeOperationReceipt(operationReceipt{
		OperationKey: operation.Key,
		CompletedAt:  time.Now().UTC(),
		Postgres: &model.RunningPostgres{
			ConnString:    "postgresql://127.0.0.1:15432/postgres?sslmode=disable",
			DataDirectory: outsideDataDir,
			Host:          "127.0.0.1",
			Port:          15432,
		},
		PID:             os.Getpid(),
		ProcessIdentity: processIdentity,
		LogPath:         logPath,
	}); err != nil {
		t.Fatal(err)
	}

	recovered := New(Config{}, nil)
	if err := recovered.BindAttempt(first.attempt); err != nil {
		t.Fatal(err)
	}
	if err := recovered.BeginOperation(operation); err != nil {
		t.Fatal(err)
	}
	result, err := recovered.Reconcile(
		context.Background(),
		operationCheckpoint(operation),
	)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.Disposition != model.ReconciliationUnknown ||
		recovered.recovered != nil {
		t.Fatalf("outside receipt reconciliation = %#v recovered=%#v", result, recovered.recovered)
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

type fakeRecoveredProcessHandle struct {
	running        bool
	terminateCalls int
	killCalls      int
	closeCalls     int
}

func (p *fakeRecoveredProcessHandle) Running() (bool, error) {
	return p.running, nil
}

func (p *fakeRecoveredProcessHandle) Terminate() error {
	p.terminateCalls++
	p.running = false
	return nil
}

func (p *fakeRecoveredProcessHandle) Kill() error {
	p.killCalls++
	p.running = false
	return nil
}

func (p *fakeRecoveredProcessHandle) Close() error {
	p.closeCalls++
	return nil
}

type testIdentityBoundProcess struct {
	pid              int
	expectedIdentity string
}

func openTestIdentityBoundProcess(pid int, expectedIdentity string) (recoveredProcessHandle, error) {
	identity, err := processIdentity(pid)
	if err != nil {
		return nil, err
	}
	if identity != expectedIdentity {
		return nil, fmt.Errorf("test process identity does not match")
	}
	return &testIdentityBoundProcess{
		pid:              pid,
		expectedIdentity: expectedIdentity,
	}, nil
}

func (p *testIdentityBoundProcess) Running() (bool, error) {
	return testProcessIdentityMatches(p.pid, p.expectedIdentity)
}

func (p *testIdentityBoundProcess) Terminate() error {
	if err := terminateTestProcess(p.pid); err != nil {
		return fmt.Errorf("terminate test process: %w", err)
	}
	return nil
}

func (p *testIdentityBoundProcess) Kill() error {
	if err := killTestProcess(p.pid); err != nil {
		return fmt.Errorf("kill test process: %w", err)
	}
	return nil
}

func (p *testIdentityBoundProcess) Close() error {
	return nil
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

func currentProcessIdentity(t *testing.T, pid int) string {
	t.Helper()
	identity, err := processIdentity(pid)
	if err != nil {
		t.Fatalf("processIdentity(%d) error = %v", pid, err)
	}
	if identity == "" {
		t.Fatalf("processIdentity(%d) returned an empty identity", pid)
	}
	return identity
}
