#!/usr/bin/env bash

# Shared host-side mechanics for disposable native-provider drills. Provider
# setup and assertions remain in each scenario.

readonly PGDRILL_INTEGRATION_VERSION_PKG="github.com/r314tive/pgdrill/internal/version"

pgdrill_integration_log() {
  printf '[%s] %s\n' "${PGDRILL_INTEGRATION_LOG_PREFIX:?integration log prefix is required}" "$*"
}

pgdrill_integration_die() {
  printf '[%s] ERROR: %s\n' "${PGDRILL_INTEGRATION_LOG_PREFIX:?integration log prefix is required}" "$*" >&2
  exit 1
}

pgdrill_integration_require_commands() {
  local command
  for command in "$@"; do
    command -v "${command}" >/dev/null 2>&1 ||
      pgdrill_integration_die "required command is missing: ${command}"
  done
  if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then
    pgdrill_integration_die "required SHA-256 tool is missing: install sha256sum or shasum"
  fi
}

pgdrill_integration_sha256_file() {
  local path="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "${path}" | awk '{print $1}'
  else
    shasum -a 256 "${path}" | awk '{print $1}'
  fi
}

pgdrill_integration_verify_file() {
  local expected="$1"
  local path="$2"
  local actual
  actual="$(pgdrill_integration_sha256_file "${path}")"
  [[ "${actual}" == "${expected}" ]]
}

pgdrill_integration_docker_arch() {
  local docker_arch
  docker_arch="$(docker info --format '{{.Architecture}}')"
  case "${docker_arch}" in
    amd64 | x86_64)
      printf 'amd64\n'
      ;;
    arm64 | aarch64)
      printf 'arm64\n'
      ;;
    *)
      pgdrill_integration_die "unsupported Docker daemon architecture: ${docker_arch}"
      ;;
  esac
}

pgdrill_integration_prepare_pgdrill() {
  local root="$1"
  local cache_root="$2"
  local target_arch="$3"
  local version_base="$4"
  local head_commit

  PGDRILL_INT_RUNTIME_DIR="${cache_root}/runtime/${target_arch}"
  PGDRILL_INT_BINARY="${PGDRILL_INT_RUNTIME_DIR}/pgdrill"
  mkdir -p "${PGDRILL_INT_RUNTIME_DIR}" "${cache_root}/runs"

  head_commit="$(git -C "${root}" rev-parse HEAD)"
  PGDRILL_INT_VERSION="${version_base}"
  PGDRILL_INT_COMMIT="${head_commit}"
  PGDRILL_INT_BUILD_DATE="$(git -C "${root}" show -s --format=%cI HEAD)"
  PGDRILL_INT_DIRTY_TREE=false
  if [[ -n "$(git -C "${root}" status --porcelain --untracked-files=normal)" ]]; then
    PGDRILL_INT_DIRTY_TREE=true
    PGDRILL_INT_COMMIT="${head_commit}-dirty"
    PGDRILL_INT_BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    if [[ "${PGDRILL_INT_VERSION}" == *-* ]]; then
      PGDRILL_INT_VERSION="${PGDRILL_INT_VERSION}.dirty"
    else
      PGDRILL_INT_VERSION="${PGDRILL_INT_VERSION}-dirty"
    fi
  fi

  PGDRILL_INT_RELEASE_ARCHIVE=""
  PGDRILL_INT_RELEASE_ARCHIVE_SHA256=""
  PGDRILL_INT_BUILD_SOURCE="dirty_source"
  if [[ "${PGDRILL_INT_DIRTY_TREE}" == "true" ]]; then
    pgdrill_integration_build_dirty_binary "${root}" "${target_arch}"
  else
    pgdrill_integration_build_release_binary "${root}" "${cache_root}" "${target_arch}"
  fi
}

pgdrill_integration_build_dirty_binary() {
  local root="$1"
  local target_arch="$2"
  local tmp_binary

  pgdrill_integration_log "building dirty ${PGDRILL_INT_VERSION} developer binary for linux/${target_arch} from ${PGDRILL_INT_COMMIT}"
  tmp_binary="$(mktemp "${PGDRILL_INT_RUNTIME_DIR}/pgdrill.build.XXXXXX")"
  if ! (
    cd "${root}"
    env \
      -u GOAMD64 \
      -u GOARM64 \
      CGO_ENABLED=0 \
      GOARCH="${target_arch}" \
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
        -ldflags "-s -w -buildid= -X ${PGDRILL_INTEGRATION_VERSION_PKG}.Version=${PGDRILL_INT_VERSION} -X ${PGDRILL_INTEGRATION_VERSION_PKG}.Commit=${PGDRILL_INT_COMMIT} -X ${PGDRILL_INTEGRATION_VERSION_PKG}.Date=${PGDRILL_INT_BUILD_DATE}" \
        -o "${tmp_binary}" \
        ./cmd/pgdrill
  ); then
    rm -f "${tmp_binary}"
    pgdrill_integration_die "dirty pgdrill build failed"
  fi
  chmod 0755 "${tmp_binary}"
  mv "${tmp_binary}" "${PGDRILL_INT_BINARY}"
}

pgdrill_integration_build_release_binary() {
  local root="$1"
  local cache_root="$2"
  local target_arch="$3"
  local release_dir archive_root extract_dir

  PGDRILL_INT_BUILD_SOURCE="release_archive"
  release_dir="${cache_root}/release/${PGDRILL_INT_VERSION}/${PGDRILL_INT_COMMIT}"
  archive_root="pgdrill_${PGDRILL_INT_VERSION#v}_linux_${target_arch}"
  PGDRILL_INT_RELEASE_ARCHIVE="${release_dir}/${archive_root}.tar.gz"
  pgdrill_integration_log "building deterministic ${PGDRILL_INT_VERSION} release archive for linux/${target_arch} from ${PGDRILL_INT_COMMIT}"
  if ! (
    cd "${root}"
    GOTOOLCHAIN="go$(sed -n '1p' .go-version)" \
      GOWORK=off \
      go run ./internal/releasecmd artifacts \
        -version "${PGDRILL_INT_VERSION}" \
        -commit "${PGDRILL_INT_COMMIT}" \
        -date "${PGDRILL_INT_BUILD_DATE}" \
        -output "${release_dir}" \
        -targets "linux/${target_arch}"
  ); then
    pgdrill_integration_die "release archive build failed"
  fi
  [[ -f "${PGDRILL_INT_RELEASE_ARCHIVE}" ]] ||
    pgdrill_integration_die "release builder did not create ${PGDRILL_INT_RELEASE_ARCHIVE}"
  PGDRILL_INT_RELEASE_ARCHIVE_SHA256="$(pgdrill_integration_sha256_file "${PGDRILL_INT_RELEASE_ARCHIVE}")"

  extract_dir="$(mktemp -d "${PGDRILL_INT_RUNTIME_DIR}/release.extract.XXXXXX")"
  if ! tar -xzf "${PGDRILL_INT_RELEASE_ARCHIVE}" -C "${extract_dir}" "${archive_root}/pgdrill"; then
    rm -rf "${extract_dir}"
    pgdrill_integration_die "extract release binary from ${PGDRILL_INT_RELEASE_ARCHIVE}"
  fi
  cp "${extract_dir}/${archive_root}/pgdrill" "${PGDRILL_INT_BINARY}"
  chmod 0755 "${PGDRILL_INT_BINARY}"
  rm -rf "${extract_dir}"
}

pgdrill_integration_print_runtime_inventory() {
  local runtime_docker_arch="$1"

  printf 'docker_arch=%s\n' "${runtime_docker_arch}"
  printf 'go=%s\n' "$(go version)"
  printf 'build_source=%s\n' "${PGDRILL_INT_BUILD_SOURCE}"
  printf 'version=%s\n' "${PGDRILL_INT_VERSION}"
  printf 'commit=%s\n' "${PGDRILL_INT_COMMIT}"
  printf 'build_date=%s\n' "${PGDRILL_INT_BUILD_DATE}"
  printf 'pgdrill_sha256=%s\n' "$(pgdrill_integration_sha256_file "${PGDRILL_INT_BINARY}")"
  if [[ -n "${PGDRILL_INT_RELEASE_ARCHIVE}" ]]; then
    printf 'release_archive=%s\n' "${PGDRILL_INT_RELEASE_ARCHIVE##*/}"
    printf 'release_archive_sha256=%s\n' "${PGDRILL_INT_RELEASE_ARCHIVE_SHA256}"
  fi
}

pgdrill_integration_prepare_image() {
  local explicit_image="$1"
  local default_image="$2"
  local runtime_name="$3"
  local postgres_version="$4"
  local base_image="$5"
  local dockerfile="$6"
  local build_context="$7"
  local target_arch="$8"
  local cached_definition

  PGDRILL_INT_IMAGE_DEFINITION_SHA256="$(pgdrill_integration_sha256_file "${dockerfile}")"
  PGDRILL_INT_IMAGE_SOURCE="pinned_build"
  if [[ -n "${explicit_image}" ]]; then
    PGDRILL_INT_CONTAINER_IMAGE="${explicit_image}"
    PGDRILL_INT_IMAGE_SOURCE="explicit_override"
    docker image inspect "${PGDRILL_INT_CONTAINER_IMAGE}" >/dev/null 2>&1 ||
      pgdrill_integration_die "explicit ${runtime_name} image is not present locally: ${PGDRILL_INT_CONTAINER_IMAGE}"
  else
    PGDRILL_INT_CONTAINER_IMAGE="${default_image}"
    cached_definition="$(docker image inspect --format '{{ index .Config.Labels "org.pgdrill.integration.definition-sha" }}' "${PGDRILL_INT_CONTAINER_IMAGE}" 2>/dev/null || true)"
    if [[ "${cached_definition}" != "${PGDRILL_INT_IMAGE_DEFINITION_SHA256}" ]]; then
      if docker image inspect "${base_image}" >/dev/null 2>&1; then
        pgdrill_integration_log "using cached immutable PostgreSQL ${postgres_version} base image for linux/${target_arch}"
      else
        pgdrill_integration_log "pulling immutable PostgreSQL ${postgres_version} base image for linux/${target_arch}"
        docker pull --platform "linux/${target_arch}" "${base_image}" >/dev/null
      fi
      pgdrill_integration_log "building pinned ${runtime_name} runtime for linux/${target_arch}"
      docker build \
        --pull=false \
        --platform "linux/${target_arch}" \
        --build-arg "PGDRILL_INTEGRATION_DEFINITION_SHA=${PGDRILL_INT_IMAGE_DEFINITION_SHA256}" \
        --tag "${PGDRILL_INT_CONTAINER_IMAGE}" \
        --file "${dockerfile}" \
        "${build_context}"
    else
      pgdrill_integration_log "using cached pinned ${runtime_name} runtime for linux/${target_arch}"
    fi
  fi
  PGDRILL_INT_CONTAINER_IMAGE_ID="$(docker image inspect --format '{{.Id}}' "${PGDRILL_INT_CONTAINER_IMAGE}")"
  PGDRILL_INT_IMAGE_DEFINITION_LABEL="$(docker image inspect --format '{{ index .Config.Labels "org.pgdrill.integration.definition-sha" }}' "${PGDRILL_INT_CONTAINER_IMAGE}")"
  if [[ "${PGDRILL_INT_IMAGE_SOURCE}" == "pinned_build" && "${PGDRILL_INT_IMAGE_DEFINITION_LABEL}" != "${PGDRILL_INT_IMAGE_DEFINITION_SHA256}" ]]; then
    pgdrill_integration_die "built ${runtime_name} image definition label does not match ${PGDRILL_INT_IMAGE_DEFINITION_SHA256}"
  fi
}

pgdrill_integration_print_image_inventory() {
  local base_image="$1"

  printf 'container_image=%s\n' "${PGDRILL_INT_CONTAINER_IMAGE}"
  printf 'container_image_id=%s\n' "${PGDRILL_INT_CONTAINER_IMAGE_ID}"
  printf 'container_image_source=%s\n' "${PGDRILL_INT_IMAGE_SOURCE}"
  printf 'container_expected_definition_sha256=%s\n' "${PGDRILL_INT_IMAGE_DEFINITION_SHA256}"
  printf 'container_image_definition_sha256=%s\n' "${PGDRILL_INT_IMAGE_DEFINITION_LABEL}"
  printf 'container_base_image=%s\n' "${base_image}"
}

pgdrill_integration_docker_run() {
  local container_name="$1"
  local target_arch="$2"
  local validation_size="$3"
  shift 3

  docker run \
    --rm \
    --pull never \
    --name "${container_name}" \
    --platform "linux/${target_arch}" \
    --network none \
    --user 999:999 \
    --read-only \
    --cap-drop ALL \
    --security-opt no-new-privileges=true \
    --pids-limit 256 \
    --tmpfs "/validation:rw,exec,nosuid,nodev,uid=999,gid=999,mode=0700,size=${validation_size}" \
    --tmpfs /tmp:rw,exec,nosuid,nodev,uid=999,gid=999,mode=1777,size=268435456 \
    "$@"
}

pgdrill_integration_finalize_artifacts() {
  local output_dir="$1"
  local cache_root="$2"
  local checksums_tmp latest_tmp symlink

  symlink="$(find "${output_dir}" -type l -print -quit)"
  [[ -z "${symlink}" ]] || pgdrill_integration_die "artifact tree contains a symlink: ${symlink}"

  checksums_tmp="$(mktemp "${cache_root}/checksums.XXXXXX")"
  if ! (
    cd "${output_dir}"
    while IFS= read -r path; do
      printf '%s  %s\n' "$(pgdrill_integration_sha256_file "${path}")" "${path}"
    done < <(find . -type f ! -name checksums.txt -print | LC_ALL=C sort)
  ) >"${checksums_tmp}"; then
    rm -f "${checksums_tmp}"
    pgdrill_integration_die "calculate integration artifact checksums"
  fi
  mv "${checksums_tmp}" "${output_dir}/checksums.txt"

  (
    cd "${output_dir}"
    while IFS= read -r line; do
      expected="${line%%  *}"
      path="${line#*  }"
      [[ "$(pgdrill_integration_sha256_file "${path}")" == "${expected}" ]] || exit 1
    done <checksums.txt
  ) || pgdrill_integration_die "verify integration artifact checksums"

  latest_tmp="$(mktemp "${cache_root}/latest-run.XXXXXX")"
  printf '%s\n' "${output_dir}" >"${latest_tmp}"
  mv "${latest_tmp}" "${cache_root}/latest-run.txt"
}
