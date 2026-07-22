#!/usr/bin/env bash

set -Eeuo pipefail
umask 022

readonly PGBIN="/opt/postgresql-18.3/bin"
readonly PGDRILL="/opt/pgdrill/bin/pgdrill"
readonly PGPROBACKUP="${PGBIN}/pg_probackup"
readonly CONFIG="/opt/pgdrill/test/pgdrill.yaml"
readonly ROOT="/validation"
readonly SOURCE_DATA="${ROOT}/source-data"
readonly SOURCE_SOCKET="${ROOT}/source-socket"
readonly SOURCE_LOG="${ROOT}/source.log"
readonly BACKUP_DIR="${ROOT}/backup"
readonly ARCHIVE_GET_DATA="${ROOT}/archive-get-data"
readonly INSTANCE="integration"
readonly WORK_DIR="${ROOT}/work/restore"
readonly SOURCE_PORT="55431"
readonly PGPROBACKUP_COMMIT="79b986494ecea8bbd67f97a62ba8ae4a00703586"
readonly POSTGRES_SOURCE_SHA256="d95663fbbf3a80f81a9d98d895266bdcb74ba274bcc04ef6d76630a72dee016f"
readonly EXPECTED_COMMIT="${PGDRILL_EXPECTED_COMMIT:?PGDRILL_EXPECTED_COMMIT is required}"
readonly EXPECTED_VERSION="${PGDRILL_EXPECTED_VERSION:?PGDRILL_EXPECTED_VERSION is required}"

export HOME="${ROOT}/home"
export TMPDIR="${ROOT}/tmp"
export PATH="/opt/pgdrill/bin:${PGBIN}:${PATH}"
export PGHOST="${SOURCE_SOCKET}"
export PGPORT="${SOURCE_PORT}"
export PGDATABASE="postgres"
export PGUSER="postgres"

log() {
  printf '[integration/pgprobackup] %s\n' "$*"
}

die() {
  printf '[integration/pgprobackup] ERROR: %s\n' "$*" >&2
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
  instance_config="${BACKUP_DIR}/backups/${INSTANCE}/pg_probackup.conf"
  if [[ -f "${instance_config}" ]]; then
    cp "${instance_config}" /output/pg_probackup.conf
  fi
  exit "${status}"
}
trap cleanup EXIT

[[ "$(id -u)" == "999" ]] || die "container must run as the postgres UID 999"
[[ -x "${PGDRILL}" ]] || die "pgdrill binary is not executable"
[[ -x "${PGPROBACKUP}" ]] || die "pg_probackup binary is not executable"
[[ -r "${CONFIG}" ]] || die "pgdrill config is not readable"
command -v perl >/dev/null 2>&1 || die "Perl is required for structured pg_probackup JSON parsing"

mkdir -p \
  "${HOME}" \
  "${TMPDIR}" \
  "${SOURCE_DATA}" \
  "${SOURCE_SOCKET}" \
  "${ROOT}/work"
chmod 0700 "${HOME}" "${TMPDIR}" "${SOURCE_DATA}" "${SOURCE_SOCKET}" "${ROOT}/work"

pgdrill_version="$(${PGDRILL} version)"
expected_version_prefix="pgdrill ${EXPECTED_VERSION} (${EXPECTED_COMMIT}, "
[[ "${pgdrill_version}" == "${expected_version_prefix}"* ]] ||
  die "pgdrill version is not bound to expected version/commit ${EXPECTED_VERSION}/${EXPECTED_COMMIT}"
pgprobackup_version="$(${PGPROBACKUP} version | tr '\n' ' ' | sed 's/[[:space:]]*$//')"
[[ "${pgprobackup_version}" == *"2.5.16"* ]] ||
  die "unexpected pg_probackup version: ${pgprobackup_version}"
postgres_version="$(${PGBIN}/postgres --version)"
[[ "${postgres_version}" == *" 18.3 "* || "${postgres_version}" == *" 18.3" ]] ||
  die "unexpected PostgreSQL version: ${postgres_version}"

{
  printf 'pg_probackup_source_commit=%s\n' "${PGPROBACKUP_COMMIT}"
  printf 'postgresql_source_sha256=%s\n' "${POSTGRES_SOURCE_SHA256}"
  printf 'pg_configure=%s\n' "$(${PGBIN}/pg_config --configure)"
} >/output/source-build.txt
sha256sum \
  "${PGPROBACKUP}" \
  "${PGBIN}/postgres" \
  "${PGBIN}/pg_amcheck" \
  "${PGBIN}/pg_dump" > /output/provider-binaries.sha256

log "initializing checksummed PostgreSQL source and backup catalog"
"${PGBIN}/initdb" \
  --pgdata "${SOURCE_DATA}" \
  --auth-local trust \
  --auth-host trust \
  --encoding UTF8 \
  --locale C.UTF-8 \
  --data-checksums >/output/initdb.log
"${PGPROBACKUP}" init -B "${BACKUP_DIR}" 2>&1 | tee /output/catalog-init.log
"${PGPROBACKUP}" add-instance \
  -B "${BACKUP_DIR}" \
  --instance="${INSTANCE}" \
  -D "${SOURCE_DATA}" 2>&1 | tee /output/add-instance.log

cat >>"${SOURCE_DATA}/postgresql.conf" <<'EOF'
listen_addresses = '127.0.0.1'
port = 55431
unix_socket_directories = '/validation/source-socket'
archive_mode = on
archive_command = '/opt/postgresql-18.3/bin/pg_probackup archive-push -B /validation/backup --instance=integration --wal-file-path=%p --wal-file-name=%f'
archive_timeout = '10s'
wal_level = replica
max_wal_senders = 5
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

log "taking a compressed pg_probackup full STREAM backup"
"${PGPROBACKUP}" backup \
  -B "${BACKUP_DIR}" \
  --instance="${INSTANCE}" \
  -b FULL \
  --stream \
  --temp-slot \
  --compress \
  --pghost="${SOURCE_SOCKET}" \
  --pgport="${SOURCE_PORT}" \
  --pgdatabase=postgres \
  --pguser=postgres \
  -j 2 2>&1 | tee /output/backup.log

backup_list_json="$(${PGPROBACKUP} show -B "${BACKUP_DIR}" --instance="${INSTANCE}" --format=json)"
backup_id="$(
  perl -MJSON::PP -0777 -e '
    my $data = decode_json(<>);
    die "expected one instance\n" unless ref($data) eq "ARRAY" && @$data == 1;
    my $backups = $data->[0]{backups};
    die "expected one backup\n" unless ref($backups) eq "ARRAY" && @$backups == 1;
    die "backup id is missing\n" unless defined $backups->[0]{id} && length $backups->[0]{id};
    print $backups->[0]{id};
  ' <<<"${backup_list_json}"
)"
[[ -n "${backup_id}" ]] || die "pg_probackup did not return a backup ID"

log "committing and archiving the post-backup WAL sentinel"
"${PGBIN}/psql" --set ON_ERROR_STOP=1 --command \
  "INSERT INTO public.pgdrill_integration_probe (id, payload) VALUES (101, 'post-backup-wal-sentinel');"
sentinel_wal="$(${PGBIN}/psql -Atqc 'SELECT pg_walfile_name(pg_current_wal_lsn());')"
"${PGBIN}/psql" -Atqc 'SELECT pg_switch_wal();' >/dev/null

mkdir -p "${ARCHIVE_GET_DATA}/global" "${ARCHIVE_GET_DATA}/pg_wal"
cp "${SOURCE_DATA}/global/pg_control" "${ARCHIVE_GET_DATA}/global/pg_control"
archive_get_wal="${ARCHIVE_GET_DATA}/pg_wal/${sentinel_wal}"
archived=false
archive_get_log="/output/archive-get-sentinel.log"
: >"${archive_get_log}"
for attempt in $(seq 1 60); do
  rm -f "${archive_get_wal}"
  printf 'attempt=%s wal=%s\n' "${attempt}" "${sentinel_wal}" >>"${archive_get_log}"
  if (
    cd "${ARCHIVE_GET_DATA}"
    "${PGPROBACKUP}" archive-get \
      -B "${BACKUP_DIR}" \
      --instance="${INSTANCE}" \
      --wal-file-path="pg_wal/${sentinel_wal}" \
      --wal-file-name="${sentinel_wal}" >>"${archive_get_log}" 2>&1
  ); then
    archived=true
    break
  fi
  sleep 1
done
find "${BACKUP_DIR}/wal/${INSTANCE}" -type f -print | sort >/output/archive-files.txt
[[ "${archived}" == "true" && -s "${archive_get_wal}" ]] ||
  die "post-backup WAL ${sentinel_wal} was not retrievable from pg_probackup"
cmp -s "${BACKUP_DIR}/wal/${INSTANCE}/${sentinel_wal}" "${archive_get_wal}" ||
  die "retrieved post-backup WAL ${sentinel_wal} differs from the archive"
rm -rf "${ARCHIVE_GET_DATA}"

log "validating the native backup and WAL archive"
"${PGPROBACKUP}" validate \
  -B "${BACKUP_DIR}" \
  --instance="${INSTANCE}" \
  -i "${backup_id}" \
  -j 2 \
  --wal \
  --recovery-target=latest 2>&1 | tee /output/validate-before-drill.log

row_count="$(${PGBIN}/psql -Atqc 'SELECT count(*) FROM public.pgdrill_integration_probe;')"
[[ "${row_count}" == "101" ]] || die "source row count is ${row_count}, expected 101"

log "capturing read-only preflight and catalog evidence"
"${PGDRILL}" doctor -f "${CONFIG}" -format json >/output/doctor.json
"${PGDRILL}" catalog list -f "${CONFIG}" -format json -evidence >/output/catalog.json

run_id="integration-pgprobackup-$(date -u +%Y%m%dT%H%M%SZ)"
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
grep -Eq '^pg-probackup-validate[[:space:]]+-[[:space:]]+passed' /output/report.txt ||
  die "pg_probackup validate did not pass"
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
  printf 'pg_probackup=%s\n' "${pgprobackup_version}"
  printf 'postgresql=%s\n' "${postgres_version}"
  printf 'source_rows=%s\n' "${row_count}"
  printf 'backup_id=%s\n' "${backup_id}"
  printf 'sentinel_wal=%s\n' "${sentinel_wal}"
  "${PGPROBACKUP}" show -B "${BACKUP_DIR}" --instance="${INSTANCE}" --format=json
} >/output/source-state.txt

log "PASS: real backup, WAL replay, native validation, probes, policy, and cleanup completed"
