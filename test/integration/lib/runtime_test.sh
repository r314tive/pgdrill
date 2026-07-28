#!/usr/bin/env bash

set -Eeuo pipefail
umask 077

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR
readonly PGDRILL_INTEGRATION_LOG_PREFIX="integration/runtime-test"

# shellcheck source=test/integration/lib/runtime.sh
source "${SCRIPT_DIR}/runtime.sh"

fail() {
  printf '[integration/runtime-test] FAIL: %s\n' "$*" >&2
  exit 1
}

TEST_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/pgdrill-runtime-test.XXXXXX")"
readonly TEST_ROOT
trap 'rm -rf "${TEST_ROOT}"' EXIT

readonly VERSION="v1.2.3-rc.4"
readonly COMMIT="0123456789abcdef0123456789abcdef01234567"
readonly ARCHIVE_ROOT="pgdrill_1.2.3-rc.4_linux_arm64"
readonly STAGING="${TEST_ROOT}/staging"
readonly ARCHIVE="${TEST_ROOT}/${ARCHIVE_ROOT}.tar.gz"
mkdir -p "${STAGING}/${ARCHIVE_ROOT}"
printf '#!/bin/sh\nprintf "fixture\\n"\n' >"${STAGING}/${ARCHIVE_ROOT}/pgdrill"
chmod 0755 "${STAGING}/${ARCHIVE_ROOT}/pgdrill"
tar -czf "${ARCHIVE}" -C "${STAGING}" "${ARCHIVE_ROOT}/pgdrill"
ARCHIVE_SHA256="$(pgdrill_integration_sha256_file "${ARCHIVE}")"
readonly ARCHIVE_SHA256

target_arch="$(PGDRILL_INTEGRATION_TARGET_ARCH=amd64 pgdrill_integration_target_arch)"
[[ "${target_arch}" == "amd64" ]] ||
  fail "explicit target architecture was not retained"
if (
  export PGDRILL_INTEGRATION_TARGET_ARCH=x86_64
  pgdrill_integration_target_arch
) >/dev/null 2>&1; then
  fail "unsupported target architecture was accepted"
fi

[[ "$(pgdrill_integration_postgres_version)" == "18.3" ]] ||
  fail "unexpected default PostgreSQL version"
[[ "$(PGDRILL_INTEGRATION_POSTGRES_VERSION=17.10 pgdrill_integration_postgres_version)" == "17.10" ]] ||
  fail "explicit PostgreSQL version was not retained"
[[ "$(pgdrill_integration_postgres_major 17.10)" == "17" ]] ||
  fail "unexpected PostgreSQL 17 major"
[[ "$(pgdrill_integration_postgres_major 18.3)" == "18" ]] ||
  fail "unexpected PostgreSQL 18 major"
[[ "$(pgdrill_integration_postgres_source_sha256 17.10)" == \
  "078a03516dcdbdb705fecaf415ea3d13a956c589e46f09fed68a06fb00598c90" ]] ||
  fail "unexpected PostgreSQL 17.10 source digest"
[[ "$(pgdrill_integration_postgres_source_sha256 18.3)" == \
  "d95663fbbf3a80f81a9d98d895266bdcb74ba274bcc04ef6d76630a72dee016f" ]] ||
  fail "unexpected PostgreSQL 18.3 source digest"
[[ "$(pgdrill_integration_postgres_image 17.10 amd64)" == \
  "postgres@sha256:cb875afe6d2e8593c28c22d37d0fd7aaf035c43a42e2f7792cd4c09ceb6beac5" ]] ||
  fail "unexpected linux/amd64 PostgreSQL 17.10 image"
[[ "$(pgdrill_integration_postgres_image 17.10 arm64)" == \
  "postgres@sha256:c274743e5423a554d3ebe3fcf73e489460397f538a1383d191a9c54774b04a49" ]] ||
  fail "unexpected linux/arm64 PostgreSQL 17.10 image"
[[ "$(pgdrill_integration_postgres_image 18.3 amd64)" == \
  "postgres@sha256:a145910d7079e9fbf73e6df19d5fcca0ce59d747cf7d97ac772bff28c3759c32" ]] ||
  fail "unexpected linux/amd64 PostgreSQL 18.3 image"
[[ "$(pgdrill_integration_postgres_image 18.3 arm64)" == \
  "postgres@sha256:0c24d31b13a9801233f136bc80e908bda9577ab7e9c622e572eebc13c186ed4d" ]] ||
  fail "unexpected linux/arm64 PostgreSQL 18.3 image"
[[ "$(pgdrill_integration_postgres_cache_root /cache 18.3)" == "/cache" ]] ||
  fail "default PostgreSQL cache root changed"
[[ "$(pgdrill_integration_postgres_cache_root /cache 17.10)" == "/cache/postgresql-17.10" ]] ||
  fail "alternate PostgreSQL cache root is not isolated"
if (
  export PGDRILL_INTEGRATION_POSTGRES_VERSION=17
  pgdrill_integration_postgres_version
) >/dev/null 2>&1; then
  fail "unsupported PostgreSQL version was accepted"
fi
if (pgdrill_integration_postgres_image 17.10 ppc64le) >/dev/null 2>&1; then
  fail "unsupported PostgreSQL architecture was accepted"
fi

[[ "$(pgdrill_integration_minio_image amd64)" == \
  "quay.io/minio/minio@sha256:3f97c5651cb6662b880c787a232b6b34fec8d8922e08d6617b25d241a21164bb" ]] ||
  fail "unexpected linux/amd64 MinIO image"
[[ "$(pgdrill_integration_minio_image arm64)" == \
  "quay.io/minio/minio@sha256:54d3d6a0a58fb25b4e9943d1db3828d3b4de44666f911381b4fda57175488194" ]] ||
  fail "unexpected linux/arm64 MinIO image"
[[ "$(pgdrill_integration_minio_client_image amd64)" == \
  "quay.io/minio/mc@sha256:2582c2f48b1e31545143ba5285c67d7b38c8b8f6912142d0630686dc7aaac28b" ]] ||
  fail "unexpected linux/amd64 MinIO Client image"
[[ "$(pgdrill_integration_minio_client_image arm64)" == \
  "quay.io/minio/mc@sha256:d798ef4fe8f417b814a8968682c1e172cdfabe59da81b39e4d9cc108a355b271" ]] ||
  fail "unexpected linux/arm64 MinIO Client image"
if (pgdrill_integration_minio_image ppc64le) >/dev/null 2>&1; then
  fail "unsupported MinIO architecture was accepted"
fi

PGDRILL_INT_VERSION="${VERSION}"
PGDRILL_INT_COMMIT="${COMMIT}"
PGDRILL_INT_BUILD_DATE=""
PGDRILL_INT_RUNTIME_DIR="${TEST_ROOT}/runtime"
PGDRILL_INT_BINARY="${PGDRILL_INT_RUNTIME_DIR}/pgdrill"
mkdir -p "${PGDRILL_INT_RUNTIME_DIR}"
pgdrill_integration_use_release_archive \
  "${ARCHIVE}" \
  "${ARCHIVE_SHA256}" \
  linux \
  arm64

[[ "${PGDRILL_INT_BUILD_SOURCE}" == "supplied_release_archive" ]] ||
  fail "successful import did not record supplied_release_archive"
[[ "${PGDRILL_INT_RELEASE_ARCHIVE_SHA256}" == "${ARCHIVE_SHA256}" ]] ||
  fail "successful import did not retain the verified archive digest"
cmp "${STAGING}/${ARCHIVE_ROOT}/pgdrill" "${PGDRILL_INT_BINARY}" >/dev/null ||
  fail "extracted binary differs from the archive member"

if (
  PGDRILL_INT_VERSION="${VERSION}"
  PGDRILL_INT_COMMIT="${COMMIT}"
  PGDRILL_INT_RUNTIME_DIR="${TEST_ROOT}/bad-digest"
  PGDRILL_INT_BINARY="${PGDRILL_INT_RUNTIME_DIR}/pgdrill"
  mkdir -p "${PGDRILL_INT_RUNTIME_DIR}"
  pgdrill_integration_use_release_archive \
    "${ARCHIVE}" \
    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" \
    linux \
    arm64
) >/dev/null 2>&1; then
  fail "checksum mismatch was accepted"
fi

if (
  PGDRILL_INT_VERSION="v1.2.3/unsafe"
  PGDRILL_INT_COMMIT="${COMMIT}"
  PGDRILL_INT_RUNTIME_DIR="${TEST_ROOT}/bad-version"
  PGDRILL_INT_BINARY="${PGDRILL_INT_RUNTIME_DIR}/pgdrill"
  mkdir -p "${PGDRILL_INT_RUNTIME_DIR}"
  pgdrill_integration_use_release_archive \
    "${ARCHIVE}" \
    "${ARCHIVE_SHA256}" \
    linux \
    arm64
) >/dev/null 2>&1; then
  fail "unsafe version was accepted"
fi

if (
  PGDRILL_INT_VERSION="${VERSION}"
  PGDRILL_INT_COMMIT="01234567"
  PGDRILL_INT_RUNTIME_DIR="${TEST_ROOT}/short-commit"
  PGDRILL_INT_BINARY="${PGDRILL_INT_RUNTIME_DIR}/pgdrill"
  mkdir -p "${PGDRILL_INT_RUNTIME_DIR}"
  pgdrill_integration_use_release_archive \
    "${ARCHIVE}" \
    "${ARCHIVE_SHA256}" \
    linux \
    arm64
) >/dev/null 2>&1; then
  fail "short commit was accepted"
fi

readonly RENAMED_ARCHIVE="${TEST_ROOT}/renamed.tar.gz"
cp "${ARCHIVE}" "${RENAMED_ARCHIVE}"
if (
  PGDRILL_INT_VERSION="${VERSION}"
  PGDRILL_INT_COMMIT="${COMMIT}"
  PGDRILL_INT_RUNTIME_DIR="${TEST_ROOT}/bad-name"
  PGDRILL_INT_BINARY="${PGDRILL_INT_RUNTIME_DIR}/pgdrill"
  mkdir -p "${PGDRILL_INT_RUNTIME_DIR}"
  pgdrill_integration_use_release_archive \
    "${RENAMED_ARCHIVE}" \
    "${ARCHIVE_SHA256}" \
    linux \
    arm64
) >/dev/null 2>&1; then
  fail "unexpected archive filename was accepted"
fi

readonly SYMLINK_STAGING="${TEST_ROOT}/symlink-staging"
readonly SYMLINK_ARCHIVE="${TEST_ROOT}/symlink/${ARCHIVE_ROOT}.tar.gz"
mkdir -p "${SYMLINK_STAGING}/${ARCHIVE_ROOT}" "$(dirname -- "${SYMLINK_ARCHIVE}")"
ln -s /bin/sh "${SYMLINK_STAGING}/${ARCHIVE_ROOT}/pgdrill"
tar -czf "${SYMLINK_ARCHIVE}" -C "${SYMLINK_STAGING}" "${ARCHIVE_ROOT}/pgdrill"
SYMLINK_SHA256="$(pgdrill_integration_sha256_file "${SYMLINK_ARCHIVE}")"
readonly SYMLINK_SHA256
if (
  PGDRILL_INT_VERSION="${VERSION}"
  PGDRILL_INT_COMMIT="${COMMIT}"
  PGDRILL_INT_RUNTIME_DIR="${TEST_ROOT}/symlink-runtime"
  PGDRILL_INT_BINARY="${PGDRILL_INT_RUNTIME_DIR}/pgdrill"
  mkdir -p "${PGDRILL_INT_RUNTIME_DIR}"
  pgdrill_integration_use_release_archive \
    "${SYMLINK_ARCHIVE}" \
    "${SYMLINK_SHA256}" \
    linux \
    arm64
) >/dev/null 2>&1; then
  fail "symlink pgdrill payload was accepted"
fi

printf '[integration/runtime-test] PASS\n'
