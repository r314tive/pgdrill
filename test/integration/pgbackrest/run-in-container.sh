#!/usr/bin/env bash

set -Eeuo pipefail
umask 022

readonly PGBIN="/usr/lib/postgresql/18/bin"
readonly PGDRILL="/opt/pgdrill/bin/pgdrill"
readonly PGBACKREST="/usr/bin/pgbackrest"
readonly PGBACKREST_CONFIG="/opt/pgdrill/test/pgbackrest.conf"
readonly STANZA="integration"
readonly CONFIG="/opt/pgdrill/test/pgdrill.yaml"
readonly ROOT="/validation"
readonly SOURCE_DATA="${ROOT}/source-data"
readonly SOURCE_SOCKET="${ROOT}/source-socket"
readonly SOURCE_LOG="${ROOT}/source.log"
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

row_count="$(${PGBIN}/psql -Atqc 'SELECT count(*) FROM public.pgdrill_integration_probe;')"
[[ "${row_count}" == "101" ]] || die "source row count is ${row_count}, expected 101"

log "capturing read-only preflight and catalog evidence"
"${PGDRILL}" doctor -f "${CONFIG}" -format json >/output/doctor.json
"${PGDRILL}" catalog list -f "${CONFIG}" -format json -evidence >/output/catalog.json

run_id="integration-pgbackrest-$(date -u +%Y%m%dT%H%M%SZ)"
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

{
  printf 'pgdrill=%s\n' "${pgdrill_version}"
  printf 'pgbackrest=%s\n' "${pgbackrest_version}"
  printf 'postgresql=%s\n' "${postgres_version}"
  printf 'source_rows=%s\n' "${row_count}"
  printf 'backup_label=%s\n' "${backup_label}"
  printf 'sentinel_wal=%s\n' "${sentinel_wal}"
  run_pgbackrest info --output=json
} >/output/source-state.txt

log "PASS: real backup, WAL replay, provider checks, probes, policy, and cleanup completed"
