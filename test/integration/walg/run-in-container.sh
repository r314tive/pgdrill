#!/usr/bin/env bash

set -Eeuo pipefail
umask 022

readonly PGBIN="/usr/lib/postgresql/18/bin"
readonly PGDRILL="/opt/pgdrill/bin/pgdrill"
readonly WALG="/opt/pgdrill/bin/wal-g"
readonly CONFIG="/opt/pgdrill/test/pgdrill.yaml"
readonly ROOT="/validation"
readonly SOURCE_DATA="${ROOT}/source-data"
readonly SOURCE_SOCKET="${ROOT}/source-socket"
readonly SOURCE_LOG="${ROOT}/source.log"
readonly REPOSITORY="${ROOT}/repository"
readonly WORK_DIR="${ROOT}/work/restore"
readonly SOURCE_PORT="55431"
readonly EXPECTED_COMMIT="${PGDRILL_EXPECTED_COMMIT:?PGDRILL_EXPECTED_COMMIT is required}"

export HOME="${ROOT}/home"
export TMPDIR="${ROOT}/tmp"
export PATH="/opt/pgdrill/bin:${PGBIN}:${PATH}"
export PGHOST="${SOURCE_SOCKET}"
export PGPORT="${SOURCE_PORT}"
export PGDATABASE="postgres"
export WALG_FILE_PREFIX="${REPOSITORY}"
export WALG_COMPRESSION_METHOD="lz4"

log() {
  printf '[integration/walg] %s\n' "$*"
}

die() {
  printf '[integration/walg] ERROR: %s\n' "$*" >&2
  exit 1
}

source_running=false
cleanup() {
  status="$?"
  trap - EXIT
  set +e
  if [[ "${source_running}" == "true" ]]; then
    "${PGBIN}/pg_ctl" -D "${SOURCE_DATA}" -m fast -w -t 30 stop >/dev/null 2>&1
  fi
  if [[ -f "${SOURCE_LOG}" ]]; then
    cp "${SOURCE_LOG}" /output/source-postgres.log
  fi
  exit "${status}"
}
trap cleanup EXIT

[[ "$(id -u)" == "999" ]] || die "container must run as the postgres UID 999"
[[ -x "${PGDRILL}" ]] || die "pgdrill binary is not executable"
[[ -x "${WALG}" ]] || die "WAL-G binary is not executable"
[[ -r "${CONFIG}" ]] || die "pgdrill config is not readable"

mkdir -p \
  "${HOME}" \
  "${TMPDIR}" \
  "${SOURCE_DATA}" \
  "${SOURCE_SOCKET}" \
  "${REPOSITORY}" \
  "${ROOT}/work"
chmod 0700 "${HOME}" "${TMPDIR}" "${SOURCE_DATA}" "${SOURCE_SOCKET}" "${ROOT}/work"

pgdrill_version="$(${PGDRILL} version)"
[[ "${pgdrill_version}" == *"${EXPECTED_COMMIT}"* ]] ||
  die "pgdrill version is not bound to expected commit ${EXPECTED_COMMIT}"
walg_version="$(${WALG} --version | tr '\n' ' ' | sed 's/[[:space:]]*$//')"
[[ "${walg_version}" == *"v3.0.8"* ]] || die "unexpected WAL-G version: ${walg_version}"
postgres_version="$(${PGBIN}/postgres --version)"
[[ "${postgres_version}" == *" 18.3 "* || "${postgres_version}" == *" 18.3" ]] ||
  die "unexpected PostgreSQL version: ${postgres_version}"

log "initializing checksummed PostgreSQL source"
"${PGBIN}/initdb" \
  --pgdata "${SOURCE_DATA}" \
  --auth-local trust \
  --auth-host trust \
  --encoding UTF8 \
  --locale C.UTF-8 \
  --data-checksums >/output/initdb.log

cat >>"${SOURCE_DATA}/postgresql.conf" <<'EOF'
listen_addresses = '127.0.0.1'
port = 55431
unix_socket_directories = '/validation/source-socket'
archive_mode = on
archive_command = 'env WALG_FILE_PREFIX=/validation/repository WALG_COMPRESSION_METHOD=lz4 /opt/pgdrill/bin/wal-g wal-push "%p"'
archive_timeout = '10s'
wal_level = replica
shared_buffers = '32MB'
log_min_messages = info
EOF

"${PGBIN}/pg_ctl" -D "${SOURCE_DATA}" -l "${SOURCE_LOG}" -w -t 30 start
source_running=true

log "creating the 100-row base-backup boundary"
"${PGBIN}/psql" --set ON_ERROR_STOP=1 <<'SQL'
CREATE EXTENSION amcheck;
CREATE TABLE public.pgdrill_integration_probe (
  id integer PRIMARY KEY,
  payload text NOT NULL,
  committed_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
INSERT INTO public.pgdrill_integration_probe (id, payload)
SELECT id, 'base-backup-row-' || id
FROM generate_series(1, 100) AS id;
CHECKPOINT;
SQL

log "taking a real WAL-G full backup"
"${WALG}" backup-push "${SOURCE_DATA}" 2>&1 | tee /output/backup-push.log

log "committing and archiving the post-backup WAL sentinel"
"${PGBIN}/psql" --set ON_ERROR_STOP=1 --command \
  "INSERT INTO public.pgdrill_integration_probe (id, payload) VALUES (101, 'post-backup-wal-sentinel');"
sentinel_wal="$(${PGBIN}/psql -Atqc 'SELECT pg_walfile_name(pg_current_wal_lsn());')"
"${PGBIN}/psql" -Atqc 'SELECT pg_switch_wal();' >/dev/null

archived=false
for _ in $(seq 1 60); do
  last_archived_wal="$(${PGBIN}/psql -Atqc "SELECT COALESCE(last_archived_wal, '') FROM pg_stat_archiver;")"
  if [[ "${last_archived_wal}" == "${sentinel_wal}" || "${last_archived_wal}" > "${sentinel_wal}" ]]; then
    archived=true
    break
  fi
  sleep 1
done
[[ "${archived}" == "true" ]] || die "post-backup WAL ${sentinel_wal} was not archived"

row_count="$(${PGBIN}/psql -Atqc 'SELECT count(*) FROM public.pgdrill_integration_probe;')"
[[ "${row_count}" == "101" ]] || die "source row count is ${row_count}, expected 101"

log "capturing read-only preflight and catalog evidence"
"${PGDRILL}" doctor -f "${CONFIG}" -format json >/output/doctor.json
"${PGDRILL}" catalog list -f "${CONFIG}" -format json -evidence >/output/catalog.json

run_id="integration-walg-$(date -u +%Y%m%dT%H%M%SZ)"
log "running pgdrill restore attempt ${run_id}/attempt-1"
"${PGDRILL}" run \
  -f "${CONFIG}" \
  -run-id "${run_id}" \
  -attempt-id attempt-1 2>&1 | tee /output/run.log

[[ -f /output/report.json ]] || die "pgdrill did not persist report.json"
"${PGDRILL}" report show /output/report.json | tee /output/report.txt

grep -Eq '^Status[[:space:]]+passed$' /output/report.txt || die "report status is not passed"
grep -Eq '^Policy[[:space:]]+5 passed, 0 failed, 0 unknown, 0 not configured$' /output/report.txt ||
  die "recovery policy did not produce five passed verdicts"
grep -Eq '^wal-g-wal-verify-integrity[[:space:]]+-[[:space:]]+passed' /output/report.txt ||
  die "WAL-G integrity check did not pass"
grep -Eq '^post_backup_wal_replayed[[:space:]]+sql[[:space:]]+passed' /output/report.txt ||
  die "post-backup WAL sentinel probe did not pass"
grep -Eq '^structural_amcheck[[:space:]]+amcheck[[:space:]]+passed' /output/report.txt ||
  die "pg_amcheck probe did not pass"
grep -Eq '^schema_dump[[:space:]]+pg_dump[[:space:]]+passed' /output/report.txt ||
  die "pg_dump probe did not pass"
grep -Eq '^cleanup[[:space:]]+true[[:space:]]+passed' /output/report.txt ||
  die "cleanup policy did not pass"
grep -F '"archive_mode": "off"' /output/report.json >/dev/null ||
  die "report does not retain the local-target archive_mode override"
[[ ! -e "${WORK_DIR}" ]] || die "owned restore work directory remains after cleanup"

{
  printf 'pgdrill=%s\n' "${pgdrill_version}"
  printf 'wal_g=%s\n' "${walg_version}"
  printf 'postgresql=%s\n' "${postgres_version}"
  printf 'source_rows=%s\n' "${row_count}"
  printf 'sentinel_wal=%s\n' "${sentinel_wal}"
  printf 'last_archived_wal=%s\n' "${last_archived_wal}"
  "${WALG}" backup-list --detail --json
} >/output/source-state.txt

log "PASS: real backup, WAL replay, probes, policy, and cleanup completed"
