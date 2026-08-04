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
  readonly PGBACKREST_REPOSITORY="${REPOSITORY_MOUNT}/pgbackrest/repository"
  log() { printf '[pgdrill-demo/pgbackrest] %s\n' "$*"; }
  die() { printf '[pgdrill-demo/pgbackrest] ERROR: %s\n' "$*" >&2; exit 1; }
  mount_repository() {
    mountpoint --quiet "${REPOSITORY_MOUNT}" || mount "${REPOSITORY_MOUNT}"
    runuser -u postgres -- ls -ld "${REPOSITORY_MOUNT}/." >/dev/null
    [[ "$(findmnt --noheadings --raw --types nfs,nfs4 --output FSTYPE --target "${REPOSITORY_MOUNT}")" == nfs* ]] ||
      die "repository path is not backed by NFS"
    local options
    options="$(findmnt --noheadings --raw --types nfs,nfs4 --output OPTIONS --target "${REPOSITORY_MOUNT}")"
    [[ ",${options}," == *",rw,"* ]] || die "repository is not mounted read-write"
  }
fi

readonly PGBACKREST="/usr/bin/pgbackrest"
readonly PGBACKREST_CONFIG="/etc/pgdrill/pgbackrest-source.conf"
readonly STANZA="demo"
readonly SOURCE_PORT="5433"
readonly SOURCE_STATE="/var/lib/pgdrill-demo/pgbackrest-source-state.json"
readonly PGBACKREST_STATE_ROOT="/var/lib/pgdrill-demo/pgbackrest"

usage() {
  cat <<'EOF'
Usage: sudo pgdrill-demo-pgbackrest-prepare-backup --reset

Resets only the marker-guarded disposable pgBackRest repository, creates a
full backup with 100 rows, commits row 101 afterwards, archives and retrieves
its WAL segment, and writes a secret-free source-state.json.
EOF
}

run_pgbackrest() {
  runuser -u postgres -- "${PGBACKREST}" \
    --config="${PGBACKREST_CONFIG}" \
    --stanza="${STANZA}" "$@"
}

wait_for_source() {
  for _ in {1..60}; do
    if runuser -u postgres -- "${PGBIN}/pg_isready" \
      --quiet --host /var/run/postgresql --port "${SOURCE_PORT}" --dbname postgres; then
      return 0
    fi
    sleep 1
  done
  die "pgBackRest demo source PostgreSQL did not become ready"
}

if [[ "${1:-}" == "--help" || "${1:-}" == "-h" ]]; then
  usage
  exit 0
fi
[[ "${EUID}" -eq 0 ]] || die "this command must run as root"
[[ "$#" -eq 1 && "$1" == "--reset" ]] || die "explicit --reset is required"
cd /

exec 9>/run/lock/pgdrill-demo-pgbackrest-source.lock
flock --nonblock 9 || die "another pgBackRest source preparation is active"

mount_repository rw
[[ "$(runuser -u postgres -- cat "${REPOSITORY_MOUNT}/.pgdrill-demo-repository" 2>/dev/null || true)" == "pgdrill-demo-repository/v1" ]] ||
  die "repository ownership marker is absent or invalid"
[[ -x "${PGBACKREST}" ]] || die "pgBackRest is not installed"
[[ -r "${PGBACKREST_CONFIG}" ]] || die "pgBackRest source config is not readable"

restart_source=false
sentinel_copy=""
cleanup() {
  if [[ -n "${sentinel_copy}" ]]; then
    rm -f -- "${sentinel_copy}"
  fi
  if [[ "${restart_source}" == "true" ]]; then
    systemctl start pgdrill-demo-pgbackrest-source.service || true
  fi
}
trap cleanup EXIT

rm -f -- "${SOURCE_STATE}" "${SOURCE_STATE}.tmp"

log "stopping the isolated source before resetting its repository"
systemctl stop pgdrill-demo-pgbackrest-source.service
restart_source=true
runuser -u postgres -- find "${PGBACKREST_REPOSITORY}" \
  -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +
for directory in spool lock log; do
  runuser -u postgres -- find "${PGBACKREST_STATE_ROOT}/${directory}" \
    -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +
done
systemctl start pgdrill-demo-pgbackrest-source.service
restart_source=false
wait_for_source

log "creating a fresh pgBackRest stanza"
run_pgbackrest stanza-create

log "creating the 100-row base-backup state"
runuser -u postgres -- "${PGBIN}/psql" \
  --host /var/run/postgresql --port "${SOURCE_PORT}" --dbname postgres \
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

log "checking archive configuration before backup"
run_pgbackrest check

log "creating a real pgBackRest full backup"
run_pgbackrest backup --type=full
backup_info="$(run_pgbackrest info --output=json)"
backup_label="$(
  jq -er '
    .[0].backup
    | sort_by(.timestamp.start // .timestamp.stop)
    | last
    | .label
  ' <<<"${backup_info}"
)"
backup_json="$(jq -c --arg label "${backup_label}" '.[0].backup[] | select(.label == $label)' <<<"${backup_info}")"
[[ -n "${backup_label}" && -n "${backup_json}" ]] ||
  die "pgBackRest did not return the selected full backup"

log "committing the post-backup WAL sentinel"
runuser -u postgres -- "${PGBIN}/psql" \
  --host /var/run/postgresql --port "${SOURCE_PORT}" --dbname postgres \
  --set ON_ERROR_STOP=1 \
  --command "INSERT INTO public.pgdrill_demo_probe (id, payload) VALUES (101, 'post-backup-wal-sentinel');"

sentinel_wal="$(runuser -u postgres -- "${PGBIN}/psql" \
  --host /var/run/postgresql --port "${SOURCE_PORT}" --dbname postgres \
  --tuples-only --no-align \
  --command "SELECT pg_walfile_name(pg_current_wal_lsn());")"
[[ "${sentinel_wal}" =~ ^[0-9A-F]{24}$ ]] ||
  die "PostgreSQL returned an invalid WAL file name"
runuser -u postgres -- "${PGBIN}/psql" \
  --host /var/run/postgresql --port "${SOURCE_PORT}" --dbname postgres \
  --tuples-only --no-align \
  --command "SELECT pg_switch_wal();" >/dev/null

archived=false
for _ in $(seq 1 120); do
  last_archived_wal="$(runuser -u postgres -- "${PGBIN}/psql" \
    --host /var/run/postgresql --port "${SOURCE_PORT}" --dbname postgres \
    --tuples-only --no-align \
    --command "SELECT COALESCE(last_archived_wal, '') FROM pg_stat_archiver;")"
  if [[ "${last_archived_wal}" =~ ^[0-9A-F]{24}$ ]] &&
    [[ "${last_archived_wal}" == "${sentinel_wal}" ||
      "${last_archived_wal}" > "${sentinel_wal}" ]]; then
    archived=true
    break
  fi
  sleep 1
done
[[ "${archived}" == "true" ]] ||
  die "post-backup WAL ${sentinel_wal} was not archived within 120 seconds"

sentinel_copy="$(mktemp /var/lib/pgdrill-demo/pgbackrest/sentinel-wal.XXXXXX)"
[[ -n "${sentinel_copy}" ]] || die "could not allocate the WAL retrieval path"
chown postgres:postgres "${sentinel_copy}"
rm -f "${sentinel_copy}"
run_pgbackrest archive-get "${sentinel_wal}" "${sentinel_copy}"
[[ -s "${sentinel_copy}" ]] ||
  die "post-backup WAL ${sentinel_wal} was not retrievable from pgBackRest"
rm -f "${sentinel_copy}"
sentinel_copy=""

row_count="$(runuser -u postgres -- "${PGBIN}/psql" \
  --host /var/run/postgresql --port "${SOURCE_PORT}" --dbname postgres \
  --tuples-only --no-align \
  --command "SELECT count(*) FROM public.pgdrill_demo_probe;")"
[[ "${row_count}" == "101" ]] || die "source row count is ${row_count}, expected 101"

log "checking the final archive state and verifying the selected backup"
run_pgbackrest check
run_pgbackrest verify --set="${backup_label}" --output=text

created_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
postgresql_version="$("${PGBIN}/postgres" --version)"
pgbackrest_version="$("${PGBACKREST}" version)"
backup_name="${STANZA}/${backup_label}"

jq -n \
  --arg schema_version "pgdrill.demo-source-state/v1alpha1" \
  --arg created_at "${created_at}" \
  --arg backup_name "${backup_name}" \
  --arg backup_label "${backup_label}" \
  --arg sentinel_wal "${sentinel_wal}" \
  --arg postgresql_version "${postgresql_version}" \
  --arg pgbackrest_version "${pgbackrest_version}" \
  --argjson backup "${backup_json}" \
  '{
    schema_version: $schema_version,
    created_at: $created_at,
    provider: "pgbackrest",
    backup_name: $backup_name,
    backup_label: $backup_label,
    backup: $backup,
    base_backup_row_count: 100,
    expected_recovered_row_count: 101,
    post_backup_wal_sentinel: "post-backup-wal-sentinel",
    sentinel_wal: $sentinel_wal,
    native_validation: {
      pgbackrest_check_before_backup: "passed",
      pgbackrest_check_after_backup_wal: "passed",
      pgbackrest_verify_selected_backup: "passed"
    },
    postgresql_version: $postgresql_version,
    pgbackrest_version: $pgbackrest_version
  }' >"${SOURCE_STATE}.tmp"
chown root:pgdrill-demo-admins "${SOURCE_STATE}.tmp"
chmod 0640 "${SOURCE_STATE}.tmp"
mv "${SOURCE_STATE}.tmp" "${SOURCE_STATE}"

log "backup preparation complete"
jq . "${SOURCE_STATE}"
