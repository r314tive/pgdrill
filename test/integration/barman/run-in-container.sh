#!/usr/bin/env bash

set -Eeuo pipefail
umask 022

readonly PGBIN="/usr/lib/postgresql/18/bin"
readonly PGDRILL="/opt/pgdrill/bin/pgdrill"
readonly BARMAN="/usr/bin/barman"
readonly BARMAN_CONFIG="/opt/pgdrill/test/barman.conf"
readonly CONFIG="/opt/pgdrill/test/pgdrill.yaml"
readonly ROOT="/validation"
readonly SOURCE_DATA="${ROOT}/source-data"
readonly SOURCE_SOCKET="${ROOT}/source-socket"
readonly SOURCE_LOG="${ROOT}/source.log"
readonly BARMAN_HOME="${ROOT}/barman"
readonly WORK_DIR="${ROOT}/work/restore"
readonly SOURCE_PORT="55431"
readonly EXPECTED_COMMIT="${PGDRILL_EXPECTED_COMMIT:?PGDRILL_EXPECTED_COMMIT is required}"
readonly EXPECTED_VERSION="${PGDRILL_EXPECTED_VERSION:?PGDRILL_EXPECTED_VERSION is required}"

export HOME="${ROOT}/home"
export TMPDIR="${ROOT}/tmp"
export PATH="/opt/pgdrill/bin:${PGBIN}:${PATH}"
export PGHOST="${SOURCE_SOCKET}"
export PGPORT="${SOURCE_PORT}"
export PGDATABASE="postgres"
export PYTHONDONTWRITEBYTECODE=1

log() {
  printf '[integration/barman] %s\n' "$*"
}

die() {
  printf '[integration/barman] ERROR: %s\n' "$*" >&2
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
  if [[ -f "${BARMAN_HOME}/barman.log" ]]; then
    cp "${BARMAN_HOME}/barman.log" /output/barman.log
  fi
  exit "${status}"
}
trap cleanup EXIT

[[ "$(id -u)" == "999" ]] || die "container must run as the postgres UID 999"
[[ -x "${PGDRILL}" ]] || die "pgdrill binary is not executable"
[[ -x "${BARMAN}" ]] || die "Barman binary is not executable"
[[ -r "${BARMAN_CONFIG}" ]] || die "Barman config is not readable"
[[ -r "${CONFIG}" ]] || die "pgdrill config is not readable"

mkdir -p \
  "${HOME}" \
  "${TMPDIR}" \
  "${SOURCE_DATA}" \
  "${SOURCE_SOCKET}" \
  "${BARMAN_HOME}/integration/incoming" \
  "${BARMAN_HOME}/locks" \
  "${ROOT}/work"
chmod 0700 "${HOME}" "${TMPDIR}" "${SOURCE_DATA}" "${SOURCE_SOCKET}" "${ROOT}/work"

pgdrill_version="$(${PGDRILL} version)"
expected_version_prefix="pgdrill ${EXPECTED_VERSION} (${EXPECTED_COMMIT}, "
[[ "${pgdrill_version}" == "${expected_version_prefix}"* ]] ||
  die "pgdrill version is not bound to expected version/commit ${EXPECTED_VERSION}/${EXPECTED_COMMIT}"
barman_version="$(${BARMAN} --version | tr '\n' ' ' | sed 's/[[:space:]]*$//')"
[[ "${barman_version}" == "3.19.1 Barman by EnterpriseDB"* ]] ||
  die "unexpected Barman version: ${barman_version}"
postgres_version="$(${PGBIN}/postgres --version)"
[[ "${postgres_version}" == *" 18.3 "* || "${postgres_version}" == *" 18.3" ]] ||
  die "unexpected PostgreSQL version: ${postgres_version}"

dpkg-query -W \
  '-f=${binary:Package}=${Version}\n' \
  barman \
  python3-barman \
  rsync \
  postgresql-18 \
  postgresql-client-18 > /output/packages.txt

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
archive_command = 'test ! -f /validation/barman/integration/incoming/%f && cp "%p" /validation/barman/integration/incoming/%f'
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

log "taking a real Barman local-rsync full backup"
"${BARMAN}" --config "${BARMAN_CONFIG}" backup integration 2>&1 | tee /output/backup.log
"${BARMAN}" --config "${BARMAN_CONFIG}" archive-wal integration 2>&1 | tee /output/archive-base-backup-wal.log

backup_list_json="$(${BARMAN} --config "${BARMAN_CONFIG}" --format json list-backups integration)"
backup_id="$(python3 -c 'import json, sys; print(json.load(sys.stdin)["integration"][0]["backup_id"])' <<<"${backup_list_json}")"
[[ -n "${backup_id}" ]] || die "Barman did not return a backup ID"

backup_ready=false
for _ in $(seq 1 60); do
  "${BARMAN}" --config "${BARMAN_CONFIG}" archive-wal integration >/dev/null 2>&1 || true
  if "${BARMAN}" --config "${BARMAN_CONFIG}" check-backup integration "${backup_id}" >/dev/null 2>&1; then
    backup_ready=true
    break
  fi
  sleep 1
done
[[ "${backup_ready}" == "true" ]] || die "Barman backup ${backup_id} did not become valid"

log "committing and archiving the post-backup WAL sentinel"
"${PGBIN}/psql" --set ON_ERROR_STOP=1 --command \
  "INSERT INTO public.pgdrill_integration_probe (id, payload) VALUES (101, 'post-backup-wal-sentinel');"
sentinel_wal="$(${PGBIN}/psql -Atqc 'SELECT pg_walfile_name(pg_current_wal_lsn());')"
"${PGBIN}/psql" -Atqc 'SELECT pg_switch_wal();' >/dev/null

archived=false
for _ in $(seq 1 60); do
  "${BARMAN}" --config "${BARMAN_CONFIG}" archive-wal integration >/dev/null 2>&1 || true
  if "${BARMAN}" --config "${BARMAN_CONFIG}" get-wal integration "${sentinel_wal}" >/dev/null 2>&1; then
    archived=true
    break
  fi
  sleep 1
done
[[ "${archived}" == "true" ]] || die "post-backup WAL ${sentinel_wal} was not archived by Barman"

row_count="$(${PGBIN}/psql -Atqc 'SELECT count(*) FROM public.pgdrill_integration_probe;')"
[[ "${row_count}" == "101" ]] || die "source row count is ${row_count}, expected 101"

log "capturing read-only preflight and catalog evidence"
"${PGDRILL}" doctor -f "${CONFIG}" -format json >/output/doctor.json
"${PGDRILL}" catalog list -f "${CONFIG}" -format json -evidence >/output/catalog.json

run_id="integration-barman-$(date -u +%Y%m%dT%H%M%SZ)"
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
for check in \
  barman-check \
  barman-check-backup \
  barman-show-backup \
  barman-generate-manifest \
  barman-verify-backup; do
  grep -Eq "^${check}[[:space:]]+-[[:space:]]+passed" /output/report.txt ||
    die "${check} did not pass"
done
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
  printf 'barman=%s\n' "${barman_version}"
  printf 'postgresql=%s\n' "${postgres_version}"
  printf 'source_rows=%s\n' "${row_count}"
  printf 'backup_id=%s\n' "${backup_id}"
  printf 'sentinel_wal=%s\n' "${sentinel_wal}"
  "${BARMAN}" --config "${BARMAN_CONFIG}" --format json list-backups integration
} >/output/source-state.txt

log "PASS: real backup, WAL replay, provider checks, probes, policy, and cleanup completed"
