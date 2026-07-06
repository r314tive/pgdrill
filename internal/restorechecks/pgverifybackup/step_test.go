package pgverifybackup

import (
	"reflect"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
)

func TestStepDisabled(t *testing.T) {
	step, err := Config{}.Step("/tmp/data")
	if err != nil {
		t.Fatalf("build step: %v", err)
	}
	if step != nil {
		t.Fatalf("expected nil step, got %#v", step)
	}
}

func TestStepBuildsCommand(t *testing.T) {
	step, err := Config{
		Enabled:       true,
		Binary:        "/usr/lib/postgresql/16/bin/pg_verifybackup",
		Timeout:       2 * time.Minute,
		Format:        "plain",
		ManifestPath:  "/tmp/manifest",
		WALDirectory:  "/tmp/wal",
		NoParseWAL:    true,
		SkipChecksums: true,
		ExitOnError:   true,
		Quiet:         true,
		Ignore:        []string{"postgresql.auto.conf", "recovery.signal"},
		RedactValues:  []string{"secret"},
	}.Step("/tmp/data")
	if err != nil {
		t.Fatalf("build step: %v", err)
	}
	if step == nil || step.Command == nil {
		t.Fatalf("expected command step, got %#v", step)
	}
	if step.Name != "pg-verifybackup" {
		t.Fatalf("unexpected step name %q", step.Name)
	}
	if step.Command.Tool != model.ToolPGVerifyBackup {
		t.Fatalf("unexpected tool %q", step.Command.Tool)
	}
	if step.Command.Path != "/usr/lib/postgresql/16/bin/pg_verifybackup" {
		t.Fatalf("unexpected path %q", step.Command.Path)
	}
	wantArgs := []string{
		"--exit-on-error",
		"--format=plain",
		"--ignore=postgresql.auto.conf",
		"--ignore=recovery.signal",
		"--manifest-path=/tmp/manifest",
		"--no-parse-wal",
		"--quiet",
		"--skip-checksums",
		"--wal-directory=/tmp/wal",
		"/tmp/data",
	}
	if !reflect.DeepEqual(step.Command.Args, wantArgs) {
		t.Fatalf("unexpected args:\ngot  %#v\nwant %#v", step.Command.Args, wantArgs)
	}
	if step.Command.Timeout != "2m0s" {
		t.Fatalf("unexpected timeout %q", step.Command.Timeout)
	}
	if got, want := step.Command.Redactions, []string{"secret"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected redactions: got %#v want %#v", got, want)
	}
}

func TestStepRequiresDataDirectory(t *testing.T) {
	_, err := Config{Enabled: true}.Step("")
	if err == nil {
		t.Fatal("expected missing data directory error")
	}
}
