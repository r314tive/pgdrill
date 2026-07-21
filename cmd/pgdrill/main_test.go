package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/r314tive/pgdrill/internal/model"
	"github.com/r314tive/pgdrill/internal/preflight"
	"github.com/r314tive/pgdrill/internal/report"
	"github.com/r314tive/pgdrill/internal/runspec"
	"gopkg.in/yaml.v3"
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
	var overview model.Overview
	if err := json.Unmarshal(stdout.Bytes(), &overview); err != nil {
		t.Fatalf("parse explain output: %v\n%s", err, stdout.String())
	}
	if len(overview.Providers) == 0 {
		t.Fatalf("expected providers in json output, got: %s", stdout.String())
	}
	if got, want := overview.TargetCapabilities.Run, []model.RestoreTargetType{model.RestoreTargetLocal}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected run target capabilities: got %#v want %#v", got, want)
	}
	if got, want := overview.TargetCapabilities.Verify, []model.RestoreTargetType{model.RestoreTargetKubernetes}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected verify target capabilities: got %#v want %#v", got, want)
	}
}

func TestExplainTextDistinguishesImplementedTargetPaths(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := run([]string{"explain"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("expected exit code 0, got %d: %s", code, stderr.String())
	}
	for _, want := range []string{
		"pgdrill run              local",
		"pgdrill target verify    kubernetes (CloudNativePG)",
		"Canonical but not yet executable target type: container.",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("expected %q in explain output:\n%s", want, stdout.String())
		}
	}
}

func TestRunRejectsKubernetesTargetBeforeNativePreflight(t *testing.T) {
	dir := t.TempDir()
	invokedPath := filepath.Join(dir, "native-invoked")
	nativePath := filepath.Join(dir, "native")
	writeExecutable(t, nativePath, `#!/bin/sh
printf 'invoked\n' > "`+invokedPath+`"
exit 0
`)
	configPath := filepath.Join(dir, "pgdrill.yaml")
	writeFile(t, configPath, `
provider:
  type: wal-g
  binary: `+nativePath+`
target:
  type: kubernetes
probes:
  - preset: readiness
report:
  path: `+filepath.Join(dir, "report.json")+`
`)

	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "-f", configPath}, &stdout, &stderr)

	if code != 1 || !strings.Contains(stderr.String(), `full restore drills support target.type "local"`) {
		t.Fatalf("expected full-drill target validation, code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(invokedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("run invoked native preflight before target validation, stat err=%v", err)
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

func TestDoctorCommandJSONChecksKubernetesClientWithoutServer(t *testing.T) {
	dir := t.TempDir()
	kubectlPath := filepath.Join(dir, "kubectl")
	configPath := filepath.Join(dir, "pgdrill.yaml")
	writeExecutable(t, kubectlPath, `#!/bin/sh
if [ "$1" != "version" ] || [ "$2" != "--client" ] || [ "$3" != "--output=json" ]; then
  echo "unexpected args: $*" >&2
  exit 64
fi
printf '%s\n' '{"clientVersion":{"gitVersion":"v1.34.1"}}'
`)
	writeFile(t, configPath, `
target:
  type: kubernetes
  kubernetes:
    kubectl_binary: `+kubectlPath+`
`)

	var stdout, stderr bytes.Buffer
	code := run([]string{"doctor", "-f", configPath, "-format", "json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d: %s", code, stderr.String())
	}
	var result preflight.Result
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("parse doctor output: %v\n%s", err, stdout.String())
	}
	if result.SchemaVersion != preflight.CurrentSchemaVersion || result.Status != model.DrillStatusPassed {
		t.Fatalf("unexpected doctor result %#v", result)
	}
	if result.PGDrillVersion == "" || result.Target != model.RestoreTargetKubernetes || result.Provider != "" {
		t.Fatalf("unexpected doctor subject %#v", result)
	}
	if len(result.Checks) != 1 || result.Checks[0].Attributes["version"] != "v1.34.1" {
		t.Fatalf("unexpected doctor checks %#v", result.Checks)
	}
	if len(result.Evidence) != 1 || result.Evidence[0].Command == nil || result.Evidence[0].Command.ResolvedPath == "" {
		t.Fatalf("expected resolved command evidence, got %#v", result.Evidence)
	}
}

func TestDoctorCommandReportsMissingTool(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "pgdrill.yaml")
	writeFile(t, configPath, `
target:
  type: kubernetes
  kubernetes:
    kubectl_binary: /definitely/missing/kubectl
`)

	var stdout, stderr bytes.Buffer
	code := run([]string{"doctor", "-f", configPath}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Status    failed") || !strings.Contains(stdout.String(), "kubectl") {
		t.Fatalf("unexpected doctor output:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "1 unavailable tool") {
		t.Fatalf("unexpected doctor error: %s", stderr.String())
	}
}

func TestDoctorCommandRequiresProviderForLocalDrill(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "pgdrill.yaml")
	writeFile(t, configPath, `
target:
  type: local
  work_dir: /tmp/pgdrill
`)

	var stdout, stderr bytes.Buffer
	code := run([]string{"doctor", "-f", configPath}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "provider.type is required") {
		t.Fatalf("expected local provider validation, code=%d stderr=%s", code, stderr.String())
	}
}

func TestDoctorCommandRejectsNonEmptyLocalWorkDirBeforeNativePreflight(t *testing.T) {
	dir := t.TempDir()
	workDir := filepath.Join(dir, "restore")
	if err := os.Mkdir(workDir, 0o700); err != nil {
		t.Fatalf("create workdir: %v", err)
	}
	writeFile(t, filepath.Join(workDir, "important.txt"), "keep\n")
	invokedPath := filepath.Join(dir, "native-invoked")
	nativePath := filepath.Join(dir, "native")
	writeExecutable(t, nativePath, `#!/bin/sh
printf 'invoked\n' > "`+invokedPath+`"
exit 0
`)
	configPath := filepath.Join(dir, "pgdrill.yaml")
	writeFile(t, configPath, `
provider:
  type: wal-g
  binary: `+nativePath+`
target:
  type: local
  work_dir: `+workDir+`
  postgres_binary: `+nativePath+`
probes:
  - type: pg_isready
    binary: `+nativePath+`
`)

	var stdout, stderr bytes.Buffer
	code := run([]string{"doctor", "-f", configPath}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "work_dir must be empty") {
		t.Fatalf("expected read-only target validation, code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(invokedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("doctor ran native preflight before target validation, stat err=%v", err)
	}
}

func TestCommandsRejectInvalidProbeBeforeNativePreflight(t *testing.T) {
	tests := []struct {
		name string
		args func(string) []string
	}{
		{name: "run", args: func(path string) []string { return []string{"run", "-f", path} }},
		{name: "doctor", args: func(path string) []string { return []string{"doctor", "-f", path} }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			invokedPath := filepath.Join(dir, "native-invoked")
			nativePath := filepath.Join(dir, "native")
			writeExecutable(t, nativePath, `#!/bin/sh
printf 'invoked\n' > "`+invokedPath+`"
exit 0
`)
			configPath := filepath.Join(dir, "pgdrill.yaml")
			writeFile(t, configPath, `
provider:
  type: wal-g
  binary: `+nativePath+`
target:
  type: local
  work_dir: `+filepath.Join(dir, "restore")+`
  postgres_binary: `+nativePath+`
probes:
  - type: pg_dump
    binary: `+nativePath+`
    mode: custom
report:
  path: `+filepath.Join(dir, "report.json")+`
`)

			var stdout, stderr bytes.Buffer
			code := run(tt.args(configPath), &stdout, &stderr)
			if code != 1 || !strings.Contains(stderr.String(), `unsupported pg_dump mode "custom"`) {
				t.Fatalf("expected semantic config failure, code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
			}
			if _, err := os.Stat(invokedPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("native command ran before semantic validation, stat err=%v", err)
			}
		})
	}
}

func TestDoctorCommandReturnsInterruptedExitCode(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "pgdrill.yaml")
	writeFile(t, configPath, `
target:
  type: kubernetes
`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var stdout, stderr bytes.Buffer
	code := runContext(ctx, []string{"doctor", "-f", configPath, "-format", "json"}, &stdout, &stderr)
	if code != exitCodeInterrupted {
		t.Fatalf("expected interrupted exit code, got %d: %s", code, stderr.String())
	}
	var result preflight.Result
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("parse doctor output: %v\n%s", err, stdout.String())
	}
	if result.Status != model.DrillStatusAborted || len(result.Checks) != 0 {
		t.Fatalf("unexpected aborted doctor result %#v", result)
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

func TestTargetManifestCommandRendersCNPGManifest(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "pgdrill.yaml")
	writeFile(t, configPath, `
cluster:
  name: altbox
target:
  type: kubernetes
  labels:
    env: d003
  kubernetes:
    namespace: d003-db
  cnpg:
    source_cluster: altbox
    backup_name: altbox-backup-20260707
    image_name: ghcr.io/cloudnative-pg/postgresql:16
    storage_size: 20Gi
    cpu_request: 500m
    memory_request: 1Gi
    node_label_key: node-role.kubernetes.io/database
    node_label_value: "true"
`)

	var stdout, stderr bytes.Buffer
	code := run([]string{"target", "manifest", "-f", configPath, "-drill-id", "drill-1"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d: %s", code, stderr.String())
	}

	var manifest map[string]any
	if err := yaml.Unmarshal(stdout.Bytes(), &manifest); err != nil {
		t.Fatalf("parse manifest yaml: %v\n%s", err, stdout.String())
	}

	metadata := requireMap(t, manifest["metadata"])
	spec := requireMap(t, manifest["spec"])
	labels := requireMap(t, metadata["labels"])
	bootstrap := requireMap(t, spec["bootstrap"])
	recovery := requireMap(t, bootstrap["recovery"])
	backup := requireMap(t, recovery["backup"])
	storage := requireMap(t, spec["storage"])
	resources := requireMap(t, spec["resources"])
	requests := requireMap(t, resources["requests"])

	if manifest["apiVersion"] != "postgresql.cnpg.io/v1" || manifest["kind"] != "Cluster" {
		t.Fatalf("unexpected manifest identity %#v", manifest)
	}
	if metadata["namespace"] != "d003-db" {
		t.Fatalf("unexpected metadata %#v", metadata)
	}
	name, ok := metadata["name"].(string)
	if !ok || !strings.HasPrefix(name, "verify-altbox-") {
		t.Fatalf("unexpected generated name %#v", metadata["name"])
	}
	if labels["app.kubernetes.io/managed-by"] != "pgdrill" || labels["env"] != "d003" {
		t.Fatalf("unexpected labels %#v", labels)
	}
	if spec["imageName"] != "ghcr.io/cloudnative-pg/postgresql:16" {
		t.Fatalf("unexpected image name %#v", spec["imageName"])
	}
	if backup["name"] != "altbox-backup-20260707" {
		t.Fatalf("unexpected recovery backup %#v", backup)
	}
	if storage["size"] != "20Gi" {
		t.Fatalf("unexpected storage %#v", storage)
	}
	if requests["cpu"] != "500m" || requests["memory"] != "1Gi" {
		t.Fatalf("unexpected requests %#v", requests)
	}
	if !strings.Contains(stdout.String(), "node-role.kubernetes.io/database") {
		t.Fatalf("expected node affinity in manifest:\n%s", stdout.String())
	}
}

func TestTargetManifestCommandDiscoversCNPGInputs(t *testing.T) {
	dir := t.TempDir()
	kubectlPath := filepath.Join(dir, "kubectl")
	configPath := filepath.Join(dir, "pgdrill.yaml")

	writeExecutable(t, kubectlPath, `#!/bin/sh
case "$*" in
  *"get backups.postgresql.cnpg.io -o json"*)
    cat <<'JSON'
{
  "items": [
    {
      "metadata": {"name": "altbox-old", "creationTimestamp": "2026-07-06T01:00:00Z"},
      "spec": {"cluster": {"name": "altbox"}},
      "status": {"phase": "completed"}
    },
    {
      "metadata": {"name": "altbox-new", "creationTimestamp": "2026-07-07T01:00:00Z"},
      "spec": {"cluster": {"name": "altbox"}},
      "status": {"phase": "completed"}
    }
  ]
}
JSON
    ;;
  *"get cluster.postgresql.cnpg.io altbox -o json"*)
    cat <<'JSON'
{"spec":{"imageName":"ghcr.io/cloudnative-pg/postgresql:16.4"}}
JSON
    ;;
  *)
    echo "unexpected kubectl args: $*" >&2
    exit 64
    ;;
esac
`)
	writeFile(t, configPath, `
cluster:
  name: altbox
target:
  type: kubernetes
  kubernetes:
    namespace: d003-db
    kubectl_binary: `+kubectlPath+`
  cnpg:
    source_cluster: altbox
`)

	var stdout, stderr bytes.Buffer
	code := run([]string{"target", "manifest", "-f", configPath, "-discover", "-drill-id", "drill-1"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d: %s", code, stderr.String())
	}

	var manifest map[string]any
	if err := yaml.Unmarshal(stdout.Bytes(), &manifest); err != nil {
		t.Fatalf("parse manifest yaml: %v\n%s", err, stdout.String())
	}

	spec := requireMap(t, manifest["spec"])
	bootstrap := requireMap(t, spec["bootstrap"])
	recovery := requireMap(t, bootstrap["recovery"])
	backup := requireMap(t, recovery["backup"])
	if backup["name"] != "altbox-new" {
		t.Fatalf("unexpected discovered backup %#v", backup)
	}
	if spec["imageName"] != "ghcr.io/cloudnative-pg/postgresql:16.4" {
		t.Fatalf("unexpected discovered image %#v", spec["imageName"])
	}
}

func TestTargetVerifyRequiresCreateConfirmation(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := run([]string{"target", "verify", "-f", "pgdrill.yaml"}, &stdout, &stderr)

	if code != 2 {
		t.Fatalf("expected exit code 2, got %d", code)
	}
	if got := stderr.String(); !strings.Contains(got, "requires -confirm-create") {
		t.Fatalf("expected confirmation error, got: %s", got)
	}
}

func TestTargetVerifyCommandRunsCNPGLifecycleAndProbes(t *testing.T) {
	dir := t.TempDir()
	kubectlPath := filepath.Join(dir, "kubectl")
	configPath := filepath.Join(dir, "pgdrill.yaml")
	reportPath := filepath.Join(dir, "cnpg-report.json")

	writeExecutable(t, kubectlPath, `#!/bin/sh
case "$*" in
  "version --client --output=json")
    echo '{"clientVersion":{"gitVersion":"v1.34.1"}}'
    ;;
  *" get backups.postgresql.cnpg.io -o json"*)
    cat <<'JSON'
{"items":[{"metadata":{"name":"altbox-backup-20260707","creationTimestamp":"2026-07-07T01:00:00Z"},"spec":{"cluster":{"name":"altbox"}},"status":{"phase":"completed"}}]}
JSON
    ;;
  *" get cluster.postgresql.cnpg.io altbox -o json"*)
    echo '{"spec":{"imageName":"ghcr.io/cloudnative-pg/postgresql:16"}}'
    ;;
  *" create -f -"*)
    manifest="$(cat)"
    case "$manifest" in
      *"name: altbox-backup-20260707"*) exit 0 ;;
      *) echo "manifest did not contain expected backup" >&2; exit 64 ;;
    esac
    ;;
  *" get pods -l cnpg.io/cluster=verify-altbox-test,cnpg.io/jobRole=full-recovery -o json"*)
    cat <<'JSON'
{"items":[]}
JSON
    ;;
  *" get pod verify-altbox-test-1 -o json"*)
    cat <<'JSON'
{"status":{"conditions":[{"type":"Ready","status":"True"}]}}
JSON
    ;;
  *" exec verify-altbox-test-1 -c postgres -- /usr/local/bin/psql --version"*)
    echo "psql (PostgreSQL) 16.4"
    ;;
  *" exec verify-altbox-test-1 -c postgres -- /usr/local/bin/psql -X -v ON_ERROR_STOP=1 -d host=/controller/run dbname=postgres user=postgres -c select 1"*)
    echo "1"
    ;;
  *" get cluster.postgresql.cnpg.io verify-altbox-test -o yaml"*)
    echo "kind: Cluster"
    ;;
  *" get pods -l cnpg.io/cluster=verify-altbox-test -o wide"*)
    echo "verify-altbox-test-1 Running"
    ;;
  *" describe pod verify-altbox-test-1"*)
    echo "pod description"
    ;;
  *" get pvc -l cnpg.io/cluster=verify-altbox-test -o wide"*)
    echo "verify-altbox-test-1 pvc"
    ;;
  *" get events --sort-by=.metadata.creationTimestamp"*)
    echo "Normal Ready"
    ;;
  *" describe job/verify-altbox-test-1-full-recovery"*)
    echo "full recovery job description"
    ;;
  *" logs job/verify-altbox-test-1-full-recovery --timestamps --tail=25"*)
    echo "full recovery complete"
    ;;
  *" logs job/verify-altbox-test-1-full-recovery -c bootstrap-controller --timestamps --tail=25"*)
    echo "full recovery bootstrap complete"
    ;;
  *" logs verify-altbox-test-1 -c postgres --timestamps --tail=25"*)
    echo "postgres ready"
    ;;
  *" logs verify-altbox-test-1 -c bootstrap-controller --timestamps --tail=25"*)
    echo "postgres bootstrap ready"
    ;;
  *" delete cluster.postgresql.cnpg.io -l pgdrill.io/ownership-id="*" --ignore-not-found=true --wait=true --timeout=5s"*)
    echo "cluster deleted"
    ;;
  *" delete pvc -l cnpg.io/cluster=verify-altbox-test,pgdrill.io/ownership-id="*" --ignore-not-found=true --wait=true --timeout=5s"*)
    echo "pvc deleted"
    ;;
  *)
    echo "unexpected kubectl args: $*" >&2
    exit 64
    ;;
esac
`)
	writeFile(t, configPath, `
cluster:
  name: altbox
provider:
  type: wal-g
target:
  type: kubernetes
  labels:
    env: d003
  kubernetes:
    namespace: d003-db
    kubectl_binary: `+kubectlPath+`
    command_timeout: 5s
    wait_timeout: 5s
    cleanup_pvc: true
    capture_logs: true
    events_tail: 10
    postgres_log_tail: 25
  cnpg:
    source_cluster: altbox
    verify_cluster_name: verify-altbox-test
probes:
  - type: sql
    name: select_1
    binary: /usr/local/bin/psql
    query: "select 1"
    timeout: 5s
report:
  format: json
  path: `+reportPath+`
`)

	var stdout, stderr bytes.Buffer
	code := run([]string{"target", "verify", "-f", configPath, "-discover", "-confirm-create", "-drill-id", "cnpg-drill-1"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	if output := stdout.String(); !strings.Contains(output, "Status       passed") || !strings.Contains(output, "Report       "+reportPath) {
		t.Fatalf("unexpected verify summary:\n%s", output)
	}

	result, err := report.ReadJSONFile(reportPath)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	if result.ID != "cnpg-drill-1" || result.Status != model.DrillStatusPassed {
		t.Fatalf("unexpected result id/status %#v", result)
	}
	if result.Cluster != "altbox" || !strings.Contains(stdout.String(), "Cluster      altbox") {
		t.Fatalf("expected configured cluster in report and summary: cluster=%q summary=%s", result.Cluster, stdout.String())
	}
	if result.SchemaVersion != model.CurrentReportSchemaVersion {
		t.Fatalf("unexpected report schema version %q", result.SchemaVersion)
	}
	if result.Backup.ID != "cnpg:altbox-backup-20260707" {
		t.Fatalf("unexpected backup %#v", result.Backup)
	}
	if result.Provider != "" || result.Backup.Provider != "" {
		t.Fatalf("target verification must not claim its unused configured provider: %#v", result)
	}
	if !hasCheckNamed(result.Checks, "cnpg-instance-ready", model.CheckStatusPassed) {
		t.Fatalf("expected cnpg readiness check, got %#v", result.Checks)
	}
	if !hasCheck(result.Checks, model.ProbeSQL, model.CheckStatusPassed) {
		t.Fatalf("expected sql probe check, got %#v", result.Checks)
	}
	for _, name := range []string{"tool.kubectl", "tool.psql"} {
		if !hasCheckNamed(result.Checks, name, model.CheckStatusPassed) {
			t.Fatalf("expected passed preflight check %q, got %#v", name, result.Checks)
		}
	}
	for _, operation := range []string{
		"kubectl-discover-cnpg-backups",
		"kubectl-discover-cnpg-source-image",
		"cnpg-manifest-render",
		"kubectl-create-cluster",
		"kubectl-check-full-recovery",
		"kubectl-check-instance-ready",
		"kubectl-capture-instance-describe",
		"kubectl-capture-full-recovery-describe",
		"kubectl-capture-full-recovery-bootstrap-log",
		"kubectl-capture-postgres-log",
		"kubectl-capture-postgres-bootstrap-log",
		"kubectl-delete-cluster",
		"kubectl-delete-pvcs",
	} {
		if !hasEvidenceOperation(result.Evidence, operation) {
			t.Fatalf("expected evidence operation %q, got %#v", operation, result.Evidence)
		}
	}

	configData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config for remote preflight failure: %v", err)
	}
	writeFile(t, configPath, strings.ReplaceAll(string(configData), "/usr/local/bin/psql", "/missing/psql"))
	stdout.Reset()
	stderr.Reset()
	code = run([]string{"target", "verify", "-f", configPath, "-discover", "-confirm-create", "-drill-id", "cnpg-drill-preflight-failure"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected remote preflight failure exit code 1, got %d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	result, err = report.ReadJSONFile(reportPath)
	if err != nil {
		t.Fatalf("read remote preflight failure report: %v", err)
	}
	if result.Status != model.DrillStatusFailed || result.Failure == nil || result.Failure.Stage != model.DrillStageProbeExecution {
		t.Fatalf("unexpected remote preflight failure result %#v", result)
	}
	if !hasCheckNamed(result.Checks, "tool.psql", model.CheckStatusFailed) {
		t.Fatalf("expected failed restored-target psql preflight, got %#v", result.Checks)
	}
	for _, operation := range []string{"kubectl-delete-cluster", "kubectl-delete-pvcs"} {
		if !hasEvidenceOperation(result.Evidence, operation) {
			t.Fatalf("expected cleanup evidence %q after remote preflight failure, got %#v", operation, result.Evidence)
		}
	}
}

func TestTargetVerifyWritesDiscoveryFailureReport(t *testing.T) {
	dir := t.TempDir()
	kubectlPath := filepath.Join(dir, "kubectl")
	configPath := filepath.Join(dir, "pgdrill.yaml")
	reportPath := filepath.Join(dir, "cnpg-report.json")

	writeExecutable(t, kubectlPath, `#!/bin/sh
if [ "$*" = "version --client --output=json" ]; then
  echo '{"clientVersion":{"gitVersion":"v1.34.1"}}'
  exit 0
fi
echo "forbidden" >&2
exit 42
`)
	writeFile(t, configPath, `
target:
  type: kubernetes
  kubernetes:
    namespace: d003-db
    kubectl_binary: `+kubectlPath+`
  cnpg:
    source_cluster: altbox
    image_name: ghcr.io/cloudnative-pg/postgresql:16
probes:
  - type: sql
    query: "select 1"
report:
  format: json
  path: `+reportPath+`
`)

	var stdout, stderr bytes.Buffer
	code := run([]string{"target", "verify", "-f", configPath, "-discover", "-confirm-create", "-drill-id", "cnpg-discovery-failure"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "discover target verify inputs") {
		t.Fatalf("expected discovery failure, got %q", stderr.String())
	}

	result, err := report.ReadJSONFile(reportPath)
	if err != nil {
		t.Fatalf("read discovery failure report: %v", err)
	}
	if result.ID != "cnpg-discovery-failure" || result.Status != model.DrillStatusFailed {
		t.Fatalf("unexpected failure result %#v", result)
	}
	if result.Failure == nil || result.Failure.Stage != model.DrillStageTargetDiscovery {
		t.Fatalf("expected target discovery failure, got %#v", result.Failure)
	}
	if !hasCheckNamed(result.Checks, "cnpg-input-discovery", model.CheckStatusFailed) {
		t.Fatalf("expected failed discovery check, got %#v", result.Checks)
	}
	if !hasEvidenceOperation(result.Evidence, "kubectl-discover-cnpg-backups") {
		t.Fatalf("expected discovery command evidence, got %#v", result.Evidence)
	}
	if len(result.Evidence) != 2 || result.Evidence[1].Command == nil || result.Evidence[1].Command.ExitStatus.ExitCode != 42 {
		t.Fatalf("expected preflight and structured discovery exit evidence, got %#v", result.Evidence)
	}
}

func TestTargetVerifyWritesAbortedDiscoveryReport(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "pgdrill.yaml")
	reportPath := filepath.Join(dir, "cnpg-report.json")
	writeFile(t, configPath, `
target:
  type: kubernetes
  kubernetes:
    namespace: d003-db
    kubectl_binary: kubectl
  cnpg:
    source_cluster: altbox
    image_name: ghcr.io/cloudnative-pg/postgresql:16
probes:
  - type: sql
    query: "select 1"
report:
  format: json
  path: `+reportPath+`
`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var stdout, stderr bytes.Buffer
	code := runContext(ctx, []string{"target", "verify", "-f", configPath, "-discover", "-confirm-create", "-drill-id", "cnpg-discovery-aborted"}, &stdout, &stderr)

	if code != exitCodeInterrupted {
		t.Fatalf("expected exit code %d, got %d\nstdout:\n%s\nstderr:\n%s", exitCodeInterrupted, code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "target verify aborted") {
		t.Fatalf("expected aborted message, got %q", stderr.String())
	}
	result, err := report.ReadJSONFile(reportPath)
	if err != nil {
		t.Fatalf("read aborted discovery report: %v", err)
	}
	if result.Status != model.DrillStatusAborted {
		t.Fatalf("expected aborted report, got %#v", result)
	}
	if result.Failure == nil || result.Failure.Stage != model.DrillStageRequestValidation {
		t.Fatalf("expected aborted request validation failure, got %#v", result.Failure)
	}
	if len(result.Evidence) != 0 {
		t.Fatalf("context canceled before command start must not invent evidence, got %#v", result.Evidence)
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
  --version)
    echo "WAL-G v3.0.7"
    ;;
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
if [ "$1" = "--version" ]; then
  echo "pg_verifybackup (PostgreSQL) 16.4"
  exit 0
fi
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
if [ "$1" = "--version" ]; then
  echo "postgres (PostgreSQL) 16.4"
  exit 0
fi
trap 'echo stopped > "$PGDRILL_STOP_FILE"; exit 0' TERM
while true; do sleep 1; done
`)
	writeExecutable(t, pgIsReadyPath, `#!/bin/sh
if [ "$1" = "--version" ]; then echo "pg_isready (PostgreSQL) 16.4"; fi
exit 0
`)
	writeExecutable(t, psqlPath, `#!/bin/sh
case "$*" in
  "--version") echo "psql (PostgreSQL) 16.4" ;;
  *"select 1"*) exit 0 ;;
  *) echo "unexpected psql args: $*" >&2; exit 64 ;;
esac
`)
	writeExecutable(t, pgAMCheckPath, `#!/bin/sh
case "$*" in
  "--version") echo "pg_amcheck (PostgreSQL) 16.4" ;;
  *"postgresql://127.0.0.1:15432/postgres?sslmode=disable"*) exit 0 ;;
  *) echo "unexpected pg_amcheck args: $*" >&2; exit 64 ;;
esac
`)
	writeExecutable(t, pgDumpPath, `#!/bin/sh
case "$*" in
  "--version") echo "pg_dump (PostgreSQL) 16.4" ;;
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
    timeout: 5s
    exit_on_error: true
    quiet: true
target:
  type: local
  work_dir: `+workDir+`
  postgres_binary: `+postgresPath+`
  postgres_port: 15432
  startup_timeout: 100ms
  shutdown_timeout: 5s
  env:
    PGDRILL_EXPECT_DATA_DIR: `+filepath.Join(workDir, "data")+`
    PGDRILL_STOP_FILE: `+stopFile+`
recovery:
  target: latest
probes:
  - type: pg_isready
    binary: `+pgIsReadyPath+`
    timeout: 5s
  - type: sql
    name: select_1
    binary: `+psqlPath+`
    query: "select 1"
    timeout: 5s
  - type: amcheck
    binary: `+pgAMCheckPath+`
    timeout: 5s
  - type: pg_dump
    binary: `+pgDumpPath+`
    mode: schema
    timeout: 5s
report:
  format: json
  path: `+reportPath+`
`)

	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "-f", configPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	if output := stdout.String(); !strings.Contains(output, "Status       passed") || !strings.Contains(output, "Report       "+reportPath) {
		t.Fatalf("unexpected run summary:\n%s", output)
	}
	result, err := report.ReadJSONFile(reportPath)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	if result.Status != model.DrillStatusPassed {
		t.Fatalf("expected passed report, got %#v", result)
	}
	if result.PGDrillVersion == "" {
		t.Fatal("expected pgdrill version in report")
	}
	for _, name := range []string{"tool.wal-g", "tool.postgres", "tool.pg_verifybackup", "tool.pg_isready", "tool.psql", "tool.pg_amcheck", "tool.pg_dump"} {
		if !hasCheckNamed(result.Checks, name, model.CheckStatusPassed) {
			t.Fatalf("expected passed preflight check %q, got %#v", name, result.Checks)
		}
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
  --version)
    echo "3.17.0"
    ;;
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
  generate-manifest)
    if [ "$2" != "main" ] || [ "$3" != "20240502T030405" ]; then
      echo "unexpected barman generate-manifest args: $*" >&2
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
if [ "$1" = "--version" ]; then
  echo "postgres (PostgreSQL) 16.4"
  exit 0
fi
trap 'exit 0' TERM
while true; do sleep 1; done
`)
	writeExecutable(t, pgIsReadyPath, `#!/bin/sh
if [ "$1" = "--version" ]; then echo "pg_isready (PostgreSQL) 16.4"; fi
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
  barman_generate_manifest:
    enabled: true
target:
  type: local
  work_dir: `+workDir+`
  postgres_binary: `+postgresPath+`
  postgres_port: 15434
  startup_timeout: 100ms
  shutdown_timeout: 5s
recovery:
  target: timestamp
  value: "2026-07-06T01:02:03Z"
  timeline: latest
probes:
  - type: pg_isready
    binary: `+pgIsReadyPath+`
    timeout: 5s
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
	for _, name := range []string{"tool.barman", "tool.postgres", "tool.pg_isready"} {
		if !hasCheckNamed(result.Checks, name, model.CheckStatusPassed) {
			t.Fatalf("expected passed preflight check %q, got %#v", name, result.Checks)
		}
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
	if !hasCheckNamed(result.Checks, "barman-generate-manifest", model.CheckStatusPassed) {
		t.Fatalf("expected passed barman generate-manifest, got %#v", result.Checks)
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
  version)
    echo "pgBackRest 2.57.0"
    ;;
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
  verify)
    set=""
    saw_output=0
    for arg in "$@"; do
      case "$arg" in
        --set=*) set="${arg#--set=}" ;;
        --output=text) saw_output=1 ;;
      esac
    done
    if [ "$set" != "20240502-030405F" ] || [ "$saw_output" != "1" ]; then
      echo "unexpected pgbackrest verify args: $*" >&2
      exit 64
    fi
    ;;
  restore)
    dest=""
    set=""
    saw_reset=0
    saw_type=0
    saw_target=0
    saw_action=0
    for arg in "$@"; do
      case "$arg" in
        --set=*) set="${arg#--set=}" ;;
        --pg1-path=*) dest="${arg#--pg1-path=}" ;;
        --reset-pg1-host) saw_reset=1 ;;
        --type=time) saw_type=1 ;;
        --target=2026-07-06T01:02:03Z) saw_target=1 ;;
        --target-action=promote) saw_action=1 ;;
      esac
    done
    if [ "$set" != "20240502-030405F" ] || [ -z "$dest" ] || [ "$saw_reset" != "1" ] || [ "$saw_type" != "1" ] || [ "$saw_target" != "1" ] || [ "$saw_action" != "1" ]; then
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
if [ "$1" = "--version" ]; then
  echo "postgres (PostgreSQL) 16.4"
  exit 0
fi
trap 'exit 0' TERM
while true; do sleep 1; done
`)
	writeExecutable(t, pgIsReadyPath, `#!/bin/sh
if [ "$1" = "--version" ]; then echo "pg_isready (PostgreSQL) 16.4"; fi
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
  pgbackrest_verify:
    enabled: true
target:
  type: local
  work_dir: `+workDir+`
  postgres_binary: `+postgresPath+`
  postgres_port: 15435
  startup_timeout: 100ms
  shutdown_timeout: 5s
recovery:
  target: timestamp
  value: "2026-07-06T01:02:03Z"
  timeline: latest
probes:
  - type: pg_isready
    binary: `+pgIsReadyPath+`
    timeout: 5s
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
	if result.Cluster != "test-main" || !strings.Contains(stdout.String(), "Cluster      test-main") {
		t.Fatalf("expected configured cluster in report and summary: cluster=%q summary=%s", result.Cluster, stdout.String())
	}
	for _, name := range []string{"tool.pgbackrest", "tool.postgres", "tool.pg_isready"} {
		if !hasCheckNamed(result.Checks, name, model.CheckStatusPassed) {
			t.Fatalf("expected passed preflight check %q, got %#v", name, result.Checks)
		}
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
	if !hasCheckNamed(result.Checks, "pgbackrest-verify", model.CheckStatusPassed) {
		t.Fatalf("expected passed pgbackrest verify, got %#v", result.Checks)
	}
	if !hasCheck(result.Checks, model.ProbePGIsReady, model.CheckStatusPassed) {
		t.Fatalf("expected passed pg_isready check, got %#v", result.Checks)
	}
}

func TestRunCommandWritesPreflightFailureBeforeCatalogAccess(t *testing.T) {
	dir := t.TempDir()
	const secret = "binary-secret"
	postgresPath := filepath.Join(dir, "postgres")
	pgIsReadyPath := filepath.Join(dir, "pg_isready")
	configPath := filepath.Join(dir, "pgdrill.yaml")
	reportPath := filepath.Join(dir, "report.json")
	writeExecutable(t, postgresPath, `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "postgres (PostgreSQL) 16.4"
  exit 0
fi
exit 64
`)
	writeExecutable(t, pgIsReadyPath, `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "pg_isready (PostgreSQL) 16.4"
  exit 0
fi
exit 64
`)
	writeFile(t, configPath, `
provider:
  type: wal-g
  binary: /definitely/missing/`+secret+`
  redact_values:
    - `+secret+`
target:
  type: local
  work_dir: `+filepath.Join(dir, "restore")+`
  postgres_binary: `+postgresPath+`
probes:
  - type: pg_isready
    binary: `+pgIsReadyPath+`
report:
  format: json
  path: `+reportPath+`
`)

	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "-f", configPath}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	result, err := report.ReadJSONFile(reportPath)
	if err != nil {
		t.Fatalf("read preflight failure report: %v", err)
	}
	if result.Status != model.DrillStatusFailed || result.Failure == nil || result.Failure.Stage != model.DrillStagePreflight {
		t.Fatalf("unexpected preflight failure %#v", result)
	}
	if result.Backup.ID != "" {
		t.Fatalf("preflight must stop before backup selection, got %#v", result.Backup)
	}
	if !hasCheckNamed(result.Checks, "tool.wal-g", model.CheckStatusFailed) || !hasCheckNamed(result.Checks, "tool.postgres", model.CheckStatusPassed) {
		t.Fatalf("expected complete tool outcomes, got %#v", result.Checks)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("encode result: %v", err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("preflight report leaked redacted value:\n%s", encoded)
	}
}

func TestRunCommandRejectsNonEmptyWorkDirBeforeNativePreflight(t *testing.T) {
	dir := t.TempDir()
	workDir := filepath.Join(dir, "restore")
	if err := os.Mkdir(workDir, 0o700); err != nil {
		t.Fatalf("create existing workdir: %v", err)
	}
	importantPath := filepath.Join(workDir, "important.txt")
	writeFile(t, importantPath, "keep\n")
	invokedPath := filepath.Join(dir, "wal-g-invoked")
	walgPath := filepath.Join(dir, "wal-g")
	writeExecutable(t, walgPath, `#!/bin/sh
printf 'invoked\n' > "`+invokedPath+`"
exit 0
`)
	reportPath := filepath.Join(dir, "report.json")
	configPath := filepath.Join(dir, "pgdrill.yaml")
	writeFile(t, configPath, `
provider:
  type: wal-g
  binary: `+walgPath+`
target:
  type: local
  work_dir: `+workDir+`
  remove_work_dir: true
probes:
  - preset: readiness
report:
  format: json
  path: `+reportPath+`
`)

	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "-f", configPath}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	result, err := report.ReadJSONFile(reportPath)
	if err != nil {
		t.Fatalf("read target validation report: %v", err)
	}
	if result.Status != model.DrillStatusFailed || result.Failure == nil || result.Failure.Stage != model.DrillStageRequestValidation {
		t.Fatalf("unexpected target validation report %#v", result)
	}
	if !strings.Contains(result.Failure.Message, "work_dir must be empty") {
		t.Fatalf("unexpected target validation failure %#v", result.Failure)
	}
	if _, err := os.Stat(invokedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("native preflight ran before target validation, stat err=%v", err)
	}
	data, err := os.ReadFile(importantPath)
	if err != nil || string(data) != "keep\n" {
		t.Fatalf("existing workdir data changed: data=%q err=%v", data, err)
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

func TestRunCommandRequiresProbe(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "pgdrill.yaml")
	writeFile(t, configPath, `
provider:
  type: wal-g
target:
  type: local
  work_dir: `+filepath.Join(dir, "restore")+`
report:
  path: `+filepath.Join(dir, "report.json")+`
`)

	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "-f", configPath}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
	if got := stderr.String(); !strings.Contains(got, "at least one probe is required") {
		t.Fatalf("expected probe requirement, got: %s", got)
	}
}

func TestRunCommandWritesAbortedReportForCanceledContext(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "pgdrill.yaml")
	reportPath := filepath.Join(dir, "report.json")
	writeFile(t, configPath, `
provider:
  type: wal-g
target:
  type: local
  work_dir: `+filepath.Join(dir, "restore")+`
probes:
  - preset: readiness
report:
  format: json
  path: `+reportPath+`
`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var stdout, stderr bytes.Buffer
	code := runContext(ctx, []string{"run", "-f", configPath}, &stdout, &stderr)

	if code != exitCodeInterrupted {
		t.Fatalf("expected exit code %d, got %d: %s", exitCodeInterrupted, code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "run aborted") {
		t.Fatalf("expected aborted error, got %q", stderr.String())
	}
	result, err := report.ReadJSONFile(reportPath)
	if err != nil {
		t.Fatalf("read aborted report: %v", err)
	}
	if result.Status != model.DrillStatusAborted {
		t.Fatalf("expected aborted report, got %#v", result)
	}
	if result.Failure == nil || result.Failure.Stage != model.DrillStageRequestValidation {
		t.Fatalf("expected request validation failure, got %#v", result.Failure)
	}
}

func TestReportShowCommandText(t *testing.T) {
	reportPath := filepath.Join(t.TempDir(), "drill.json")
	writeDrillReport(t, reportPath, model.DrillResult{
		ID:             "drill-1",
		PGDrillVersion: "pgdrill v0.1.0-test",
		Cluster:        "production-main",
		Provider:       model.ProviderWALG,
		Backup: model.Backup{
			ID:         "wal-g:base_1",
			Provider:   model.ProviderWALG,
			ProviderID: "base_1",
			Kind:       model.BackupKindFull,
			Status:     model.BackupStatusAvailable,
		},
		Target:         model.TargetSpec{Type: model.RestoreTargetLocal, WorkDir: "/tmp/pgdrill/main"},
		RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetLatest},
		StartedAt:      mustTime(t, "2026-07-06T01:02:03Z"),
		FinishedAt:     mustTime(t, "2026-07-06T01:03:03Z"),
		Status:         model.DrillStatusFailed,
		Failure: &model.DrillFailure{
			Stage:   model.DrillStageProbeExecution,
			Message: "one or more probes failed",
		},
		Checks: []model.Check{
			{Name: "catalog", Status: model.CheckStatusPassed},
			{Name: "select_1", Probe: model.ProbeSQL, Status: model.CheckStatusFailed, Message: "query failed\nconnection closed"},
		},
		Evidence: []model.EvidenceRecord{{
			ID:          "evidence-1",
			Kind:        model.EvidenceCheck,
			Source:      "test",
			CollectedAt: mustTime(t, "2026-07-06T01:03:03Z"),
		}},
	})

	var stdout, stderr bytes.Buffer
	code := run([]string{"report", "show", reportPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d: %s", code, stderr.String())
	}

	output := stdout.String()
	for _, expected := range []string{
		model.CurrentReportSchemaVersion,
		"pgdrill      pgdrill v0.1.0-test",
		"ID           drill-1",
		"Attempt      attempt-1",
		"Spec digest  sha256:",
		"Cluster      production-main",
		"Status       failed",
		"Stage        probe_execution",
		"Error        one or more probes failed",
		"Backup       wal-g:base_1",
		"Checks       1 passed, 1 failed",
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
		ID:             "drill-json",
		Provider:       model.ProviderBarman,
		Target:         model.TargetSpec{Type: model.RestoreTargetLocal},
		RecoveryTarget: model.RecoveryTarget{Type: model.RecoveryTargetLatest},
		StartedAt:      mustTime(t, "2026-07-06T01:02:03Z"),
		FinishedAt:     mustTime(t, "2026-07-06T01:03:03Z"),
		Status:         model.DrillStatusPassed,
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
		Cluster:  "production-main",
		Provider: model.ProviderPGBackRest,
		Target: model.TargetSpec{
			Type: model.RestoreTargetLocal,
		},
		RecoveryTarget: model.RecoveryTarget{
			Type:  model.RecoveryTargetTimestamp,
			Value: "2026-07-06T01:05:03Z",
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
		`pgdrill_report_info{cluster="production-main",schema_version="pgdrill.report/v1alpha1"} 1`,
		"# TYPE pgdrill_drill_status gauge",
		`pgdrill_drill_status{cluster="production-main",provider="pgbackrest",target_type="local",recovery_target="timestamp",status="passed"} 1`,
		`pgdrill_drill_duration_seconds{cluster="production-main",provider="pgbackrest",target_type="local",recovery_target="timestamp",status="passed"} 120`,
		`pgdrill_checks_total{cluster="production-main",provider="pgbackrest",check="pgbackrest-check",probe="unknown",status="passed"} 1`,
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

func requireMap(t *testing.T, value any) map[string]any {
	t.Helper()
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %#v", value)
	}
	return result
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
	selection := model.BackupSelection{Type: model.BackupSelectionLatestAvailable}
	if result.Backup.ID != "" {
		selection = model.BackupSelection{Type: model.BackupSelectionByID, BackupID: result.Backup.ID}
	}
	targetID := result.Target.WorkDir
	if targetID == "" {
		targetID = "test-target"
	}
	spec, err := runspec.New(model.DrillSpec{
		Mode:    model.DrillModeNative,
		Cluster: result.Cluster,
		Source: model.BackupSourceSpec{
			Ref:      model.ComponentRef{ID: "test-source", Driver: string(result.Provider), Revision: "sha256:" + strings.Repeat("a", 64)},
			Provider: result.Provider,
		},
		BackupSelection: selection,
		Target: model.RestoreTargetSpec{
			Ref:  model.ComponentRef{ID: targetID, Driver: string(result.Target.Type), Revision: "sha256:" + strings.Repeat("b", 64)},
			Spec: result.Target,
		},
		RecoveryTarget: result.RecoveryTarget,
		ProbeProfile: model.ProbeProfileSpec{
			Ref:    model.ComponentRef{ID: "test-probes", Driver: "inline", Revision: "sha256:" + strings.Repeat("c", 64)},
			Probes: []model.ProbeDescriptor{{Type: model.ProbeSQL, Name: "select_1"}},
		},
	})
	if err != nil {
		t.Fatalf("create drill spec: %v", err)
	}
	document := spec.Document()
	result.AttemptID = "attempt-1"
	result.SpecDigest = spec.Digest()
	result.Spec = &document
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

func hasEvidenceOperation(records []model.EvidenceRecord, operation string) bool {
	for _, record := range records {
		if record.Attributes["operation"] == operation {
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
