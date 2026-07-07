package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
	"github.com/r314tive/pgdrill/internal/report"
)

func TestVersionCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := run([]string{"version"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("expected exit code 0, got %d: %s", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got == "" {
		t.Fatal("expected version output")
	}
}

func TestExplainJSONCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := run([]string{"explain", "-format", "json"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("expected exit code 0, got %d: %s", code, stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, `"providers"`) {
		t.Fatalf("expected providers in json output, got: %s", got)
	}
}

func TestUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := run([]string{"missing"}, &stdout, &stderr)

	if code != 2 {
		t.Fatalf("expected exit code 2, got %d", code)
	}
	if got := stderr.String(); !strings.Contains(got, "unknown command") {
		t.Fatalf("expected unknown command error, got: %s", got)
	}
}

func TestSubcommandHelpReturnsSuccess(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := run([]string{"run", "-h"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("expected exit code 0, got %d: %s", code, stderr.String())
	}
	if got := stderr.String(); !strings.Contains(got, "Usage of run") {
		t.Fatalf("expected run help output, got: %s", got)
	}
}

func TestCatalogListCommandJSON(t *testing.T) {
	dir := t.TempDir()
	walgPath := filepath.Join(dir, "wal-g")
	configPath := filepath.Join(dir, "pgdrill.yaml")

	writeExecutable(t, walgPath, `#!/bin/sh
if [ "$1" != "backup-list" ] || [ "$2" != "--detail" ] || [ "$3" != "--json" ]; then
  echo "unexpected args: $*" >&2
  exit 64
fi
cat <<'JSON'
[
  {
    "name": "base_00000001000000000000007F",
    "start_time": "2026-07-06T01:02:03Z",
    "finish_time": "2026-07-06T01:03:03Z",
    "wal_segment_backup_start": "00000001000000000000007F",
    "pg_version": 160005
  }
]
JSON
`)

	writeFile(t, configPath, `
cluster:
  name: test-main
provider:
  type: wal-g
  binary: `+walgPath+`
target:
  type: local
  work_dir: `+dir+`
recovery:
  target: latest
`)

	var stdout, stderr bytes.Buffer
	code := run([]string{"catalog", "list", "-f", configPath, "-format", "json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d: %s", code, stderr.String())
	}

	var output catalogListOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("parse catalog output: %v\n%s", err, stdout.String())
	}
	if output.Cluster != "test-main" {
		t.Fatalf("unexpected cluster %q", output.Cluster)
	}
	if output.Provider != model.ProviderWALG {
		t.Fatalf("unexpected provider %q", output.Provider)
	}
	if output.BackupCount != 1 || len(output.Backups) != 1 {
		t.Fatalf("unexpected backups: count=%d backups=%#v", output.BackupCount, output.Backups)
	}
	if output.Backups[0].ID != "wal-g:base_00000001000000000000007F" {
		t.Fatalf("unexpected backup id %q", output.Backups[0].ID)
	}
	if len(output.Evidence) != 0 {
		t.Fatalf("expected evidence to be omitted by default, got %#v", output.Evidence)
	}
}

func TestCatalogListCommandJSONPgBackRest(t *testing.T) {
	dir := t.TempDir()
	pgBackRestPath := filepath.Join(dir, "pgbackrest")
	configPath := filepath.Join(dir, "pgdrill.yaml")

	writeExecutable(t, pgBackRestPath, `#!/bin/sh
if [ "$1" != "--config=/etc/pgbackrest.conf" ] || [ "$2" != "--stanza=main" ] || [ "$3" != "--repo=1" ] || [ "$4" != "info" ] || [ "$5" != "--output=json" ]; then
  echo "unexpected args: $*" >&2
  exit 64
fi
cat <<'JSON'
[
  {
    "name": "main",
    "status": {"code": 0, "message": "ok"},
    "db": [{"id": 1, "system-id": 73924987654321, "version": "16"}],
    "backup": [
      {
        "label": "20240502-030405F",
        "type": "full",
        "error": false,
        "database": {"id": 1, "repo-key": 1},
        "archive": {"start": "0000000100000000000000A1", "stop": "0000000100000000000000A2"},
        "lsn": {"start": "0/A1000028", "stop": "0/A2000028"},
        "timestamp": {"start": 1714619045, "stop": 1714619645}
      }
    ]
  }
]
JSON
`)

	writeFile(t, configPath, `
cluster:
  name: test-main
provider:
  type: pgbackrest
  binary: `+pgBackRestPath+`
  config_path: /etc/pgbackrest.conf
  stanza: main
  repo: "1"
target:
  type: local
  work_dir: `+dir+`
recovery:
  target: latest
`)

	var stdout, stderr bytes.Buffer
	code := run([]string{"catalog", "list", "-f", configPath, "-format", "json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d: %s", code, stderr.String())
	}

	var output catalogListOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("parse catalog output: %v\n%s", err, stdout.String())
	}
	if output.Provider != model.ProviderPGBackRest {
		t.Fatalf("unexpected provider %q", output.Provider)
	}
	if output.BackupCount != 1 || len(output.Backups) != 1 {
		t.Fatalf("unexpected backups: count=%d backups=%#v", output.BackupCount, output.Backups)
	}
	if output.Backups[0].ID != "pgbackrest:main/20240502-030405F" {
		t.Fatalf("unexpected backup id %q", output.Backups[0].ID)
	}
	if output.Backups[0].PostgreSQLVersion != "16" {
		t.Fatalf("unexpected postgres version %q", output.Backups[0].PostgreSQLVersion)
	}
}

func TestCatalogListRequiresConfig(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := run([]string{"catalog", "list"}, &stdout, &stderr)

	if code != 2 {
		t.Fatalf("expected exit code 2, got %d", code)
	}
	if got := stderr.String(); !strings.Contains(got, "requires -f or -config") {
		t.Fatalf("expected missing config error, got: %s", got)
	}
}

func TestRunCommandExecutesWALLocalDrill(t *testing.T) {
	dir := t.TempDir()
	walgPath := filepath.Join(dir, "wal-g")
	pgVerifyBackupPath := filepath.Join(dir, "pg_verifybackup")
	postgresPath := filepath.Join(dir, "postgres")
	pgIsReadyPath := filepath.Join(dir, "pg_isready")
	psqlPath := filepath.Join(dir, "psql")
	pgAMCheckPath := filepath.Join(dir, "pg_amcheck")
	pgDumpPath := filepath.Join(dir, "pg_dump")
	configPath := filepath.Join(dir, "pgdrill.yaml")
	reportPath := filepath.Join(dir, "report.json")
	workDir := filepath.Join(dir, "restore")
	stopFile := filepath.Join(dir, "postgres-stopped")

	writeExecutable(t, walgPath, `#!/bin/sh
case "$1" in
  backup-list)
    cat <<'JSON'
[
  {
    "name": "base_00000001000000000000007F",
    "start_time": "2026-07-06T01:02:03Z",
    "finish_time": "2026-07-06T01:03:03Z",
    "wal_segment_backup_start": "00000001000000000000007F",
    "pg_version": 160005
  }
]
JSON
    ;;
  backup-fetch)
    mkdir -p "$2"
    ;;
  wal-verify)
    shift
    if [ "$1" != "--json" ] || [ "$2" != "--backup-name" ] || [ "$3" != "base_00000001000000000000007F" ] || [ "$4" != "integrity" ]; then
      echo "unexpected wal-g wal-verify args: $*" >&2
      exit 64
    fi
    cat <<'JSON'
{"integrity":{"status":"OK","details":[]}}
JSON
    ;;
  *)
    echo "unexpected wal-g args: $*" >&2
    exit 64
    ;;
esac
`)
	writeExecutable(t, pgVerifyBackupPath, `#!/bin/sh
if [ "$1" != "--exit-on-error" ] || [ "$2" != "--quiet" ]; then
  echo "unexpected pg_verifybackup args: $*" >&2
  exit 64
fi
last=""
for arg in "$@"; do last="$arg"; done
if [ "$last" != "$PGDRILL_EXPECT_DATA_DIR" ]; then
  echo "unexpected pg_verifybackup data dir: $last" >&2
  exit 64
fi
exit 0
`)
	writeExecutable(t, postgresPath, `#!/bin/sh
trap 'echo stopped > "$PGDRILL_STOP_FILE"; exit 0' TERM
while true; do sleep 1; done
`)
	writeExecutable(t, pgIsReadyPath, `#!/bin/sh
exit 0
`)
	writeExecutable(t, psqlPath, `#!/bin/sh
case "$*" in
  *"select 1"*) exit 0 ;;
  *) echo "unexpected psql args: $*" >&2; exit 64 ;;
esac
`)
	writeExecutable(t, pgAMCheckPath, `#!/bin/sh
case "$*" in
  *"postgresql://127.0.0.1:15432/postgres?sslmode=disable"*) exit 0 ;;
  *) echo "unexpected pg_amcheck args: $*" >&2; exit 64 ;;
esac
`)
	writeExecutable(t, pgDumpPath, `#!/bin/sh
case "$*" in
  *"--schema-only"*) exit 0 ;;
  *) echo "unexpected pg_dump args: $*" >&2; exit 64 ;;
esac
`)
	writeFile(t, configPath, `
cluster:
  name: test-main
provider:
  type: wal-g
  binary: `+walgPath+`
  wal_verify:
    enabled: true
restore:
  verify_backup:
    enabled: true
    binary: `+pgVerifyBackupPath+`
    timeout: 1s
    exit_on_error: true
    quiet: true
target:
  type: local
  work_dir: `+workDir+`
  postgres_binary: `+postgresPath+`
  postgres_port: 15432
  startup_timeout: 50ms
  shutdown_timeout: 2s
  env:
    PGDRILL_EXPECT_DATA_DIR: `+filepath.Join(workDir, "data")+`
    PGDRILL_STOP_FILE: `+stopFile+`
recovery:
  target: latest
probes:
  - type: pg_isready
    binary: `+pgIsReadyPath+`
    timeout: 1s
  - type: sql
    name: select_1
    binary: `+psqlPath+`
    query: "select 1"
    timeout: 1s
  - type: amcheck
    binary: `+pgAMCheckPath+`
    timeout: 1s
  - type: pg_dump
    binary: `+pgDumpPath+`
    mode: schema
    timeout: 1s
report:
  format: json
  path: `+reportPath+`
`)

	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "-f", configPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	if output := stdout.String(); !strings.Contains(output, "Status    passed") || !strings.Contains(output, "Report    "+reportPath) {
		t.Fatalf("unexpected run summary:\n%s", output)
	}
	result, err := report.ReadJSONFile(reportPath)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	if result.Status != model.DrillStatusPassed {
		t.Fatalf("expected passed report, got %#v", result)
	}
	if result.Backup.ID != "wal-g:base_00000001000000000000007F" {
		t.Fatalf("unexpected backup %q", result.Backup.ID)
	}
	if !hasCheck(result.Checks, model.ProbePGIsReady, model.CheckStatusPassed) {
		t.Fatalf("expected passed pg_isready check, got %#v", result.Checks)
	}
	if !hasCheck(result.Checks, model.ProbeSQL, model.CheckStatusPassed) {
		t.Fatalf("expected passed sql check, got %#v", result.Checks)
	}
	if !hasCheck(result.Checks, model.ProbeAMCheck, model.CheckStatusPassed) {
		t.Fatalf("expected passed amcheck check, got %#v", result.Checks)
	}
	if !hasCheck(result.Checks, model.ProbePGDump, model.CheckStatusPassed) {
		t.Fatalf("expected passed pg_dump check, got %#v", result.Checks)
	}
	if !hasCheckNamed(result.Checks, "wal-g-wal-verify-integrity", model.CheckStatusPassed) {
		t.Fatalf("expected passed wal-g wal-verify check, got %#v", result.Checks)
	}
	if !hasEvidenceKind(result.Evidence, model.EvidenceRuntime) {
		t.Fatalf("expected runtime evidence, got %#v", result.Evidence)
	}
	if !hasEvidenceKind(result.Evidence, model.EvidenceFile) {
		t.Fatalf("expected file evidence, got %#v", result.Evidence)
	}
	if !hasCommandEvidencePath(result.Evidence, pgVerifyBackupPath) {
		t.Fatalf("expected pg_verifybackup command evidence, got %#v", result.Evidence)
	}
	recoveryConfig, err := os.ReadFile(filepath.Join(workDir, "data", "postgresql.auto.conf"))
	if err != nil {
		t.Fatalf("read recovery config: %v", err)
	}
	if !strings.Contains(string(recoveryConfig), "restore_command") || !strings.Contains(string(recoveryConfig), "wal-fetch") {
		t.Fatalf("unexpected recovery config:\n%s", string(recoveryConfig))
	}
	if _, err := os.Stat(filepath.Join(workDir, "data", "recovery.signal")); err != nil {
		t.Fatalf("expected recovery.signal: %v", err)
	}
}

func TestRunCommandExecutesBarmanLocalDrill(t *testing.T) {
	dir := t.TempDir()
	barmanPath := filepath.Join(dir, "barman")
	postgresPath := filepath.Join(dir, "postgres")
	pgIsReadyPath := filepath.Join(dir, "pg_isready")
	configPath := filepath.Join(dir, "pgdrill.yaml")
	reportPath := filepath.Join(dir, "report.json")
	workDir := filepath.Join(dir, "restore")

	writeExecutable(t, barmanPath, `#!/bin/sh
if [ "$1" = "--config" ]; then
  shift 2
fi
case "$1" in
  --format)
    if [ "$2" != "json" ]; then
      echo "unexpected barman format args: $*" >&2
      exit 64
    fi
    case "$3" in
      list-backups)
        if [ "$4" != "main" ]; then
          echo "unexpected barman list args: $*" >&2
          exit 64
        fi
        cat <<'JSON'
[
  {
    "backup_id": "20240502T030405",
    "server_name": "main",
    "status": "DONE",
    "backup_type": "full",
    "begin_time": "2024-05-02T03:04:05Z",
    "end_time": "2024-05-02T03:14:05Z"
  }
]
JSON
        ;;
      show-backup)
        if [ "$4" != "main" ] || [ "$5" != "20240502T030405" ]; then
          echo "unexpected barman show-backup args: $*" >&2
          exit 64
        fi
        cat <<'JSON'
{
  "backup_id": "20240502T030405",
  "server_name": "main",
  "status": "DONE",
  "backup_type": "full",
  "begin_wal": "0000000100000000000000A1",
  "end_wal": "0000000100000000000000A2"
}
JSON
        ;;
      *)
        echo "unexpected barman json command: $*" >&2
        exit 64
        ;;
    esac
    ;;
  check)
    if [ "$2" != "main" ]; then
      echo "unexpected barman check args: $*" >&2
      exit 64
    fi
    ;;
  check-backup)
    if [ "$2" != "main" ] || [ "$3" != "20240502T030405" ]; then
      echo "unexpected barman check-backup args: $*" >&2
      exit 64
    fi
    ;;
  verify-backup)
    if [ "$2" != "main" ] || [ "$3" != "20240502T030405" ]; then
      echo "unexpected barman verify-backup args: $*" >&2
      exit 64
    fi
    ;;
  restore)
    dest=""
    for arg in "$@"; do dest="$arg"; done
    mkdir -p "$dest"
    ;;
  *)
    echo "unexpected barman args: $*" >&2
    exit 64
    ;;
esac
`)
	writeExecutable(t, postgresPath, `#!/bin/sh
trap 'exit 0' TERM
while true; do sleep 1; done
`)
	writeExecutable(t, pgIsReadyPath, `#!/bin/sh
exit 0
`)
	writeFile(t, configPath, `
cluster:
  name: test-main
provider:
  type: barman
  binary: `+barmanPath+`
  config_path: /etc/barman.conf
  server: main
  barman_verify_backup:
    enabled: true
target:
  type: local
  work_dir: `+workDir+`
  postgres_binary: `+postgresPath+`
  postgres_port: 15434
  startup_timeout: 50ms
  shutdown_timeout: 2s
recovery:
  target: timestamp
  value: "2026-07-06 01:02:03"
  timeline: latest
probes:
  - type: pg_isready
    binary: `+pgIsReadyPath+`
    timeout: 1s
report:
  format: json
  path: `+reportPath+`
`)

	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "-f", configPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}

	result, err := report.ReadJSONFile(reportPath)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	if result.Status != model.DrillStatusPassed {
		t.Fatalf("expected passed report, got %#v", result)
	}
	if result.Provider != model.ProviderBarman {
		t.Fatalf("unexpected provider %q", result.Provider)
	}
	if result.Backup.ID != "barman:main/20240502T030405" {
		t.Fatalf("unexpected backup %q", result.Backup.ID)
	}
	if !hasCheckNamed(result.Checks, "barman-check", model.CheckStatusPassed) {
		t.Fatalf("expected passed barman check, got %#v", result.Checks)
	}
	if !hasCheckNamed(result.Checks, "barman-check-backup", model.CheckStatusPassed) {
		t.Fatalf("expected passed barman check-backup, got %#v", result.Checks)
	}
	if !hasCheckNamed(result.Checks, "barman-show-backup", model.CheckStatusPassed) {
		t.Fatalf("expected passed barman show-backup, got %#v", result.Checks)
	}
	if !hasCheckNamed(result.Checks, "barman-verify-backup", model.CheckStatusPassed) {
		t.Fatalf("expected passed barman verify-backup, got %#v", result.Checks)
	}
	if !hasCheck(result.Checks, model.ProbePGIsReady, model.CheckStatusPassed) {
		t.Fatalf("expected passed pg_isready check, got %#v", result.Checks)
	}
}

func TestRunCommandExecutesPgBackRestLocalDrill(t *testing.T) {
	dir := t.TempDir()
	pgBackRestPath := filepath.Join(dir, "pgbackrest")
	postgresPath := filepath.Join(dir, "postgres")
	pgIsReadyPath := filepath.Join(dir, "pg_isready")
	configPath := filepath.Join(dir, "pgdrill.yaml")
	reportPath := filepath.Join(dir, "report.json")
	workDir := filepath.Join(dir, "restore")

	writeExecutable(t, pgBackRestPath, `#!/bin/sh
while [ $# -gt 0 ]; do
  case "$1" in
    --config=/etc/pgbackrest.conf|--stanza=main|--repo=1)
      shift
      ;;
    *)
      break
      ;;
  esac
done

case "$1" in
  info)
    if [ "$2" != "--output=json" ]; then
      echo "unexpected pgbackrest info args: $*" >&2
      exit 64
    fi
    cat <<'JSON'
[
  {
    "name": "main",
    "status": {"code": 0, "message": "ok"},
    "db": [{"id": 1, "system-id": 73924987654321, "version": "16"}],
    "backup": [
      {
        "label": "20240502-030405F",
        "type": "full",
        "error": false,
        "database": {"id": 1, "repo-key": 1},
        "archive": {"start": "0000000100000000000000A1", "stop": "0000000100000000000000A2"},
        "lsn": {"start": "0/A1000028", "stop": "0/A2000028"},
        "timestamp": {"start": 1714619045, "stop": 1714619645}
      }
    ]
  }
]
JSON
    ;;
  check)
    exit 0
    ;;
  restore)
    dest=""
    set=""
    saw_type=0
    saw_target=0
    saw_action=0
    for arg in "$@"; do
      case "$arg" in
        --set=*) set="${arg#--set=}" ;;
        --pg1-path=*) dest="${arg#--pg1-path=}" ;;
        --type=time) saw_type=1 ;;
        --target=2026-07-06\ 01:02:03) saw_target=1 ;;
        --target-action=promote) saw_action=1 ;;
      esac
    done
    if [ "$set" != "20240502-030405F" ] || [ -z "$dest" ] || [ "$saw_type" != "1" ] || [ "$saw_target" != "1" ] || [ "$saw_action" != "1" ]; then
      echo "unexpected pgbackrest restore args: $*" >&2
      exit 64
    fi
    mkdir -p "$dest"
    ;;
  *)
    echo "unexpected pgbackrest args: $*" >&2
    exit 64
    ;;
esac
`)
	writeExecutable(t, postgresPath, `#!/bin/sh
trap 'exit 0' TERM
while true; do sleep 1; done
`)
	writeExecutable(t, pgIsReadyPath, `#!/bin/sh
exit 0
`)
	writeFile(t, configPath, `
cluster:
  name: test-main
provider:
  type: pgbackrest
  binary: `+pgBackRestPath+`
  config_path: /etc/pgbackrest.conf
  stanza: main
  repo: "1"
  pgbackrest_check:
    enabled: true
target:
  type: local
  work_dir: `+workDir+`
  postgres_binary: `+postgresPath+`
  postgres_port: 15435
  startup_timeout: 50ms
  shutdown_timeout: 2s
recovery:
  target: timestamp
  value: "2026-07-06 01:02:03"
  timeline: latest
probes:
  - type: pg_isready
    binary: `+pgIsReadyPath+`
    timeout: 1s
report:
  format: json
  path: `+reportPath+`
`)

	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "-f", configPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}

	result, err := report.ReadJSONFile(reportPath)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	if result.Status != model.DrillStatusPassed {
		t.Fatalf("expected passed report, got %#v", result)
	}
	if result.Provider != model.ProviderPGBackRest {
		t.Fatalf("unexpected provider %q", result.Provider)
	}
	if result.Backup.ID != "pgbackrest:main/20240502-030405F" {
		t.Fatalf("unexpected backup %q", result.Backup.ID)
	}
	if !hasCheckNamed(result.Checks, "pgbackrest-check", model.CheckStatusPassed) {
		t.Fatalf("expected passed pgbackrest check, got %#v", result.Checks)
	}
	if !hasCheck(result.Checks, model.ProbePGIsReady, model.CheckStatusPassed) {
		t.Fatalf("expected passed pg_isready check, got %#v", result.Checks)
	}
}

func TestRunCommandRequiresReportPath(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "pgdrill.yaml")
	writeFile(t, configPath, `
provider:
  type: wal-g
target:
  type: local
`)

	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "-f", configPath}, &stdout, &stderr)

	if code != 2 {
		t.Fatalf("expected exit code 2, got %d", code)
	}
	if got := stderr.String(); !strings.Contains(got, "requires report.path") {
		t.Fatalf("expected report path error, got: %s", got)
	}
}

func TestReportShowCommandText(t *testing.T) {
	reportPath := filepath.Join(t.TempDir(), "drill.json")
	writeDrillReport(t, reportPath, model.DrillResult{
		ID:       "drill-1",
		Provider: model.ProviderWALG,
		Backup: model.Backup{
			ID:       "wal-g:base_1",
			Provider: model.ProviderWALG,
			Status:   model.BackupStatusAvailable,
		},
		Target:         model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: "/tmp/pgdrill/main"},
		RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetLatest},
		StartedAt:      mustTime(t, "2026-07-06T01:02:03Z"),
		FinishedAt:     mustTime(t, "2026-07-06T01:03:03Z"),
		Status:         model.DrillStatusFailed,
		Checks: []model.Check{
			{Name: "catalog", Status: model.CheckStatusPassed},
			{Name: "select_1", Probe: model.ProbeSQL, Status: model.CheckStatusFailed, Message: "query failed\nconnection closed"},
		},
		Evidence: []model.EvidenceRecord{{ID: "evidence-1"}},
	})

	var stdout, stderr bytes.Buffer
	code := run([]string{"report", "show", reportPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d: %s", code, stderr.String())
	}

	output := stdout.String()
	for _, expected := range []string{
		"ID        drill-1",
		"Status    failed",
		"Backup    wal-g:base_1",
		"Checks    1 passed, 1 failed",
		"select_1",
		"query failed connection closed",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected output to contain %q, got:\n%s", expected, output)
		}
	}
}

func TestReportShowCommandJSON(t *testing.T) {
	reportPath := filepath.Join(t.TempDir(), "drill.json")
	writeDrillReport(t, reportPath, model.DrillResult{
		ID:       "drill-json",
		Provider: model.ProviderBarman,
		Status:   model.DrillStatusPassed,
	})

	var stdout, stderr bytes.Buffer
	code := run([]string{"report", "show", "-format", "json", reportPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d: %s", code, stderr.String())
	}

	var result model.DrillResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("parse report json output: %v\n%s", err, stdout.String())
	}
	if result.ID != "drill-json" {
		t.Fatalf("unexpected report id %q", result.ID)
	}
	if result.Provider != model.ProviderBarman {
		t.Fatalf("unexpected provider %q", result.Provider)
	}
}

func TestReportMetricsCommandPrometheus(t *testing.T) {
	reportPath := filepath.Join(t.TempDir(), "drill.json")
	writeDrillReport(t, reportPath, model.DrillResult{
		ID:       "drill-metrics",
		Provider: model.ProviderPGBackRest,
		Target: model.TargetSpec{
			Type: model.RestoreTargetLocal,
		},
		RecoveryTarget: model.RecoveryTarget{
			Type: model.RecoveryTargetTimestamp,
		},
		StartedAt:  mustTime(t, "2026-07-06T01:02:03Z"),
		FinishedAt: mustTime(t, "2026-07-06T01:04:03Z"),
		Status:     model.DrillStatusPassed,
		Checks: []model.Check{
			{Name: "pgbackrest-check", Status: model.CheckStatusPassed},
		},
	})

	var stdout, stderr bytes.Buffer
	code := run([]string{"report", "metrics", reportPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d: %s", code, stderr.String())
	}

	output := stdout.String()
	for _, expected := range []string{
		"# TYPE pgdrill_drill_status gauge",
		`pgdrill_drill_status{provider="pgbackrest",target_type="local",recovery_target="timestamp",status="passed"} 1`,
		`pgdrill_drill_duration_seconds{provider="pgbackrest",target_type="local",recovery_target="timestamp",status="passed"} 120`,
		`pgdrill_checks_total{provider="pgbackrest",check="pgbackrest-check",probe="unknown",status="passed"} 1`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected metrics output to contain %q, got:\n%s", expected, output)
		}
	}
}

func TestReportShowRequiresPath(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := run([]string{"report", "show"}, &stdout, &stderr)

	if code != 2 {
		t.Fatalf("expected exit code 2, got %d", code)
	}
	if got := stderr.String(); !strings.Contains(got, "requires exactly one report path") {
		t.Fatalf("expected missing path error, got: %s", got)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}
}

func writeDrillReport(t *testing.T, path string, result model.DrillResult) {
	t.Helper()
	if err := (report.JSONFileSink{Path: path}).Write(context.Background(), result); err != nil {
		t.Fatalf("write drill report %s: %v", path, err)
	}
}

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse time %s: %v", value, err)
	}
	return parsed
}

func hasCheck(checks []model.Check, probe model.ProbeType, status model.CheckStatus) bool {
	for _, check := range checks {
		if check.Probe == probe && check.Status == status {
			return true
		}
	}
	return false
}

func hasCheckNamed(checks []model.Check, name string, status model.CheckStatus) bool {
	for _, check := range checks {
		if check.Name == name && check.Status == status {
			return true
		}
	}
	return false
}

func hasEvidenceKind(records []model.EvidenceRecord, kind model.EvidenceKind) bool {
	for _, record := range records {
		if record.Kind == kind {
			return true
		}
	}
	return false
}

func hasCommandEvidencePath(records []model.EvidenceRecord, path string) bool {
	for _, record := range records {
		if record.Command != nil && record.Command.Path == path {
			return true
		}
	}
	return false
}
