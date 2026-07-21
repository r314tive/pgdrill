#!/usr/bin/env bash

set -Eeuo pipefail
umask 027

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR
if [[ -r "${SCRIPT_DIR}/lib.sh" ]]; then
  # Development invocation from the staged bootstrap directory.
  # shellcheck source=demo/yandex-cloud/scripts/remote/lib.sh
  source "${SCRIPT_DIR}/lib.sh"
else
  readonly PG_MAJOR="18"
  readonly PGBIN="/usr/lib/postgresql/${PG_MAJOR}/bin"
  readonly REPOSITORY_MOUNT="/mnt/pgdrill-repository"
  readonly WALG_REPOSITORY="${REPOSITORY_MOUNT}/walg"
  log() { printf '[pgdrill-demo] %s\n' "$*"; }
  die() { printf '[pgdrill-demo] ERROR: %s\n' "$*" >&2; exit 1; }
  mount_repository() {
    mountpoint --quiet "${REPOSITORY_MOUNT}" || mount "${REPOSITORY_MOUNT}"
    runuser -u postgres -- ls -ld "${REPOSITORY_MOUNT}/." >/dev/null
    [[ "$(findmnt --noheadings --raw --output FSTYPE --target "${REPOSITORY_MOUNT}")" == nfs* ]] ||
      die "repository path is not backed by NFS"
    local options
    options="$(findmnt --noheadings --raw --output OPTIONS --target "${REPOSITORY_MOUNT}")"
    [[ ",${options}," == *",rw,"* ]] || die "repository is not mounted read-write"
  }
  wait_for_postgres() {
    for _ in {1..60}; do
      runuser -u postgres -- "${PGBIN}/pg_isready" -q -h /var/run/postgresql -p 5432 && return 0
      sleep 1
    done
    die "demo source PostgreSQL did not become ready"
  }
fi

usage() {
  cat <<'EOF'
Usage: sudo pgdrill-demo-prepare-backup --reset

Resets only the marker-guarded disposable WAL-G repository, creates a full
backup with 100 rows, commits row 101 afterwards, archives its WAL segment,
and writes a secret-free source-state.json for the drill runner.
EOF
}

if [[ "${1:-}" == "--help" || "${1:-}" == "-h" ]]; then
  usage
  exit 0
fi
[[ "${EUID}" -eq 0 ]] || die "this command must run as root"
[[ "$#" -eq 1 && "$1" == "--reset" ]] || die "explicit --reset is required"

exec 9>/run/lock/pgdrill-demo-source.lock
flock --nonblock 9 || die "another source preparation is active"

mount_repository rw
[[ "$(runuser -u postgres -- cat "${REPOSITORY_MOUNT}/.pgdrill-demo-repository" 2>/dev/null || true)" == "pgdrill-demo-repository/v1" ]] ||
  die "repository ownership marker is absent or invalid"

restart_source=false
restore_source_service() {
  if [[ "${restart_source}" == "true" ]]; then
    systemctl start pgdrill-demo-source.service || true
  fi
}
trap restore_source_service EXIT

log "stopping the disposable source before resetting its repository"
systemctl stop pgdrill-demo-source.service
restart_source=true
runuser -u postgres -- find "${WALG_REPOSITORY}" \
  -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +
runuser -u postgres -- rm -f \
  "${REPOSITORY_MOUNT}/source-state.json" \
  "${REPOSITORY_MOUNT}/source-state.json.tmp" \
  "${REPOSITORY_MOUNT}/wal-verify.json" \
  "${REPOSITORY_MOUNT}/wal-verify.json.tmp"
systemctl start pgdrill-demo-source.service
restart_source=false
wait_for_postgres

log "creating the 100-row base-backup state"
runuser -u postgres -- "${PGBIN}/psql" \
  --host /var/run/postgresql --port 5432 --dbname postgres \
  --set ON_ERROR_STOP=1 <<'SQL'
DROP TABLE IF EXISTS public.pgdrill_demo_probe;
CREATE EXTENSION IF NOT EXISTS amcheck;
CREATE TABLE public.pgdrill_demo_probe (
  id integer PRIMARY KEY,
  payload text NOT NULL,
  committed_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
INSERT INTO public.pgdrill_demo_probe (id, payload)
SELECT id, 'base-backup-row-' || id
FROM generate_series(1, 100) AS id;
CHECKPOINT;
SQL

log "creating a real WAL-G full backup"
runuser -u postgres -- env \
  WALG_FILE_PREFIX="${WALG_REPOSITORY}" \
  WALG_COMPRESSION_METHOD=lz4 \
  PGHOST=/var/run/postgresql \
  PGPORT=5432 \
  /usr/local/bin/wal-g backup-push /var/lib/pgdrill-demo/source-data

backup_list="$(runuser -u postgres -- env \
  WALG_FILE_PREFIX="${WALG_REPOSITORY}" \
  WALG_COMPRESSION_METHOD=lz4 \
  /usr/local/bin/wal-g backup-list --detail --json)"
backup_name="$(jq -er 'sort_by(.start_time // .last_modified // .time) | last | (.name // .backup_name)' <<<"${backup_list}")"

log "committing the post-backup WAL sentinel"
runuser -u postgres -- "${PGBIN}/psql" \
  --host /var/run/postgresql --port 5432 --dbname postgres \
  --set ON_ERROR_STOP=1 \
  --command "INSERT INTO public.pgdrill_demo_probe (id, payload) VALUES (101, 'post-backup-wal-sentinel');"

sentinel_wal="$(runuser -u postgres -- "${PGBIN}/psql" \
  --host /var/run/postgresql --port 5432 --dbname postgres \
  --tuples-only --no-align \
  --command "SELECT pg_walfile_name(pg_current_wal_lsn());")"
runuser -u postgres -- "${PGBIN}/psql" \
  --host /var/run/postgresql --port 5432 --dbname postgres \
  --tuples-only --no-align \
  --command "SELECT pg_switch_wal();" >/dev/null

archived=false
for _ in $(seq 1 120); do
  last_archived="$(runuser -u postgres -- "${PGBIN}/psql" \
    --host /var/run/postgresql --port 5432 --dbname postgres \
    --tuples-only --no-align \
    --command "SELECT COALESCE(last_archived_wal, '') FROM pg_stat_archiver;")"
  if [[ "${last_archived}" == "${sentinel_wal}" || "${last_archived}" > "${sentinel_wal}" ]]; then
    archived=true
    break
  fi
  sleep 1
done
[[ "${archived}" == "true" ]] || die "post-backup WAL ${sentinel_wal} was not archived"

row_count="$(runuser -u postgres -- "${PGBIN}/psql" \
  --host /var/run/postgresql --port 5432 --dbname postgres \
  --tuples-only --no-align \
  --command "SELECT count(*) FROM public.pgdrill_demo_probe;")"
[[ "${row_count}" == "101" ]] || die "source row count is ${row_count}, expected 101"

log "verifying WAL continuity before handing the repository to pgdrill"
wal_verify="$(runuser -u postgres -- env \
  WALG_FILE_PREFIX="${WALG_REPOSITORY}" \
  WALG_COMPRESSION_METHOD=lz4 \
  /usr/local/bin/wal-g wal-verify --json --backup-name "${backup_name}" integrity)"
jq -e '.integrity.status == "OK"' <<<"${wal_verify}" >/dev/null ||
  die "WAL-G integrity verification did not report OK"
printf '%s\n' "${wal_verify}" |
  runuser -u postgres -- tee "${REPOSITORY_MOUNT}/wal-verify.json.tmp" >/dev/null
runuser -u postgres -- chmod 0640 "${REPOSITORY_MOUNT}/wal-verify.json.tmp"
runuser -u postgres -- mv \
  "${REPOSITORY_MOUNT}/wal-verify.json.tmp" \
  "${REPOSITORY_MOUNT}/wal-verify.json"

created_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
postgresql_version="$("${PGBIN}/postgres" --version)"
walg_version="$(/usr/local/bin/wal-g --version | tr '\n' ' ' | sed 's/[[:space:]]*$//')"
backup_json="$(jq -c --arg name "${backup_name}" '.[] | select((.name // .backup_name) == $name)' <<<"${backup_list}")"

jq -n \
  --arg schema_version "pgdrill.demo-source-state/v1alpha1" \
  --arg created_at "${created_at}" \
  --arg backup_name "${backup_name}" \
  --arg sentinel_wal "${sentinel_wal}" \
  --arg postgresql_version "${postgresql_version}" \
  --arg walg_version "${walg_version}" \
  --argjson backup "${backup_json}" \
  '{
    schema_version: $schema_version,
    created_at: $created_at,
    provider: "wal-g",
    backup_name: $backup_name,
    backup: $backup,
    base_backup_row_count: 100,
    expected_recovered_row_count: 101,
    post_backup_wal_sentinel: "post-backup-wal-sentinel",
    sentinel_wal: $sentinel_wal,
    postgresql_version: $postgresql_version,
    walg_version: $walg_version
  }' | runuser -u postgres -- tee "${REPOSITORY_MOUNT}/source-state.json.tmp" >/dev/null
runuser -u postgres -- chmod 0640 "${REPOSITORY_MOUNT}/source-state.json.tmp"
runuser -u postgres -- mv \
  "${REPOSITORY_MOUNT}/source-state.json.tmp" \
  "${REPOSITORY_MOUNT}/source-state.json"
runuser -u postgres -- cat "${REPOSITORY_MOUNT}/source-state.json" \
  >/var/lib/pgdrill-demo/source-state.json
chown root:pgdrill-demo-admins /var/lib/pgdrill-demo/source-state.json
chmod 0640 /var/lib/pgdrill-demo/source-state.json

log "backup preparation complete"
runuser -u postgres -- jq . "${REPOSITORY_MOUNT}/source-state.json"
