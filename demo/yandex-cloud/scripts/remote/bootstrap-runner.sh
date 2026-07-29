#!/usr/bin/env bash

set -Eeuo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR
# shellcheck source=demo/yandex-cloud/scripts/remote/lib.sh
source "${SCRIPT_DIR}/lib.sh"

require_root
[[ "$#" -eq 4 ]] ||
  die "usage: bootstrap-runner.sh <pgdrill-archive> <sha256> <config> <postgres-password-file>"
cd /

readonly PGDRILL_ARCHIVE="$1"
readonly PGDRILL_SHA256="$2"
readonly PGDRILL_CONFIG="$3"
readonly POSTGRES_PASSWORD_FILE="$4"

[[ -f "${PGDRILL_ARCHIVE}" ]] || die "pgdrill archive does not exist: ${PGDRILL_ARCHIVE}"
[[ -f "${PGDRILL_CONFIG}" ]] || die "pgdrill config does not exist: ${PGDRILL_CONFIG}"
[[ -f "${POSTGRES_PASSWORD_FILE}" ]] ||
  die "PostgreSQL password file does not exist: ${POSTGRES_PASSWORD_FILE}"
[[ "${PGDRILL_SHA256}" =~ ^[0-9a-f]{64}$ ]] || die "pgdrill SHA-256 is invalid"
printf '%s  %s\n' "${PGDRILL_SHA256}" "${PGDRILL_ARCHIVE}" | sha256sum --check --status ||
  die "pgdrill archive checksum verification failed"

log "installing PostgreSQL ${PG_MAJOR}, WAL-G ${WALG_VERSION}, and pgdrill on the runner"
install_postgresql
remove_default_postgresql_cluster
install_walg
mount_repository ro

tmpdir="$(mktemp -d)"
trap 'rm -rf "${tmpdir}"' EXIT
tar -xzf "${PGDRILL_ARCHIVE}" -C "${tmpdir}"
pgdrill_binary="$(find "${tmpdir}" -mindepth 2 -maxdepth 2 -type f -name pgdrill -print -quit)"
[[ -n "${pgdrill_binary}" ]] || die "pgdrill archive does not contain the expected binary"
install -o root -g root -m 0755 "${pgdrill_binary}" /usr/local/bin/pgdrill

install -d -o root -g postgres -m 0750 /etc/pgdrill
install -o root -g postgres -m 0640 \
  "${PGDRILL_CONFIG}" /etc/pgdrill/demo.yaml
postgres_password="$(tr -d '\r\n' <"${POSTGRES_PASSWORD_FILE}")"
rm -f -- "${POSTGRES_PASSWORD_FILE}"
[[ "${postgres_password}" =~ ^[0-9a-f]{64}$ ]] ||
  die "PostgreSQL password file does not contain the expected generated secret"
install -o postgres -g postgres -m 0600 /dev/null /etc/pgdrill/pgpass
printf '127.0.0.1:*:*:postgres:%s\n' "${postgres_password}" >/etc/pgdrill/pgpass
unset postgres_password
runuser -u postgres -- test -r /etc/pgdrill/demo.yaml ||
  die "postgres cannot read the installed pgdrill config"
runuser -u postgres -- test -s /etc/pgdrill/pgpass ||
  die "postgres cannot read the installed PostgreSQL password file"
install -d -o postgres -g postgres -m 0700 /var/lib/pgdrill-demo/work
install -d -o postgres -g pgdrill-demo-admins -m 2750 /var/lib/pgdrill-demo/reports

install -o root -g root -m 0755 \
  "${SCRIPT_DIR}/run-drill.sh" /usr/local/sbin/pgdrill-demo-run
install -o root -g root -m 0755 \
  "${SCRIPT_DIR}/doctor.sh" /usr/local/sbin/pgdrill-demo-doctor
install -o root -g root -m 0755 \
  "${SCRIPT_DIR}/report.sh" /usr/local/sbin/pgdrill-demo-report

cat >/etc/sudoers.d/pgdrill-demo-runner <<'EOF'
%pgdrill-demo-admins ALL=(postgres) NOPASSWD: /usr/local/sbin/pgdrill-demo-run, /usr/local/sbin/pgdrill-demo-doctor, /usr/local/sbin/pgdrill-demo-report
EOF
chmod 0440 /etc/sudoers.d/pgdrill-demo-runner
visudo --check --file=/etc/sudoers.d/pgdrill-demo-runner >/dev/null

pgdrill_version="$(/usr/local/bin/pgdrill version | tr '\n' ' ' | sed 's/[[:space:]]*$//')"
postgresql_version="$("${PGBIN}/postgres" --version)"
walg_version="$(/usr/local/bin/wal-g --version | tr '\n' ' ' | sed 's/[[:space:]]*$//')"
jq -n \
  --arg schema_version "pgdrill.demo-runner-inventory/v1alpha1" \
  --arg captured_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --arg pgdrill_archive_sha256 "${PGDRILL_SHA256}" \
  --arg pgdrill_version "${pgdrill_version}" \
  --arg postgresql_version "${postgresql_version}" \
  --arg walg_version "${walg_version}" \
  --arg pgdg_key_fingerprint "${PGDG_KEY_FINGERPRINT}" \
  --argjson postgres_uid "$(id -u postgres)" \
  --argjson postgres_gid "$(id -g postgres)" \
  '{
    schema_version: $schema_version,
    captured_at: $captured_at,
    pgdrill_archive_sha256: $pgdrill_archive_sha256,
    pgdrill_version: $pgdrill_version,
    postgresql_version: $postgresql_version,
    walg_version: $walg_version,
    pgdg_key_fingerprint: $pgdg_key_fingerprint,
    postgres_uid: $postgres_uid,
    postgres_gid: $postgres_gid,
    repository_mount: "/mnt/pgdrill-repository",
    repository_mode: "read_only"
  }' >/var/lib/pgdrill-demo/runner-inventory.json
chown root:pgdrill-demo-admins /var/lib/pgdrill-demo/runner-inventory.json
chmod 0640 /var/lib/pgdrill-demo/runner-inventory.json

runuser -u postgres -- test -r /mnt/pgdrill-repository/.pgdrill-demo-repository ||
  die "postgres cannot read the repository marker"
if runuser -u postgres -- touch /mnt/pgdrill-repository/.runner-write-test 2>/dev/null; then
  runuser -u postgres -- rm -f /mnt/pgdrill-repository/.runner-write-test
  die "runner repository mount unexpectedly permits writes"
fi

log "runner bootstrap complete"
cat /var/lib/pgdrill-demo/runner-inventory.json
