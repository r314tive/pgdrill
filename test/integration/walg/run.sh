#!/usr/bin/env bash

set -Eeuo pipefail
umask 022

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR
ROOT="$(cd -- "${SCRIPT_DIR}/../../.." && pwd)"
readonly ROOT
readonly STORAGE_MODE="${PGDRILL_INTEGRATION_WALG_STORAGE:-file}"
case "${STORAGE_MODE}" in
  file)
    cache_default="${ROOT}/.cache/integration/walg"
    log_prefix="integration/walg-host"
    storage_backend="file"
    ;;
  s3)
    cache_default="${ROOT}/.cache/integration/walg-s3"
    log_prefix="integration/walg-s3-host"
    storage_backend="s3-compatible"
    ;;
  *)
    printf 'unsupported PGDRILL_INTEGRATION_WALG_STORAGE: %s; expected file or s3\n' \
      "${STORAGE_MODE}" >&2
    exit 1
    ;;
esac
readonly storage_backend
readonly CACHE_ROOT="${PGDRILL_INTEGRATION_CACHE:-${cache_default}}"
readonly RUNS_DIR="${CACHE_ROOT}/runs"
readonly WALG_VERSION="3.0.8"
readonly MINIO_VERSION="RELEASE.2025-04-22T22-12-26Z"
readonly MINIO_CLIENT_VERSION="RELEASE.2025-04-16T18-13-26Z"
readonly VERSION_BASE="${PGDRILL_INTEGRATION_VERSION:-v0.0.0-integration}"
readonly PGDRILL_INTEGRATION_LOG_PREFIX="${log_prefix}"

# shellcheck source=test/integration/lib/runtime.sh
source "${SCRIPT_DIR}/../lib/runtime.sh"

log() {
  pgdrill_integration_log "$@"
}

die() {
  pgdrill_integration_die "$@"
}

pgdrill_integration_require_commands awk curl docker git tar
pgdrill_integration_require_source_build_commands
docker info >/dev/null 2>&1 || die "Docker daemon is unavailable"
git -C "${ROOT}" rev-parse --is-inside-work-tree >/dev/null 2>&1 ||
  die "integration test must run from a Git checkout"

docker_arch="$(docker info --format '{{.Architecture}}')"
arch="$(pgdrill_integration_target_arch)"
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
postgres_image_default="$(pgdrill_integration_postgres_18_3_image "${arch}")"
readonly postgres_image_default
readonly POSTGRES_IMAGE="${PGDRILL_INTEGRATION_POSTGRES_IMAGE:-${postgres_image_default}}"
MINIO_IMAGE=""
MINIO_CLIENT_IMAGE=""
if [[ "${STORAGE_MODE}" == "s3" ]]; then
  MINIO_IMAGE="$(pgdrill_integration_minio_image "${arch}")"
  MINIO_CLIENT_IMAGE="$(pgdrill_integration_minio_client_image "${arch}")"
fi
readonly MINIO_IMAGE MINIO_CLIENT_IMAGE

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
readonly MINIO_CONTAINER_NAME="pgdrill-walg-minio-${run_stamp}"
readonly NETWORK_NAME="pgdrill-walg-s3-${run_stamp}"
readonly MINIO_ALIAS="pgdrill"
readonly MINIO_BUCKET="pgdrill-walg"
readonly MINIO_ACCESS_KEY="pgdrill-integration"
readonly MINIO_SECRET_KEY="pgdrill-${run_stamp}-object-storage"
mkdir -p "${OUTPUT_DIR}"
chmod 0777 "${OUTPUT_DIR}"

pgdrill_integration_ensure_image_platform "${POSTGRES_IMAGE}" "${arch}" "PostgreSQL 18.3"
image_id="$(docker image inspect --format '{{.Id}}' "${POSTGRES_IMAGE}")"
image_arch="$(docker image inspect --format '{{.Architecture}}' "${POSTGRES_IMAGE}")"
minio_image_id=""
minio_image_arch=""
minio_image_version=""
minio_client_image_id=""
minio_client_image_arch=""
minio_client_image_version=""
if [[ "${STORAGE_MODE}" == "s3" ]]; then
  pgdrill_integration_ensure_image_platform "${MINIO_IMAGE}" "${arch}" "MinIO"
  pgdrill_integration_ensure_image_platform "${MINIO_CLIENT_IMAGE}" "${arch}" "MinIO Client"
  minio_image_id="$(docker image inspect --format '{{.Id}}' "${MINIO_IMAGE}")"
  minio_image_arch="$(docker image inspect --format '{{.Architecture}}' "${MINIO_IMAGE}")"
  minio_image_version="${MINIO_VERSION}"
  minio_client_image_id="$(docker image inspect --format '{{.Id}}' "${MINIO_CLIENT_IMAGE}")"
  minio_client_image_arch="$(docker image inspect --format '{{.Architecture}}' "${MINIO_CLIENT_IMAGE}")"
  minio_client_image_version="${MINIO_CLIENT_VERSION}"
fi

{
  printf 'container_image=%s\n' "${POSTGRES_IMAGE}"
  printf 'container_image_id=%s\n' "${image_id}"
  printf 'container_image_architecture=%s\n' "${image_arch}"
  pgdrill_integration_print_runtime_inventory "${docker_arch}"
  printf 'wal_g_sha256=%s\n' "$(pgdrill_integration_sha256_file "${WALG_BINARY}")"
  printf 'storage_backend=%s\n' "${storage_backend}"
  if [[ "${STORAGE_MODE}" == "s3" ]]; then
    printf 'storage_network=internal\n'
    printf 'storage_endpoint=http://minio:9000\n'
    printf 'storage_bucket=%s\n' "${MINIO_BUCKET}"
    printf 'minio_image=%s\n' "${MINIO_IMAGE}"
    printf 'minio_image_id=%s\n' "${minio_image_id}"
    printf 'minio_image_architecture=%s\n' "${minio_image_arch}"
    printf 'minio_image_version=%s\n' "${minio_image_version}"
    printf 'minio_client_image=%s\n' "${MINIO_CLIENT_IMAGE}"
    printf 'minio_client_image_id=%s\n' "${minio_client_image_id}"
    printf 'minio_client_image_architecture=%s\n' "${minio_client_image_arch}"
    printf 'minio_client_image_version=%s\n' "${minio_client_image_version}"
  else
    printf 'storage_network=none\n'
  fi
} >"${OUTPUT_DIR}/runtime.txt"

cleanup_runtime() {
  docker rm -f "${CONTAINER_NAME}" >/dev/null 2>&1 || true
  if [[ "${STORAGE_MODE}" == "s3" ]]; then
    docker rm -f "${MINIO_CONTAINER_NAME}" >/dev/null 2>&1 || true
    docker network rm "${NETWORK_NAME}" >/dev/null 2>&1 || true
  fi
  chmod 0750 "${OUTPUT_DIR}" >/dev/null 2>&1 || true
}
trap cleanup_runtime EXIT INT TERM

run_minio_client() {
  local timeout_seconds="$1"
  shift

  docker run \
    --rm \
    --pull never \
    --platform "linux/${arch}" \
    --network "${NETWORK_NAME}" \
    --user 1000:1000 \
    --read-only \
    --cap-drop ALL \
    --security-opt no-new-privileges=true \
    --pids-limit 64 \
    --tmpfs /tmp:rw,exec,nosuid,nodev,uid=1000,gid=1000,mode=0700,size=67108864 \
    --env HOME=/tmp \
    --env MC_CONFIG_DIR=/tmp/.mc \
    --env "MC_HOST_${MINIO_ALIAS}=http://${MINIO_ACCESS_KEY}:${MINIO_SECRET_KEY}@minio:9000" \
    --entrypoint /usr/bin/timeout \
    "${MINIO_CLIENT_IMAGE}" \
    "${timeout_seconds}" \
    /usr/bin/mc \
    "$@"
}

drill_network="none"
drill_env=(--env "PGDRILL_WALG_STORAGE=${STORAGE_MODE}")
if [[ "${STORAGE_MODE}" == "s3" ]]; then
  log "starting isolated S3-compatible object storage"
  docker network create \
    --internal \
    --label "org.pgdrill.integration=walg-s3" \
    "${NETWORK_NAME}" >/dev/null
  docker run \
    --detach \
    --pull never \
    --name "${MINIO_CONTAINER_NAME}" \
    --platform "linux/${arch}" \
    --network "${NETWORK_NAME}" \
    --network-alias minio \
    --user 1000:1000 \
    --read-only \
    --cap-drop ALL \
    --security-opt no-new-privileges=true \
    --pids-limit 128 \
    --tmpfs /data:rw,nosuid,nodev,uid=1000,gid=1000,mode=0700,size=2147483648 \
    --tmpfs /tmp:rw,exec,nosuid,nodev,uid=1000,gid=1000,mode=0700,size=67108864 \
    --env HOME=/tmp \
    --env MINIO_BROWSER=off \
    --env "MINIO_ROOT_USER=${MINIO_ACCESS_KEY}" \
    --env "MINIO_ROOT_PASSWORD=${MINIO_SECRET_KEY}" \
    --entrypoint /usr/bin/minio \
    "${MINIO_IMAGE}" \
    server /data --address :9000 >/dev/null

  ready=false
  for _ in $(seq 1 30); do
    if run_minio_client 3 ready --quiet "${MINIO_ALIAS}" >"${OUTPUT_DIR}/minio-ready.log" 2>&1; then
      ready=true
      break
    fi
    if ! docker container inspect "${MINIO_CONTAINER_NAME}" >/dev/null 2>&1; then
      docker logs "${MINIO_CONTAINER_NAME}" >&2 || true
      die "MinIO exited before becoming ready"
    fi
    sleep 1
  done
  [[ "${ready}" == "true" ]] || die "MinIO did not become ready within 30 seconds"
  run_minio_client 10 mb --ignore-existing "${MINIO_ALIAS}/${MINIO_BUCKET}" \
    >"${OUTPUT_DIR}/minio-bucket-create.log"
  run_minio_client 10 stat --json "${MINIO_ALIAS}/${MINIO_BUCKET}" \
    >"${OUTPUT_DIR}/minio-bucket.json"

  drill_network="${NETWORK_NAME}"
  drill_env+=(
    --env "AWS_ACCESS_KEY_ID=${MINIO_ACCESS_KEY}"
    --env "AWS_SECRET_ACCESS_KEY=${MINIO_SECRET_KEY}"
  )
fi
readonly drill_network

log "starting rootless ${STORAGE_MODE} storage drill"
pgdrill_integration_docker_run_on_network \
  "${CONTAINER_NAME}" \
  "${arch}" \
  2147483648 \
  "${drill_network}" \
  --mount "type=bind,src=${PGDRILL_BINARY},dst=/opt/pgdrill/bin/pgdrill,readonly" \
  --mount "type=bind,src=${WALG_BINARY},dst=/opt/pgdrill/bin/wal-g,readonly" \
  --mount "type=bind,src=${SCRIPT_DIR}/run-in-container.sh,dst=/opt/pgdrill/test/run-in-container.sh,readonly" \
  --mount "type=bind,src=${SCRIPT_DIR}/../lib/history.sh,dst=/opt/pgdrill/test/history.sh,readonly" \
  --mount "type=bind,src=${SCRIPT_DIR}/pgdrill.yaml,dst=/opt/pgdrill/test/pgdrill.yaml,readonly" \
  --mount "type=bind,src=${SCRIPT_DIR}/pgdrill-pitr.yaml.tmpl,dst=/opt/pgdrill/test/pgdrill-pitr.yaml.tmpl,readonly" \
  --mount "type=bind,src=${SCRIPT_DIR}/pgdrill-s3.yaml,dst=/opt/pgdrill/test/pgdrill-s3.yaml,readonly" \
  --mount "type=bind,src=${SCRIPT_DIR}/pgdrill-s3-pitr.yaml.tmpl,dst=/opt/pgdrill/test/pgdrill-s3-pitr.yaml.tmpl,readonly" \
  --mount "type=bind,src=${OUTPUT_DIR},dst=/output" \
  --env "PGDRILL_EXPECTED_COMMIT=${commit}" \
  --env "PGDRILL_EXPECTED_VERSION=${version}" \
  "${drill_env[@]}" \
  "${POSTGRES_IMAGE}" \
  /opt/pgdrill/test/run-in-container.sh 2>&1 | tee "${OUTPUT_DIR}/container.log"

if [[ "${STORAGE_MODE}" == "s3" ]]; then
  run_minio_client 30 ls --recursive --json "${MINIO_ALIAS}/${MINIO_BUCKET}" \
    >"${OUTPUT_DIR}/object-storage.jsonl"
  [[ -s "${OUTPUT_DIR}/object-storage.jsonl" ]] ||
    die "S3-compatible bucket contains no retained WAL-G objects"
  grep -F '"key":"integration/basebackups_005/' \
    "${OUTPUT_DIR}/object-storage.jsonl" >/dev/null ||
    die "S3-compatible bucket contains no WAL-G base backup objects"
  grep -F '"key":"integration/wal_005/' \
    "${OUTPUT_DIR}/object-storage.jsonl" >/dev/null ||
    die "S3-compatible bucket contains no archived WAL objects"
  run_minio_client 30 du --json "${MINIO_ALIAS}/${MINIO_BUCKET}" \
    >"${OUTPUT_DIR}/object-storage-summary.json"
  if grep -R -F -q -- "${MINIO_ACCESS_KEY}" "${OUTPUT_DIR}" ||
    grep -R -F -q -- "${MINIO_SECRET_KEY}" "${OUTPUT_DIR}"; then
    die "S3 credentials leaked into retained integration artifacts"
  fi
fi

cleanup_runtime
if docker container inspect "${CONTAINER_NAME}" >/dev/null 2>&1; then
  die "WAL-G drill container remains after cleanup"
fi
if [[ "${STORAGE_MODE}" == "s3" ]]; then
  if docker container inspect "${MINIO_CONTAINER_NAME}" >/dev/null 2>&1; then
    die "MinIO container remains after cleanup"
  fi
  if docker network inspect "${NETWORK_NAME}" >/dev/null 2>&1; then
    die "S3 integration network remains after cleanup"
  fi
fi
chmod 0750 "${OUTPUT_DIR}"
trap - EXIT INT TERM

pgdrill_integration_finalize_artifacts "${OUTPUT_DIR}" "${CACHE_ROOT}"

log "PASS: artifacts retained at ${OUTPUT_DIR}"
log "inspect from the source checkout with: go run ./cmd/pgdrill report show ${OUTPUT_DIR}/report.json"
