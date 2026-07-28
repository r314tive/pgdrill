#!/usr/bin/env bash

set -Eeuo pipefail
umask 022

# shellcheck source=test/integration/lib/history.sh
source /opt/pgdrill/test/history.sh

readonly POSTGRES_VERSION="${PGDRILL_POSTGRES_VERSION:?PGDRILL_POSTGRES_VERSION is required}"
readonly POSTGRES_MAJOR="${PGDRILL_POSTGRES_MAJOR:?PGDRILL_POSTGRES_MAJOR is required}"
readonly PGBIN="/usr/lib/postgresql/${POSTGRES_MAJOR}/bin"
readonly PGDRILL="/opt/pgdrill/bin/pgdrill"
readonly WALG="/opt/pgdrill/bin/wal-g"
readonly ROOT="/validation"
readonly PITR_CONFIG="${ROOT}/pgdrill-pitr.yaml"
readonly KILL_CONFIG="${ROOT}/pgdrill-kill.yaml"
readonly WALG_KILL_WRAPPER="${ROOT}/wal-g-kill-wrapper"
readonly WALG_KILL_READY="${ROOT}/wal-g-kill.ready"
readonly WALG_KILL_BYPASS="${ROOT}/wal-g-kill.bypass"
readonly SOURCE_DATA="${ROOT}/source-data"
readonly SOURCE_SOCKET="${ROOT}/source-socket"
readonly SOURCE_LOG="${ROOT}/source.log"
readonly REPOSITORY="${ROOT}/repository"
readonly HISTORY="${ROOT}/history"
readonly WORK_DIR="${ROOT}/work/restore"
readonly SOURCE_PORT="55431"
readonly EXPECTED_COMMIT="${PGDRILL_EXPECTED_COMMIT:?PGDRILL_EXPECTED_COMMIT is required}"
readonly EXPECTED_VERSION="${PGDRILL_EXPECTED_VERSION:?PGDRILL_EXPECTED_VERSION is required}"
readonly STORAGE_MODE="${PGDRILL_WALG_STORAGE:-file}"

export HOME="${ROOT}/home"
export TMPDIR="${ROOT}/tmp"
export PATH="/opt/pgdrill/bin:${PGBIN}:${PATH}"
export PGHOST="${SOURCE_SOCKET}"
export PGPORT="${SOURCE_PORT}"
export PGDATABASE="postgres"
export WALG_COMPRESSION_METHOD="lz4"

case "${STORAGE_MODE}" in
  file)
    CONFIG="/opt/pgdrill/test/pgdrill.yaml"
    PITR_CONFIG_TEMPLATE="/opt/pgdrill/test/pgdrill-pitr.yaml.tmpl"
    STORAGE_BACKEND="file"
    export WALG_FILE_PREFIX="${REPOSITORY}"
    ;;
  s3)
    CONFIG="/opt/pgdrill/test/pgdrill-s3.yaml"
    PITR_CONFIG_TEMPLATE="/opt/pgdrill/test/pgdrill-s3-pitr.yaml.tmpl"
    STORAGE_BACKEND="s3-compatible"
    : "${AWS_ACCESS_KEY_ID:?AWS_ACCESS_KEY_ID is required for S3 storage}"
    : "${AWS_SECRET_ACCESS_KEY:?AWS_SECRET_ACCESS_KEY is required for S3 storage}"
    export AWS_ENDPOINT="http://minio:9000"
    export AWS_REGION="us-east-1"
    export AWS_S3_FORCE_PATH_STYLE="true"
    export WALG_S3_PREFIX="s3://pgdrill-walg/integration"
    ;;
  *)
    printf '[integration/walg] ERROR: unsupported storage mode: %s\n' "${STORAGE_MODE}" >&2
    exit 1
    ;;
esac
readonly CONFIG PITR_CONFIG_TEMPLATE STORAGE_BACKEND

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
    observed_wal="$("${PGBIN}/psql" -Atqc "SELECT COALESCE(last_archived_wal, '') FROM pg_stat_archiver;")"
    if [[ "${observed_wal}" == "${expected_wal}" || "${observed_wal}" > "${expected_wal}" ]]; then
      printf '%s\n' "${observed_wal}"
      return 0
    fi
    sleep 1
  done
  return 1
}

source_running=false
killed_drill_pid=""
cleanup() {
  status="$?"
  trap - EXIT
  set +e
  if [[ -n "${killed_drill_pid}" ]] && kill -0 "${killed_drill_pid}" >/dev/null 2>&1; then
    kill -KILL -- "-${killed_drill_pid}" >/dev/null 2>&1
  fi
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
command -v setsid >/dev/null 2>&1 || die "setsid is required for process-loss testing"
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
postgres_version="$("${PGBIN}/postgres" --version)"
[[ "${postgres_version}" == "postgres (PostgreSQL) ${POSTGRES_VERSION}"* ]] ||
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
archive_command = '/opt/pgdrill/bin/wal-g wal-push "%p"'
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
sentinel_wal="$("${PGBIN}/psql" -Atqc 'SELECT pg_walfile_name(pg_current_wal_lsn());')"
"${PGBIN}/psql" -Atqc 'SELECT pg_switch_wal();' >/dev/null

if ! last_archived_wal="$(wait_for_archived_wal "${sentinel_wal}")"; then
  die "post-backup WAL ${sentinel_wal} was not archived"
fi

latest_row_count="$("${PGBIN}/psql" -Atqc 'SELECT count(*) FROM public.pgdrill_integration_probe;')"
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
  -attempt-id attempt-1 \
  -history-dir "${HISTORY}" 2>&1 | tee /output/run.log

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
pgdrill_integration_verify_history_attempt \
  "${PGDRILL}" \
  "${HISTORY}" \
  "${latest_run_id}" \
  attempt-1 \
  /output/latest-history

log "committing and archiving a transaction after the PITR boundary"
"${PGBIN}/psql" --set ON_ERROR_STOP=1 --command \
  "INSERT INTO public.pgdrill_integration_probe (id, payload) VALUES (102, 'post-target-wal-sentinel');"
post_target_wal="$("${PGBIN}/psql" -Atqc 'SELECT pg_walfile_name(pg_current_wal_lsn());')"
"${PGBIN}/psql" -Atqc 'SELECT pg_switch_wal();' >/dev/null
if ! last_archived_post_target_wal="$(wait_for_archived_wal "${post_target_wal}")"; then
  die "post-target WAL ${post_target_wal} was not archived"
fi

source_row_count="$("${PGBIN}/psql" -Atqc 'SELECT count(*) FROM public.pgdrill_integration_probe;')"
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
  -attempt-id attempt-1 \
  -history-dir "${HISTORY}" 2>&1 | tee /output/pitr-run.log

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
pgdrill_integration_verify_history_attempt \
  "${PGDRILL}" \
  "${HISTORY}" \
  "${pitr_run_id}" \
  attempt-1 \
  /output/pitr-history

log "preparing a deterministic WAL-G process-loss boundary"
cat >"${WALG_KILL_WRAPPER}" <<EOF
#!/usr/bin/env bash
set -Eeuo pipefail
if [[ "\${1:-}" == "backup-fetch" && ! -e "${WALG_KILL_BYPASS}" ]]; then
  : >"${WALG_KILL_READY}"
  while [[ ! -e "${WALG_KILL_BYPASS}" ]]; do
    sleep 1
  done
fi
exec "${WALG}" "\$@"
EOF
chmod 0755 "${WALG_KILL_WRAPPER}"
sed \
  -e "s|binary: ${WALG}|binary: ${WALG_KILL_WRAPPER}|" \
  -e 's|path: /output/report.json|path: /output/killed-report.json|' \
  -e 's/count(\*) = 101/count(*) = 102/' \
  "${CONFIG}" >"${KILL_CONFIG}"
grep -F "binary: ${WALG_KILL_WRAPPER}" "${KILL_CONFIG}" >/dev/null ||
  die "killed-drill provider wrapper was not configured"
grep -F 'path: /output/killed-report.json' "${KILL_CONFIG}" >/dev/null ||
  die "killed-drill report path was not isolated"
grep -F 'count(*) = 102' "${KILL_CONFIG}" >/dev/null ||
  die "killed-drill latest-recovery probe was not updated to the 102-row boundary"
cp "${KILL_CONFIG}" /output/killed-config.yaml

killed_run_id="integration-walg-killed-$(date -u +%Y%m%dT%H%M%SZ)"
log "starting interrupted attempt ${killed_run_id}/attempt-1"
setsid "${PGDRILL}" run \
  -f "${KILL_CONFIG}" \
  -run-id "${killed_run_id}" \
  -attempt-id attempt-1 \
  -history-dir "${HISTORY}" >/output/killed-run.log 2>&1 &
killed_drill_pid="$!"
for _ in $(seq 1 300); do
  if [[ -e "${WALG_KILL_READY}" ]]; then
    break
  fi
  if ! kill -0 "${killed_drill_pid}" >/dev/null 2>&1; then
    wait "${killed_drill_pid}" || true
    die "interrupted attempt exited before the WAL-G mutation boundary"
  fi
  sleep 0.1
done
[[ -e "${WALG_KILL_READY}" ]] ||
  die "interrupted attempt did not reach the WAL-G mutation boundary"

kill -KILL -- "-${killed_drill_pid}"
set +e
wait "${killed_drill_pid}"
killed_status="$?"
set -e
killed_drill_pid=""
[[ "${killed_status}" == "137" ]] ||
  die "interrupted attempt exited with ${killed_status}, expected SIGKILL status 137"
touch "${WALG_KILL_BYPASS}"
[[ -d "${WORK_DIR}" ]] || die "interrupted attempt did not retain its owned target"
[[ ! -e /output/killed-report.json ]] ||
  die "interrupted attempt unexpectedly published a terminal report"

"${PGDRILL}" history show \
  -store "${HISTORY}" \
  -attempt-id attempt-1 \
  -format json \
  "${killed_run_id}" >/output/killed-history.json
grep -F '"type": "run_started"' /output/killed-history.json >/dev/null ||
  die "interrupted attempt has no durable start event"
if grep -F '"type": "run_finished"' /output/killed-history.json >/dev/null; then
  die "interrupted attempt history is unexpectedly terminal"
fi
if grep -F '"report":' /output/killed-history.json >/dev/null; then
  die "interrupted attempt history unexpectedly contains a report"
fi

log "planning and confirming interrupted-attempt reconciliation"
"${PGDRILL}" attempt recover \
  -f "${KILL_CONFIG}" \
  -run-id "${killed_run_id}" \
  -attempt-id attempt-1 \
  -history-store "${HISTORY}" \
  -format json >/output/recovery-plan.json
recovery_digest="$(
  sed -n 's/^[[:space:]]*"digest": "\(sha256:[0-9a-f]\{64\}\)"[[:space:]]*$/\1/p' \
    /output/recovery-plan.json
)"
[[ -n "${recovery_digest}" ]] || die "recovery plan has no canonical digest"
"${PGDRILL}" attempt recover \
  -f "${KILL_CONFIG}" \
  -run-id "${killed_run_id}" \
  -attempt-id attempt-1 \
  -history-store "${HISTORY}" \
  -confirm "${recovery_digest}" \
  -confirm-executor-stopped \
  -format json >/output/recovery-result.json
grep -F '"target_ready_for_retry": true' /output/recovery-result.json >/dev/null ||
  die "recovery did not prove the target ready for retry"
grep -F '"history_preserved": true' /output/recovery-result.json >/dev/null ||
  die "recovery did not preserve incomplete history"
grep -F '"source_reconciliation_complete": false' /output/recovery-result.json >/dev/null ||
  die "unproven killed provider mutation was not retained as unresolved"
[[ ! -e "${WORK_DIR}" ]] || die "recovery left the owned restore work directory"

log "running clean retry ${killed_run_id}/attempt-2 after recovery"
"${PGDRILL}" run \
  -f "${KILL_CONFIG}" \
  -run-id "${killed_run_id}" \
  -attempt-id attempt-2 \
  -history-dir "${HISTORY}" 2>&1 | tee /output/retry-run.log
"${PGDRILL}" report show /output/killed-report.json | tee /output/retry-report.txt
grep -Eq '^Status[[:space:]]+passed$' /output/retry-report.txt ||
  die "clean retry report status is not passed"
grep -Eq '^Attempt[[:space:]]+attempt-2$' /output/retry-report.txt ||
  die "clean retry report has the wrong attempt identity"
grep -Eq '^cleanup[[:space:]]+true[[:space:]]+passed' /output/retry-report.txt ||
  die "clean retry cleanup policy did not pass"
[[ ! -e "${WORK_DIR}" ]] || die "clean retry left the restore work directory"
pgdrill_integration_verify_history_attempt \
  "${PGDRILL}" \
  "${HISTORY}" \
  "${killed_run_id}" \
  attempt-2 \
  /output/retry-history
pgdrill_integration_capture_history_store "${PGDRILL}" "${HISTORY}" /output 4 1

{
  printf 'pgdrill=%s\n' "${pgdrill_version}"
  printf 'wal_g=%s\n' "${walg_version}"
  printf 'postgresql=%s\n' "${postgres_version}"
  printf 'storage_backend=%s\n' "${STORAGE_BACKEND}"
  printf 'latest_recovery_source_rows=%s\n' "${latest_row_count}"
  printf 'timestamp_recovery_target=%s\n' "${pitr_target_time}"
  printf 'source_rows_after_target=%s\n' "${source_row_count}"
  printf 'sentinel_wal=%s\n' "${sentinel_wal}"
  printf 'last_archived_wal=%s\n' "${last_archived_wal}"
  printf 'post_target_wal=%s\n' "${post_target_wal}"
  printf 'last_archived_post_target_wal=%s\n' "${last_archived_post_target_wal}"
  "${WALG}" backup-list --detail --json
} >/output/source-state.txt

log "PASS: latest recovery, timestamp PITR, killed-attempt reconciliation, clean retry, probes, policy, and cleanup completed"
