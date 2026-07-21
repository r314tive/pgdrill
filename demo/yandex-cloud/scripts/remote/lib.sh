#!/usr/bin/env bash

set -Eeuo pipefail

readonly PG_MAJOR="18"
readonly POSTGRES_UID="2000"
readonly POSTGRES_GID="2000"
readonly PGBIN="/usr/lib/postgresql/${PG_MAJOR}/bin"
readonly REPOSITORY_MOUNT="/mnt/pgdrill-repository"
# Used by scripts that source this helper.
# shellcheck disable=SC2034
readonly WALG_REPOSITORY="${REPOSITORY_MOUNT}/walg"
readonly WALG_VERSION="3.0.8"
readonly WALG_ASSET="wal-g-pg-24.04-amd64"
readonly WALG_SHA256="342574292b1907af738d48ff2d1d771ad90a63e441b40a85208022144253f6b8"
readonly WALG_URL="https://github.com/wal-g/wal-g/releases/download/v${WALG_VERSION}/${WALG_ASSET}"
readonly PGDG_KEY_FINGERPRINT="B97B0AFCAA1A47F044F244A07FCC7D46ACCC4CF8"

log() {
  printf '[pgdrill-demo] %s\n' "$*"
}

die() {
  printf '[pgdrill-demo] ERROR: %s\n' "$*" >&2
  exit 1
}

require_root() {
  [[ "${EUID}" -eq 0 ]] || die "this command must run as root"
}

require_no_args() {
  [[ "$#" -eq 0 ]] || die "this command does not accept arguments"
}

install_postgresql() {
  local key_fingerprint

  require_root

  # shellcheck disable=SC1091
  source /etc/os-release
  [[ "${ID:-}" == "ubuntu" && "${VERSION_CODENAME:-}" == "noble" ]] ||
    die "the demo bootstrap supports only Ubuntu 24.04 (noble)"

  if getent group "${POSTGRES_GID}" >/dev/null && ! getent group postgres >/dev/null; then
    die "required PostgreSQL GID ${POSTGRES_GID} is already assigned"
  fi
  if getent passwd "${POSTGRES_UID}" >/dev/null && ! getent passwd postgres >/dev/null; then
    die "required PostgreSQL UID ${POSTGRES_UID} is already assigned"
  fi
  if getent group postgres >/dev/null; then
    [[ "$(getent group postgres | cut -d: -f3)" == "${POSTGRES_GID}" ]] ||
      die "existing postgres group does not use required GID ${POSTGRES_GID}"
  else
    groupadd --gid "${POSTGRES_GID}" postgres
  fi
  if getent passwd postgres >/dev/null; then
    [[ "$(id -u postgres)" == "${POSTGRES_UID}" && "$(id -g postgres)" == "${POSTGRES_GID}" ]] ||
      die "existing postgres user does not use required UID:GID ${POSTGRES_UID}:${POSTGRES_GID}"
  else
    useradd \
      --uid "${POSTGRES_UID}" \
      --gid "${POSTGRES_GID}" \
      --home-dir /var/lib/postgresql \
      --create-home \
      --shell /bin/bash \
      postgres
  fi

  install -d -m 0755 /usr/share/postgresql-common/pgdg
  if [[ ! -s /usr/share/postgresql-common/pgdg/apt.postgresql.org.asc ]]; then
    curl --fail --location --proto '=https' --tlsv1.2 \
      --retry 5 --retry-all-errors \
      --output /usr/share/postgresql-common/pgdg/apt.postgresql.org.asc \
      https://www.postgresql.org/media/keys/ACCC4CF8.asc
  fi
  key_fingerprint="$(
    gpg --batch --show-keys --with-colons \
      /usr/share/postgresql-common/pgdg/apt.postgresql.org.asc 2>/dev/null |
      awk -F: '$1 == "fpr" { print $10; exit }'
  )"
  [[ "${key_fingerprint}" == "${PGDG_KEY_FINGERPRINT}" ]] ||
    die "PGDG signing-key fingerprint verification failed"

  printf '%s\n' \
    "deb [signed-by=/usr/share/postgresql-common/pgdg/apt.postgresql.org.asc] https://apt.postgresql.org/pub/repos/apt noble-pgdg main" \
    >/etc/apt/sources.list.d/pgdg.list

  apt-get update
  DEBIAN_FRONTEND=noninteractive apt-get install -y \
    "postgresql-${PG_MAJOR}" \
    "postgresql-client-${PG_MAJOR}"

  "${PGBIN}/postgres" --version
  "${PGBIN}/psql" --version
}

remove_default_postgresql_cluster() {
  require_root

  if [[ -d "/etc/postgresql/${PG_MAJOR}/main" ]]; then
    pg_dropcluster --stop "${PG_MAJOR}" main
  fi
}

install_walg() {
  require_root

  local download
  download="$(mktemp)"
  if ! curl --fail --location --proto '=https' --tlsv1.2 \
    --retry 5 --retry-all-errors \
    --output "${download}" \
    "${WALG_URL}"; then
    rm -f "${download}"
    die "WAL-G download failed"
  fi
  if ! printf '%s  %s\n' "${WALG_SHA256}" "${download}" | sha256sum --check --status; then
    rm -f "${download}"
    die "WAL-G checksum verification failed"
  fi
  install -o root -g root -m 0755 "${download}" /usr/local/bin/wal-g
  rm -f "${download}"

  /usr/local/bin/wal-g version | grep -F "v${WALG_VERSION}" >/dev/null ||
    die "installed WAL-G did not report v${WALG_VERSION}"
}

mount_repository() {
  local required_mode="$1"
  local options

  [[ "${required_mode}" == "ro" || "${required_mode}" == "rw" ]] ||
    die "mount_repository requires ro or rw"

  install -d -m 0755 "${REPOSITORY_MOUNT}"
  for _ in {1..30}; do
    if ! mountpoint --quiet "${REPOSITORY_MOUNT}"; then
      mount "${REPOSITORY_MOUNT}" >/dev/null 2>&1 || true
    fi
    if mountpoint --quiet "${REPOSITORY_MOUNT}"; then
      # Trigger x-systemd.automount so findmnt observes NFS, not only autofs.
      runuser -u postgres -- ls -ld "${REPOSITORY_MOUNT}/." >/dev/null 2>&1 || true
      if [[ "$(findmnt --noheadings --raw --output FSTYPE --target "${REPOSITORY_MOUNT}")" != nfs* ]]; then
        sleep 2
        continue
      fi
      options="$(findmnt --noheadings --raw --output OPTIONS --target "${REPOSITORY_MOUNT}")"
      if [[ ",${options}," == *",${required_mode},"* ]]; then
        findmnt --noheadings --output SOURCE,FSTYPE,OPTIONS --target "${REPOSITORY_MOUNT}"
        return 0
      fi
      die "repository is mounted without required ${required_mode} mode: ${options}"
    fi
    sleep 2
  done
  die "repository mount did not become available"
}

wait_for_postgres() {
  for _ in {1..60}; do
    if runuser -u postgres -- "${PGBIN}/pg_isready" \
      --host /var/run/postgresql --port 5432 --dbname postgres >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  die "demo source PostgreSQL did not become ready"
}
