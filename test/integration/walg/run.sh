#!/usr/bin/env bash

set -Eeuo pipefail
umask 022

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR
ROOT="$(cd -- "${SCRIPT_DIR}/../../.." && pwd)"
readonly ROOT
readonly CACHE_ROOT="${PGDRILL_INTEGRATION_CACHE:-${ROOT}/.cache/integration/walg}"
readonly RUNS_DIR="${CACHE_ROOT}/runs"
readonly WALG_VERSION="3.0.8"
readonly POSTGRES_IMAGE_DEFAULT="postgres@sha256:7e32e9833a6fb1c92c32552794cb6ed569d51b445a54907d35fc112ef39684db"
readonly POSTGRES_IMAGE="${PGDRILL_INTEGRATION_POSTGRES_IMAGE:-${POSTGRES_IMAGE_DEFAULT}}"
readonly VERSION_BASE="${PGDRILL_INTEGRATION_VERSION:-v0.0.0-integration}"
readonly PGDRILL_INTEGRATION_LOG_PREFIX="integration/walg-host"

# shellcheck source=test/integration/lib/runtime.sh
source "${SCRIPT_DIR}/../lib/runtime.sh"

log() {
  pgdrill_integration_log "$@"
}

die() {
  pgdrill_integration_die "$@"
}

pgdrill_integration_require_commands awk curl docker git go tar
docker info >/dev/null 2>&1 || die "Docker daemon is unavailable"
git -C "${ROOT}" rev-parse --is-inside-work-tree >/dev/null 2>&1 ||
  die "integration test must run from a Git checkout"

docker_arch="$(docker info --format '{{.Architecture}}')"
arch="$(pgdrill_integration_docker_arch)"
case "${arch}" in
  amd64)
    arch="amd64"
    walg_asset="wal-g-pg-24.04-amd64"
    walg_sha256="342574292b1907af738d48ff2d1d771ad90a63e441b40a85208022144253f6b8"
    ;;
  arm64)
    arch="arm64"
    walg_asset="wal-g-pg-24.04-aarch64"
    walg_sha256="a822caafa9ee61c2f96add3e768c06971677d8b7a6781e585253b8735a3bc4f7"
    ;;
esac
readonly arch walg_asset walg_sha256

pgdrill_integration_prepare_pgdrill "${ROOT}" "${CACHE_ROOT}" "${arch}" "${VERSION_BASE}"
readonly RUNTIME_DIR="${PGDRILL_INT_RUNTIME_DIR}"
readonly PGDRILL_BINARY="${PGDRILL_INT_BINARY}"
readonly WALG_BINARY="${RUNTIME_DIR}/wal-g"
readonly version="${PGDRILL_INT_VERSION}"
readonly commit="${PGDRILL_INT_COMMIT}"

if [[ ! -f "${WALG_BINARY}" ]] || ! pgdrill_integration_verify_file "${walg_sha256}" "${WALG_BINARY}"; then
  log "downloading pinned WAL-G ${WALG_VERSION} for linux/${arch}"
  download="$(mktemp "${RUNTIME_DIR}/wal-g.download.XXXXXX")"
  trap 'rm -f "${download:-}"' EXIT
  curl \
    --fail \
    --location \
    --proto '=https' \
    --tlsv1.2 \
    --retry 5 \
    --retry-all-errors \
    --output "${download}" \
    "https://github.com/wal-g/wal-g/releases/download/v${WALG_VERSION}/${walg_asset}"
  pgdrill_integration_verify_file "${walg_sha256}" "${download}" || die "WAL-G checksum verification failed"
  chmod 0755 "${download}"
  mv "${download}" "${WALG_BINARY}"
  trap - EXIT
fi

run_stamp="$(date -u +%Y%m%dT%H%M%SZ)-$$"
readonly OUTPUT_DIR="${RUNS_DIR}/${run_stamp}"
readonly CONTAINER_NAME="pgdrill-walg-integration-${run_stamp}"
mkdir -p "${OUTPUT_DIR}"
chmod 0777 "${OUTPUT_DIR}"

if docker image inspect "${POSTGRES_IMAGE}" >/dev/null 2>&1; then
  log "using cached immutable PostgreSQL 18.3 image for linux/${arch}"
else
  log "pulling immutable PostgreSQL 18.3 image for linux/${arch}"
  docker pull --platform "linux/${arch}" "${POSTGRES_IMAGE}" >/dev/null
fi
image_id="$(docker image inspect --format '{{.Id}}' "${POSTGRES_IMAGE}")"

{
  printf 'container_image=%s\n' "${POSTGRES_IMAGE}"
  printf 'container_image_id=%s\n' "${image_id}"
  pgdrill_integration_print_runtime_inventory "${docker_arch}"
  printf 'wal_g_sha256=%s\n' "$(pgdrill_integration_sha256_file "${WALG_BINARY}")"
} >"${OUTPUT_DIR}/runtime.txt"

cleanup_container() {
  docker rm -f "${CONTAINER_NAME}" >/dev/null 2>&1 || true
  chmod 0750 "${OUTPUT_DIR}" >/dev/null 2>&1 || true
}
trap cleanup_container EXIT INT TERM

log "starting network-isolated rootless drill"
pgdrill_integration_docker_run "${CONTAINER_NAME}" "${arch}" 2147483648 \
  --mount "type=bind,src=${PGDRILL_BINARY},dst=/opt/pgdrill/bin/pgdrill,readonly" \
  --mount "type=bind,src=${WALG_BINARY},dst=/opt/pgdrill/bin/wal-g,readonly" \
  --mount "type=bind,src=${SCRIPT_DIR}/run-in-container.sh,dst=/opt/pgdrill/test/run-in-container.sh,readonly" \
  --mount "type=bind,src=${SCRIPT_DIR}/pgdrill.yaml,dst=/opt/pgdrill/test/pgdrill.yaml,readonly" \
  --mount "type=bind,src=${OUTPUT_DIR},dst=/output" \
  --env "PGDRILL_EXPECTED_COMMIT=${commit}" \
  --env "PGDRILL_EXPECTED_VERSION=${version}" \
  "${POSTGRES_IMAGE}" \
  /opt/pgdrill/test/run-in-container.sh 2>&1 | tee "${OUTPUT_DIR}/container.log"
chmod 0750 "${OUTPUT_DIR}"
trap - EXIT INT TERM

pgdrill_integration_finalize_artifacts "${OUTPUT_DIR}" "${CACHE_ROOT}"

log "PASS: artifacts retained at ${OUTPUT_DIR}"
log "inspect from the source checkout with: go run ./cmd/pgdrill report show ${OUTPUT_DIR}/report.json"
