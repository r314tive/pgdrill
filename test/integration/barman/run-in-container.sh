#!/usr/bin/env bash

set -Eeuo pipefail
umask 022

# shellcheck source=test/integration/lib/history.sh
source /opt/pgdrill/test/history.sh

readonly PGBIN="/usr/lib/postgresql/18/bin"
readonly PGDRILL="/opt/pgdrill/bin/pgdrill"
readonly BARMAN="/usr/bin/barman"
readonly BARMAN_CONFIG="/opt/pgdrill/test/barman.conf"
readonly CONFIG="/opt/pgdrill/test/pgdrill.yaml"
readonly PITR_CONFIG_TEMPLATE="/opt/pgdrill/test/pgdrill-pitr.yaml.tmpl"
readonly ROOT="/validation"
readonly PITR_CONFIG="${ROOT}/pgdrill-pitr.yaml"
readonly SOURCE_DATA="${ROOT}/source-data"
readonly SOURCE_SOCKET="${ROOT}/source-socket"
readonly SOURCE_LOG="${ROOT}/source.log"
readonly BARMAN_HOME="${ROOT}/barman"
readonly HISTORY="${ROOT}/history"
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
[[ -r "${PITR_CONFIG_TEMPLATE}" ]] || die "pgdrill PITR config template is not readable"

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

latest_run_id="integration-barman-latest-$(date -u +%Y%m%dT%H%M%SZ)"
log "running latest-recovery attempt ${latest_run_id}/attempt-1"
"${PGDRILL}" run \
  -f "${CONFIG}" \
  -run-id "${latest_run_id}" \
  -attempt-id attempt-1 \
  -history-dir "${HISTORY}" 2>&1 | tee /output/run.log

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
pgdrill_integration_verify_history_attempt \
  "${PGDRILL}" \
  "${HISTORY}" \
  "${latest_run_id}" \
  attempt-1 \
  /output/latest-history

log "committing and archiving a transaction after the PITR boundary"
"${PGBIN}/psql" --set ON_ERROR_STOP=1 --command \
  "INSERT INTO public.pgdrill_integration_probe (id, payload) VALUES (102, 'post-target-wal-sentinel');"
post_target_wal="$(${PGBIN}/psql -Atqc 'SELECT pg_walfile_name(pg_current_wal_lsn());')"
"${PGBIN}/psql" -Atqc 'SELECT pg_switch_wal();' >/dev/null

post_target_archived=false
for _ in $(seq 1 60); do
  "${BARMAN}" --config "${BARMAN_CONFIG}" archive-wal integration >/dev/null 2>&1 || true
  if "${BARMAN}" --config "${BARMAN_CONFIG}" get-wal integration "${post_target_wal}" >/dev/null 2>&1; then
    post_target_archived=true
    break
  fi
  sleep 1
done
[[ "${post_target_archived}" == "true" ]] ||
  die "post-target WAL ${post_target_wal} was not archived by Barman"

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

pitr_run_id="integration-barman-pitr-$(date -u +%Y%m%dT%H%M%SZ)"
log "running timestamp-PITR attempt ${pitr_run_id}/attempt-1 to ${pitr_target_time}"
"${PGDRILL}" run \
  -f "${PITR_CONFIG}" \
  -run-id "${pitr_run_id}" \
  -attempt-id attempt-1 \
  -history-dir "${HISTORY}" 2>&1 | tee /output/pitr-run.log

[[ -f /output/pitr-report.json ]] || die "pgdrill did not persist pitr-report.json"
"${PGDRILL}" report show /output/pitr-report.json | tee /output/pitr-report.txt

grep -Eq '^Status[[:space:]]+passed$' /output/pitr-report.txt ||
  die "timestamp PITR report status is not passed"
grep -Eq '^Policy[[:space:]]+5 passed, 0 failed, 0 unknown, 0 not configured$' \
  /output/pitr-report.txt ||
  die "timestamp PITR policy did not produce five passed verdicts"
for check in \
  barman-check \
  barman-check-backup \
  barman-show-backup \
  barman-generate-manifest \
  barman-verify-backup; do
  grep -Eq "^${check}[[:space:]]+-[[:space:]]+passed" /output/pitr-report.txt ||
    die "timestamp PITR ${check} did not pass"
done
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
grep -F '"manifest_state": "existing"' /output/pitr-report.json >/dev/null ||
  die "timestamp PITR report does not identify the verified existing Barman manifest"
[[ ! -e "${WORK_DIR}" ]] || die "owned PITR restore work directory remains after cleanup"
pgdrill_integration_verify_history_attempt \
  "${PGDRILL}" \
  "${HISTORY}" \
  "${pitr_run_id}" \
  attempt-1 \
  /output/pitr-history
pgdrill_integration_capture_history_store "${PGDRILL}" "${HISTORY}" /output 2

{
  printf 'pgdrill=%s\n' "${pgdrill_version}"
  printf 'barman=%s\n' "${barman_version}"
  printf 'postgresql=%s\n' "${postgres_version}"
  printf 'latest_recovery_source_rows=%s\n' "${latest_row_count}"
  printf 'timestamp_recovery_target=%s\n' "${pitr_target_time}"
  printf 'source_rows_after_target=%s\n' "${source_row_count}"
  printf 'backup_id=%s\n' "${backup_id}"
  printf 'sentinel_wal=%s\n' "${sentinel_wal}"
  printf 'post_target_wal=%s\n' "${post_target_wal}"
  "${BARMAN}" --config "${BARMAN_CONFIG}" --format json list-backups integration
} >/output/source-state.txt

log "PASS: latest recovery, timestamp PITR, provider checks, probes, policy, and cleanup completed"
