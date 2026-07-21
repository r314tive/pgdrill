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
readonly VERSION_PKG="github.com/r314tive/pgdrill/internal/version"

log() {
  printf '[integration/walg-host] %s\n' "$*"
}

die() {
  printf '[integration/walg-host] ERROR: %s\n' "$*" >&2
  exit 1
}

for command in awk curl docker git go tar; do
  command -v "${command}" >/dev/null 2>&1 || die "required command is missing: ${command}"
done
if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then
  die "required SHA-256 tool is missing: install sha256sum or shasum"
fi
docker info >/dev/null 2>&1 || die "Docker daemon is unavailable"
git -C "${ROOT}" rev-parse --is-inside-work-tree >/dev/null 2>&1 ||
  die "integration test must run from a Git checkout"

docker_arch="$(docker info --format '{{.Architecture}}')"
case "${docker_arch}" in
  amd64 | x86_64)
    arch="amd64"
    walg_asset="wal-g-pg-24.04-amd64"
    walg_sha256="342574292b1907af738d48ff2d1d771ad90a63e441b40a85208022144253f6b8"
    ;;
  arm64 | aarch64)
    arch="arm64"
    walg_asset="wal-g-pg-24.04-aarch64"
    walg_sha256="a822caafa9ee61c2f96add3e768c06971677d8b7a6781e585253b8735a3bc4f7"
    ;;
  *)
    die "unsupported Docker daemon architecture: ${docker_arch}"
    ;;
esac
readonly arch walg_asset walg_sha256

readonly RUNTIME_DIR="${CACHE_ROOT}/runtime/${arch}"
readonly PGDRILL_BINARY="${RUNTIME_DIR}/pgdrill"
readonly WALG_BINARY="${RUNTIME_DIR}/wal-g"
mkdir -p "${RUNTIME_DIR}" "${RUNS_DIR}"

sha256_file() {
  local path="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "${path}" | awk '{print $1}'
  else
    shasum -a 256 "${path}" | awk '{print $1}'
  fi
}

verify_file() {
  local expected="$1"
  local path="$2"
  local actual
  actual="$(sha256_file "${path}")"
  [[ "${actual}" == "${expected}" ]]
}

if [[ ! -f "${WALG_BINARY}" ]] || ! verify_file "${walg_sha256}" "${WALG_BINARY}"; then
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
  verify_file "${walg_sha256}" "${download}" || die "WAL-G checksum verification failed"
  chmod 0755 "${download}"
  mv "${download}" "${WALG_BINARY}"
  trap - EXIT
fi

head_commit="$(git -C "${ROOT}" rev-parse HEAD)"
version="${VERSION_BASE}"
commit="${head_commit}"
build_date="$(git -C "${ROOT}" show -s --format=%cI HEAD)"
dirty_tree=false
if [[ -n "$(git -C "${ROOT}" status --porcelain --untracked-files=normal)" ]]; then
  dirty_tree=true
  commit="${head_commit}-dirty"
  build_date="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  if [[ "${version}" == *-* ]]; then
    version="${version}.dirty"
  else
    version="${version}-dirty"
  fi
fi
readonly version commit build_date dirty_tree

release_archive=""
release_archive_sha256=""
build_source="dirty_source"
if [[ "${dirty_tree}" == "true" ]]; then
  log "building dirty ${version} developer binary for linux/${arch} from ${commit}"
  tmp_binary="$(mktemp "${RUNTIME_DIR}/pgdrill.build.XXXXXX")"
  trap 'rm -f "${tmp_binary:-}"' EXIT
  (
    cd "${ROOT}"
    env \
      -u GOAMD64 \
      -u GOARM64 \
      CGO_ENABLED=0 \
      GOARCH="${arch}" \
      GOENV=off \
      GOEXPERIMENT= \
      GOFLAGS= \
      GOOS=linux \
      GOTOOLCHAIN="go$(sed -n '1p' .go-version)" \
      GOWORK=off \
      go build \
        -mod=readonly \
        -trimpath \
        -buildvcs=false \
        -ldflags "-s -w -buildid= -X ${VERSION_PKG}.Version=${version} -X ${VERSION_PKG}.Commit=${commit} -X ${VERSION_PKG}.Date=${build_date}" \
        -o "${tmp_binary}" \
        ./cmd/pgdrill
  )
  chmod 0755 "${tmp_binary}"
  mv "${tmp_binary}" "${PGDRILL_BINARY}"
  trap - EXIT
else
  build_source="release_archive"
  release_dir="${CACHE_ROOT}/release/${version}/${commit}"
  archive_root="pgdrill_${version#v}_linux_${arch}"
  release_archive="${release_dir}/${archive_root}.tar.gz"
  log "building deterministic ${version} release archive for linux/${arch} from ${commit}"
  (
    cd "${ROOT}"
    GOTOOLCHAIN="go$(sed -n '1p' .go-version)" \
      GOWORK=off \
      go run ./internal/releasecmd artifacts \
        -version "${version}" \
        -commit "${commit}" \
        -date "${build_date}" \
        -output "${release_dir}" \
        -targets "linux/${arch}"
  )
  [[ -f "${release_archive}" ]] || die "release builder did not create ${release_archive}"
  release_archive_sha256="$(sha256_file "${release_archive}")"

  extract_dir="$(mktemp -d "${RUNTIME_DIR}/release.extract.XXXXXX")"
  trap 'rm -rf "${extract_dir:-}"' EXIT
  tar -xzf "${release_archive}" -C "${extract_dir}" "${archive_root}/pgdrill"
  cp "${extract_dir}/${archive_root}/pgdrill" "${PGDRILL_BINARY}"
  chmod 0755 "${PGDRILL_BINARY}"
  rm -rf "${extract_dir}"
  trap - EXIT
fi
readonly release_archive release_archive_sha256 build_source

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
  printf 'docker_arch=%s\n' "${docker_arch}"
  printf 'go=%s\n' "$(go version)"
  printf 'build_source=%s\n' "${build_source}"
  printf 'version=%s\n' "${version}"
  printf 'commit=%s\n' "${commit}"
  printf 'build_date=%s\n' "${build_date}"
  printf 'pgdrill_sha256=%s\n' "$(sha256_file "${PGDRILL_BINARY}")"
  printf 'wal_g_sha256=%s\n' "$(sha256_file "${WALG_BINARY}")"
  if [[ -n "${release_archive}" ]]; then
    printf 'release_archive=%s\n' "${release_archive##*/}"
    printf 'release_archive_sha256=%s\n' "${release_archive_sha256}"
  fi
} >"${OUTPUT_DIR}/runtime.txt"

cleanup_container() {
  docker rm -f "${CONTAINER_NAME}" >/dev/null 2>&1 || true
  chmod 0750 "${OUTPUT_DIR}" >/dev/null 2>&1 || true
}
trap cleanup_container EXIT INT TERM

log "starting network-isolated rootless drill"
docker run \
  --rm \
  --pull never \
  --name "${CONTAINER_NAME}" \
  --platform "linux/${arch}" \
  --network none \
  --user 999:999 \
  --read-only \
  --cap-drop ALL \
  --security-opt no-new-privileges=true \
  --pids-limit 256 \
  --tmpfs /validation:rw,exec,nosuid,nodev,uid=999,gid=999,mode=0700,size=2147483648 \
  --tmpfs /tmp:rw,exec,nosuid,nodev,uid=999,gid=999,mode=1777,size=268435456 \
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

checksums_tmp="$(mktemp "${CACHE_ROOT}/checksums.XXXXXX")"
trap 'rm -f "${checksums_tmp:-}"' EXIT
(
  cd "${OUTPUT_DIR}"
  while IFS= read -r path; do
    printf '%s  %s\n' "$(sha256_file "${path}")" "${path}"
  done < <(find . -type f -print | LC_ALL=C sort)
) >"${checksums_tmp}"
mv "${checksums_tmp}" "${OUTPUT_DIR}/checksums.txt"
trap - EXIT
(
  cd "${OUTPUT_DIR}"
  while read -r expected path; do
    [[ "$(sha256_file "${path}")" == "${expected}" ]] || exit 1
  done <checksums.txt
)
printf '%s\n' "${OUTPUT_DIR}" >"${CACHE_ROOT}/latest-run.txt"

log "PASS: artifacts retained at ${OUTPUT_DIR}"
log "inspect from the source checkout with: go run ./cmd/pgdrill report show ${OUTPUT_DIR}/report.json"
