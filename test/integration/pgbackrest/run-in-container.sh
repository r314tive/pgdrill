#!/usr/bin/env bash

set -Eeuo pipefail
umask 022

# shellcheck source=test/integration/lib/history.sh
source /opt/pgdrill/test/history.sh

readonly PGBIN="/usr/lib/postgresql/18/bin"
readonly PGDRILL="/opt/pgdrill/bin/pgdrill"
readonly PGBACKREST="/usr/bin/pgbackrest"
readonly PGBACKREST_CONFIG="/opt/pgdrill/test/pgbackrest.conf"
readonly STANZA="integration"
readonly CONFIG="/opt/pgdrill/test/pgdrill.yaml"
readonly PITR_CONFIG_TEMPLATE="/opt/pgdrill/test/pgdrill-pitr.yaml.tmpl"
readonly ROOT="/validation"
readonly PITR_CONFIG="${ROOT}/pgdrill-pitr.yaml"
readonly SOURCE_DATA="${ROOT}/source-data"
readonly SOURCE_SOCKET="${ROOT}/source-socket"
readonly SOURCE_LOG="${ROOT}/source.log"
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
export PGBACKREST_CONFIG

log() {
  printf '[integration/pgbackrest] %s\n' "$*"
}

die() {
  printf '[integration/pgbackrest] ERROR: %s\n' "$*" >&2
  exit 1
}

run_pgbackrest() {
  "${PGBACKREST}" --config="${PGBACKREST_CONFIG}" --stanza="${STANZA}" "$@"
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
[[ -x "${PGBACKREST}" ]] || die "pgBackRest binary is not executable"
[[ -r "${PGBACKREST_CONFIG}" ]] || die "pgBackRest config is not readable"
[[ -r "${CONFIG}" ]] || die "pgdrill config is not readable"
[[ -r "${PITR_CONFIG_TEMPLATE}" ]] || die "pgdrill PITR config template is not readable"
command -v perl >/dev/null 2>&1 || die "Perl is required for structured pgBackRest JSON parsing"

mkdir -p \
  "${HOME}" \
  "${TMPDIR}" \
  "${SOURCE_DATA}" \
  "${SOURCE_SOCKET}" \
  "${ROOT}/repository" \
  "${ROOT}/spool" \
  "${ROOT}/lock" \
  "${ROOT}/log" \
  "${ROOT}/work"
chmod 0700 "${HOME}" "${TMPDIR}" "${SOURCE_DATA}" "${SOURCE_SOCKET}" "${ROOT}/work"

pgdrill_version="$(${PGDRILL} version)"
expected_version_prefix="pgdrill ${EXPECTED_VERSION} (${EXPECTED_COMMIT}, "
[[ "${pgdrill_version}" == "${expected_version_prefix}"* ]] ||
  die "pgdrill version is not bound to expected version/commit ${EXPECTED_VERSION}/${EXPECTED_COMMIT}"
pgbackrest_version="$(${PGBACKREST} version | tr '\n' ' ' | sed 's/[[:space:]]*$//')"
[[ "${pgbackrest_version}" == "pgBackRest 2.58.0" ]] ||
  die "unexpected pgBackRest version: ${pgbackrest_version}"
postgres_version="$(${PGBIN}/postgres --version)"
[[ "${postgres_version}" == *" 18.3 "* || "${postgres_version}" == *" 18.3" ]] ||
  die "unexpected PostgreSQL version: ${postgres_version}"

dpkg-query -W \
  '-f=${binary:Package}=${Version}\n' \
  pgbackrest \
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
archive_command = '/usr/bin/pgbackrest --config=/opt/pgdrill/test/pgbackrest.conf --stanza=integration archive-push "%p"'
archive_timeout = '10s'
wal_level = replica
shared_buffers = '32MB'
log_min_messages = info
EOF

"${PGBIN}/pg_ctl" -D "${SOURCE_DATA}" -l "${SOURCE_LOG}" -w -t 30 start
source_running=true

log "creating the pgBackRest stanza"
run_pgbackrest stanza-create 2>&1 | tee /output/stanza-create.log

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

log "checking archive configuration before backup"
run_pgbackrest check 2>&1 | tee /output/check-before-backup.log

log "taking a real pgBackRest full backup"
run_pgbackrest backup --type=full 2>&1 | tee /output/backup.log

backup_info_json="$(run_pgbackrest info --output=json)"
backup_label="$(
  perl -MJSON::PP -0777 -e '
    my $data = decode_json(<>);
    die "expected one stanza\n" unless ref($data) eq "ARRAY" && @$data == 1;
    my $backups = $data->[0]{backup};
    die "expected one backup\n" unless ref($backups) eq "ARRAY" && @$backups == 1;
    die "backup label is missing\n" unless defined $backups->[0]{label} && length $backups->[0]{label};
    print $backups->[0]{label};
  ' <<<"${backup_info_json}"
)"
[[ -n "${backup_label}" ]] || die "pgBackRest did not return a backup label"

log "committing and archiving the post-backup WAL sentinel"
"${PGBIN}/psql" --set ON_ERROR_STOP=1 --command \
  "INSERT INTO public.pgdrill_integration_probe (id, payload) VALUES (101, 'post-backup-wal-sentinel');"
sentinel_wal="$(${PGBIN}/psql -Atqc 'SELECT pg_walfile_name(pg_current_wal_lsn());')"
"${PGBIN}/psql" -Atqc 'SELECT pg_switch_wal();' >/dev/null

run_pgbackrest check 2>&1 | tee /output/check-after-sentinel.log
archived=false
for _ in $(seq 1 60); do
  rm -f "${ROOT}/sentinel.wal"
  if run_pgbackrest archive-get "${sentinel_wal}" "${ROOT}/sentinel.wal" >/dev/null 2>&1; then
    archived=true
    break
  fi
  sleep 1
done
[[ "${archived}" == "true" && -s "${ROOT}/sentinel.wal" ]] ||
  die "post-backup WAL ${sentinel_wal} was not retrievable from pgBackRest"
rm -f "${ROOT}/sentinel.wal"

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

latest_run_id="integration-pgbackrest-latest-$(date -u +%Y%m%dT%H%M%SZ)"
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
for check in pgbackrest-check pgbackrest-verify; do
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

run_pgbackrest check 2>&1 | tee /output/check-after-pitr-boundary.log
post_target_archived=false
for _ in $(seq 1 60); do
  rm -f "${ROOT}/post-target.wal"
  if run_pgbackrest archive-get "${post_target_wal}" "${ROOT}/post-target.wal" >/dev/null 2>&1; then
    post_target_archived=true
    break
  fi
  sleep 1
done
[[ "${post_target_archived}" == "true" && -s "${ROOT}/post-target.wal" ]] ||
  die "post-target WAL ${post_target_wal} was not retrievable from pgBackRest"
rm -f "${ROOT}/post-target.wal"

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

pitr_run_id="integration-pgbackrest-pitr-$(date -u +%Y%m%dT%H%M%SZ)"
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
for check in pgbackrest-check pgbackrest-verify; do
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
  printf 'pgbackrest=%s\n' "${pgbackrest_version}"
  printf 'postgresql=%s\n' "${postgres_version}"
  printf 'latest_recovery_source_rows=%s\n' "${latest_row_count}"
  printf 'timestamp_recovery_target=%s\n' "${pitr_target_time}"
  printf 'source_rows_after_target=%s\n' "${source_row_count}"
  printf 'backup_label=%s\n' "${backup_label}"
  printf 'sentinel_wal=%s\n' "${sentinel_wal}"
  printf 'post_target_wal=%s\n' "${post_target_wal}"
  run_pgbackrest info --output=json
} >/output/source-state.txt

log "PASS: latest recovery, timestamp PITR, provider checks, probes, policy, and cleanup completed"
