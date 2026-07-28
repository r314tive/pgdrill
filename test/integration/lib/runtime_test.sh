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
