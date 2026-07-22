#!/usr/bin/env bash

set -Eeuo pipefail
umask 022

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR
ROOT="$(cd -- "${SCRIPT_DIR}/../../.." && pwd)"
readonly ROOT
readonly CACHE_ROOT="${PGDRILL_INTEGRATION_CACHE:-${ROOT}/.cache/integration/barman}"
readonly RUNS_DIR="${CACHE_ROOT}/runs"
readonly BARMAN_VERSION="3.19.1"
readonly POSTGRES_VERSION="18.3"
readonly POSTGRES_IMAGE="postgres@sha256:7e32e9833a6fb1c92c32552794cb6ed569d51b445a54907d35fc112ef39684db"
readonly VERSION_BASE="${PGDRILL_INTEGRATION_VERSION:-v0.0.0-integration}"
readonly PGDRILL_INTEGRATION_LOG_PREFIX="integration/barman-host"

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

definition_sha256="$(pgdrill_integration_sha256_file "${SCRIPT_DIR}/Dockerfile")"
image_source="pinned_build"
if [[ -n "${PGDRILL_INTEGRATION_BARMAN_IMAGE:-}" ]]; then
  barman_image="${PGDRILL_INTEGRATION_BARMAN_IMAGE}"
  image_source="explicit_override"
  docker image inspect "${barman_image}" >/dev/null 2>&1 ||
    die "explicit Barman image is not present locally: ${barman_image}"
else
  barman_image="pgdrill-barman-integration:${BARMAN_VERSION}-postgresql-${POSTGRES_VERSION}-${arch}"
  cached_definition="$(docker image inspect --format '{{ index .Config.Labels "org.pgdrill.integration.definition-sha" }}' "${barman_image}" 2>/dev/null || true)"
  if [[ "${cached_definition}" != "${definition_sha256}" ]]; then
    if docker image inspect "${POSTGRES_IMAGE}" >/dev/null 2>&1; then
      log "using cached immutable PostgreSQL ${POSTGRES_VERSION} base image for linux/${arch}"
    else
      log "pulling immutable PostgreSQL ${POSTGRES_VERSION} base image for linux/${arch}"
      docker pull --platform "linux/${arch}" "${POSTGRES_IMAGE}" >/dev/null
    fi
    log "building pinned Barman ${BARMAN_VERSION} runtime for linux/${arch}"
    docker build \
      --pull=false \
      --platform "linux/${arch}" \
      --build-arg "PGDRILL_INTEGRATION_DEFINITION_SHA=${definition_sha256}" \
      --tag "${barman_image}" \
      --file "${SCRIPT_DIR}/Dockerfile" \
      "${SCRIPT_DIR}"
  else
    log "using cached pinned Barman ${BARMAN_VERSION} runtime for linux/${arch}"
  fi
fi
readonly barman_image definition_sha256 image_source

image_id="$(docker image inspect --format '{{.Id}}' "${barman_image}")"
run_stamp="$(date -u +%Y%m%dT%H%M%SZ)-$$"
readonly OUTPUT_DIR="${RUNS_DIR}/${run_stamp}"
readonly CONTAINER_NAME="pgdrill-barman-integration-${run_stamp}"
mkdir -p "${OUTPUT_DIR}"
chmod 0777 "${OUTPUT_DIR}"

{
  printf 'container_image=%s\n' "${barman_image}"
  printf 'container_image_id=%s\n' "${image_id}"
  printf 'container_image_source=%s\n' "${image_source}"
  printf 'container_definition_sha256=%s\n' "${definition_sha256}"
  printf 'container_base_image=%s\n' "${POSTGRES_IMAGE}"
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
  --mount "type=bind,src=${SCRIPT_DIR}/barman.conf,dst=/opt/pgdrill/test/barman.conf,readonly" \
  --mount "type=bind,src=${SCRIPT_DIR}/barman.d,dst=/opt/pgdrill/test/barman.d,readonly" \
  --mount "type=bind,src=${OUTPUT_DIR},dst=/output" \
  --env "PGDRILL_EXPECTED_COMMIT=${commit}" \
  --env "PGDRILL_EXPECTED_VERSION=${version}" \
  "${barman_image}" \
  /opt/pgdrill/test/run-in-container.sh 2>&1 | tee "${OUTPUT_DIR}/container.log"
chmod 0750 "${OUTPUT_DIR}"
trap - EXIT INT TERM

pgdrill_integration_finalize_artifacts "${OUTPUT_DIR}" "${CACHE_ROOT}"

log "PASS: artifacts retained at ${OUTPUT_DIR}"
log "inspect from the source checkout with: go run ./cmd/pgdrill report show ${OUTPUT_DIR}/report.json"
