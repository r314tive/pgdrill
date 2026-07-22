#!/usr/bin/env bash

set -Eeuo pipefail
umask 022

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR
ROOT="$(cd -- "${SCRIPT_DIR}/../../.." && pwd)"
readonly ROOT
readonly CACHE_ROOT="${PGDRILL_INTEGRATION_CACHE:-${ROOT}/.cache/integration/pgprobackup}"
readonly RUNS_DIR="${CACHE_ROOT}/runs"
readonly PGPROBACKUP_VERSION="2.5.16"
readonly POSTGRES_VERSION="18.3"
readonly POSTGRES_IMAGE="postgres@sha256:7e32e9833a6fb1c92c32552794cb6ed569d51b445a54907d35fc112ef39684db"
readonly VERSION_BASE="${PGDRILL_INTEGRATION_VERSION:-v0.0.0-integration}"
readonly PGDRILL_INTEGRATION_LOG_PREFIX="integration/pgprobackup-host"

# shellcheck source=test/integration/lib/runtime.sh
source "${SCRIPT_DIR}/../lib/runtime.sh"

log() {
  pgdrill_integration_log "$@"
}

die() {
  pgdrill_integration_die "$@"
}

pgdrill_integration_require_commands awk docker git go tar
docker info >/dev/null 2>&1 || die "Docker daemon is unavailable"
git -C "${ROOT}" rev-parse --is-inside-work-tree >/dev/null 2>&1 ||
  die "integration test must run from a Git checkout"

docker_arch="$(docker info --format '{{.Architecture}}')"
arch="$(pgdrill_integration_docker_arch)"
readonly arch

pgdrill_integration_prepare_pgdrill "${ROOT}" "${CACHE_ROOT}" "${arch}" "${VERSION_BASE}"
readonly PGDRILL_BINARY="${PGDRILL_INT_BINARY}"
readonly version="${PGDRILL_INT_VERSION}"
readonly commit="${PGDRILL_INT_COMMIT}"

pgdrill_integration_prepare_image \
  "${PGDRILL_INTEGRATION_PGPROBACKUP_IMAGE:-}" \
  "pgdrill-pgprobackup-integration:${PGPROBACKUP_VERSION}-postgresql-${POSTGRES_VERSION}-${arch}" \
  "pg_probackup ${PGPROBACKUP_VERSION}" \
  "${POSTGRES_VERSION}" \
  "${POSTGRES_IMAGE}" \
  "${SCRIPT_DIR}/Dockerfile" \
  "${SCRIPT_DIR}" \
  "${arch}"
readonly pgprobackup_image="${PGDRILL_INT_CONTAINER_IMAGE}"

run_stamp="$(date -u +%Y%m%dT%H%M%SZ)-$$"
readonly OUTPUT_DIR="${RUNS_DIR}/${run_stamp}"
readonly CONTAINER_NAME="pgdrill-pgprobackup-integration-${run_stamp}"
mkdir -p "${OUTPUT_DIR}"
chmod 0777 "${OUTPUT_DIR}"

{
  pgdrill_integration_print_image_inventory "${POSTGRES_IMAGE}"
  pgdrill_integration_print_runtime_inventory "${docker_arch}"
} >"${OUTPUT_DIR}/runtime.txt"

cleanup_container() {
  docker rm -f "${CONTAINER_NAME}" >/dev/null 2>&1 || true
  chmod 0750 "${OUTPUT_DIR}" >/dev/null 2>&1 || true
}
trap cleanup_container EXIT INT TERM

log "starting network-isolated rootless drill"
pgdrill_integration_docker_run "${CONTAINER_NAME}" "${arch}" 4294967296 \
  --mount "type=bind,src=${PGDRILL_BINARY},dst=/opt/pgdrill/bin/pgdrill,readonly" \
  --mount "type=bind,src=${SCRIPT_DIR}/run-in-container.sh,dst=/opt/pgdrill/test/run-in-container.sh,readonly" \
  --mount "type=bind,src=${SCRIPT_DIR}/pgdrill.yaml,dst=/opt/pgdrill/test/pgdrill.yaml,readonly" \
  --mount "type=bind,src=${OUTPUT_DIR},dst=/output" \
  --env "PGDRILL_EXPECTED_COMMIT=${commit}" \
  --env "PGDRILL_EXPECTED_VERSION=${version}" \
  "${pgprobackup_image}" \
  /opt/pgdrill/test/run-in-container.sh 2>&1 | tee "${OUTPUT_DIR}/container.log"
chmod 0750 "${OUTPUT_DIR}"
trap - EXIT INT TERM

pgdrill_integration_finalize_artifacts "${OUTPUT_DIR}" "${CACHE_ROOT}"

log "PASS: artifacts retained at ${OUTPUT_DIR}"
log "inspect from the source checkout with: go run ./cmd/pgdrill report show ${OUTPUT_DIR}/report.json"
