#!/usr/bin/env bash

set -Eeuo pipefail
umask 022

readonly PGBIN="/usr/lib/postgresql/18/bin"
readonly PGDRILL="/opt/pgdrill/bin/pgdrill"
readonly WALG="/opt/pgdrill/bin/wal-g"
readonly CONFIG="/opt/pgdrill/test/pgdrill.yaml"
readonly PITR_CONFIG_TEMPLATE="/opt/pgdrill/test/pgdrill-pitr.yaml.tmpl"
readonly ROOT="/validation"
readonly PITR_CONFIG="${ROOT}/pgdrill-pitr.yaml"
readonly SOURCE_DATA="${ROOT}/source-data"
readonly SOURCE_SOCKET="${ROOT}/source-socket"
readonly SOURCE_LOG="${ROOT}/source.log"
readonly REPOSITORY="${ROOT}/repository"
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
export WALG_FILE_PREFIX="${REPOSITORY}"
export WALG_COMPRESSION_METHOD="lz4"

log() {
  printf '[integration/walg] %s\n' "$*"
}

die() {
  printf '[integration/walg] ERROR: %s\n' "$*" >&2
  exit 1
}

wait_for_archived_wal() {
  local expected_wal="$1"
  local observed_wal

  for _ in $(seq 1 60); do
    observed_wal="$(${PGBIN}/psql -Atqc "SELECT COALESCE(last_archived_wal, '') FROM pg_stat_archiver;")"
    if [[ "${observed_wal}" == "${expected_wal}" || "${observed_wal}" > "${expected_wal}" ]]; then
      printf '%s\n' "${observed_wal}"
      return 0
    fi
    sleep 1
  done
  return 1
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
[[ -r "${PITR_CONFIG_TEMPLATE}" ]] || die "pgdrill PITR config template is not readable"

mkdir -p \
  "${HOME}" \
  "${TMPDIR}" \
  "${SOURCE_DATA}" \
  "${SOURCE_SOCKET}" \
  "${REPOSITORY}" \
  "${ROOT}/work"
chmod 0700 "${HOME}" "${TMPDIR}" "${SOURCE_DATA}" "${SOURCE_SOCKET}" "${ROOT}/work"

pgdrill_version="$(${PGDRILL} version)"
expected_version_prefix="pgdrill ${EXPECTED_VERSION} (${EXPECTED_COMMIT}, "
[[ "${pgdrill_version}" == "${expected_version_prefix}"* ]] ||
  die "pgdrill version is not bound to expected version/commit ${EXPECTED_VERSION}/${EXPECTED_COMMIT}"
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

if ! last_archived_wal="$(wait_for_archived_wal "${sentinel_wal}")"; then
  die "post-backup WAL ${sentinel_wal} was not archived"
fi

latest_row_count="$(${PGBIN}/psql -Atqc 'SELECT count(*) FROM public.pgdrill_integration_probe;')"
[[ "${latest_row_count}" == "101" ]] ||
  die "source row count is ${latest_row_count}, expected 101 before latest recovery"
pitr_target_time="$(
  "${PGBIN}/psql" -Atqc \
    "SELECT to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"');" |
    sed -E 's/([0-9])0+Z$/\1Z/; s/\.0+Z$/Z/'
)"

log "capturing read-only preflight and catalog evidence"
"${PGDRILL}" doctor -f "${CONFIG}" -format json >/output/doctor.json
"${PGDRILL}" catalog list -f "${CONFIG}" -format json -evidence >/output/catalog.json

latest_run_id="integration-walg-latest-$(date -u +%Y%m%dT%H%M%SZ)"
log "running latest-recovery attempt ${latest_run_id}/attempt-1"
"${PGDRILL}" run \
  -f "${CONFIG}" \
  -run-id "${latest_run_id}" \
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

log "committing and archiving a transaction after the PITR boundary"
"${PGBIN}/psql" --set ON_ERROR_STOP=1 --command \
  "INSERT INTO public.pgdrill_integration_probe (id, payload) VALUES (102, 'post-target-wal-sentinel');"
post_target_wal="$(${PGBIN}/psql -Atqc 'SELECT pg_walfile_name(pg_current_wal_lsn());')"
"${PGBIN}/psql" -Atqc 'SELECT pg_switch_wal();' >/dev/null
if ! last_archived_post_target_wal="$(wait_for_archived_wal "${post_target_wal}")"; then
  die "post-target WAL ${post_target_wal} was not archived"
fi

source_row_count="$(${PGBIN}/psql -Atqc 'SELECT count(*) FROM public.pgdrill_integration_probe;')"
[[ "${source_row_count}" == "102" ]] ||
  die "source row count is ${source_row_count}, expected 102 before timestamp recovery"

sed "s|__RECOVERY_TARGET_TIME__|${pitr_target_time}|g" \
  "${PITR_CONFIG_TEMPLATE}" >"${PITR_CONFIG}"
if grep -F '__RECOVERY_TARGET_TIME__' "${PITR_CONFIG}" >/dev/null; then
  die "PITR recovery timestamp placeholder was not resolved"
fi
cp "${PITR_CONFIG}" /output/pitr-config.yaml
"${PGDRILL}" doctor -f "${PITR_CONFIG}" -format json >/output/pitr-doctor.json

pitr_run_id="integration-walg-pitr-$(date -u +%Y%m%dT%H%M%SZ)"
log "running timestamp-PITR attempt ${pitr_run_id}/attempt-1 to ${pitr_target_time}"
"${PGDRILL}" run \
  -f "${PITR_CONFIG}" \
  -run-id "${pitr_run_id}" \
  -attempt-id attempt-1 2>&1 | tee /output/pitr-run.log

[[ -f /output/pitr-report.json ]] || die "pgdrill did not persist pitr-report.json"
"${PGDRILL}" report show /output/pitr-report.json | tee /output/pitr-report.txt

grep -Eq '^Status[[:space:]]+passed$' /output/pitr-report.txt ||
  die "timestamp PITR report status is not passed"
grep -Eq '^Policy[[:space:]]+5 passed, 0 failed, 0 unknown, 0 not configured$' \
  /output/pitr-report.txt ||
  die "timestamp PITR policy did not produce five passed verdicts"
grep -Eq '^wal-g-wal-verify-integrity[[:space:]]+-[[:space:]]+passed' \
  /output/pitr-report.txt ||
  die "timestamp PITR WAL-G integrity check did not pass"
grep -Eq '^timestamp_boundary_replayed[[:space:]]+sql[[:space:]]+passed' \
  /output/pitr-report.txt ||
  die "timestamp PITR did not prove the before/after transaction boundary"
grep -Eq '^structural_amcheck[[:space:]]+amcheck[[:space:]]+passed' \
  /output/pitr-report.txt ||
  die "timestamp PITR pg_amcheck probe did not pass"
grep -Eq '^schema_dump[[:space:]]+pg_dump[[:space:]]+passed' /output/pitr-report.txt ||
  die "timestamp PITR pg_dump probe did not pass"
grep -Eq '^cleanup[[:space:]]+true[[:space:]]+passed' /output/pitr-report.txt ||
  die "timestamp PITR cleanup policy did not pass"
grep -F '"type": "timestamp"' /output/pitr-report.json >/dev/null ||
  die "timestamp PITR report does not retain the recovery target type"
grep -F "\"value\": \"${pitr_target_time}\"" /output/pitr-report.json >/dev/null ||
  die "timestamp PITR report does not retain the exact recovery target"
grep -F '"inclusive": true' /output/pitr-report.json >/dev/null ||
  die "timestamp PITR report does not retain inclusive recovery semantics"
[[ ! -e "${WORK_DIR}" ]] || die "owned PITR restore work directory remains after cleanup"

{
  printf 'pgdrill=%s\n' "${pgdrill_version}"
  printf 'wal_g=%s\n' "${walg_version}"
  printf 'postgresql=%s\n' "${postgres_version}"
  printf 'latest_recovery_source_rows=%s\n' "${latest_row_count}"
  printf 'timestamp_recovery_target=%s\n' "${pitr_target_time}"
  printf 'source_rows_after_target=%s\n' "${source_row_count}"
  printf 'sentinel_wal=%s\n' "${sentinel_wal}"
  printf 'last_archived_wal=%s\n' "${last_archived_wal}"
  printf 'post_target_wal=%s\n' "${post_target_wal}"
  printf 'last_archived_post_target_wal=%s\n' "${last_archived_post_target_wal}"
  "${WALG}" backup-list --detail --json
} >/output/source-state.txt

log "PASS: latest recovery and timestamp PITR boundaries, probes, policy, and cleanup completed"
