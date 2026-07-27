#!/usr/bin/env bash

set -Eeuo pipefail
umask 022

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR
ROOT="$(cd -- "${SCRIPT_DIR}/../.." && pwd)"
readonly ROOT
readonly WALG_CACHE="${ROOT}/.cache/integration/walg"

archive=""
archive_sha256=""
commit=""
version=""

usage() {
  cat <<'EOF'
Usage:
  rehearse.sh \
    --archive PATH \
    --archive-sha256 SHA256 \
    --commit FULL_GIT_OBJECT_ID \
    --version VERSION

Runs the real local WAL-G restore drill with an already-published Linux
release archive, then verifies the retained report and artifact checksums.
EOF
}

die() {
  printf '[demo/local] ERROR: %s\n' "$*" >&2
  exit 1
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

verify_checksums() {
  local directory="$1"

  if command -v sha256sum >/dev/null 2>&1; then
    (
      cd "${directory}"
      sha256sum -c checksums.txt
    )
  else
    (
      cd "${directory}"
      shasum -a 256 -c checksums.txt
    )
  fi
}

while [[ "$#" -gt 0 ]]; do
  case "$1" in
    --archive)
      archive="${2:-}"
      shift 2
      ;;
    --archive-sha256)
      archive_sha256="${2:-}"
      shift 2
      ;;
    --commit)
      commit="${2:-}"
      shift 2
      ;;
    --version)
      version="${2:-}"
      shift 2
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    *)
      die "unknown argument: $1"
      ;;
  esac
done

for command in awk docker git grep sed tar; do
  command -v "${command}" >/dev/null 2>&1 ||
    die "required command is missing: ${command}"
done
if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then
  die "required SHA-256 tool is missing: install sha256sum or shasum"
fi
[[ -n "${archive}" && -f "${archive}" ]] ||
  die "a readable --archive is required"
[[ "${archive_sha256}" =~ ^[0-9a-f]{64}$ ]] ||
  die "--archive-sha256 must be a lowercase SHA-256 digest"
[[ "${commit}" =~ ^[0-9a-f]{40,64}$ ]] ||
  die "--commit must be a full lowercase Git object ID"
[[ "${version}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$ ]] ||
  die "--version must be a safe semantic version with a leading v"
[[ "$(sha256_file "${archive}")" == "${archive_sha256}" ]] ||
  die "release archive checksum does not match --archive-sha256"
docker info >/dev/null 2>&1 || die "Docker daemon is unavailable"

archive_dir="$(cd -- "$(dirname -- "${archive}")" && pwd -P)"
archive="${archive_dir}/$(basename -- "${archive}")"

printf '[demo/local] running published-artifact WAL-G rehearsal\n'
env \
  PGDRILL_INTEGRATION_COMMIT="${commit}" \
  PGDRILL_INTEGRATION_RELEASE_ARCHIVE="${archive}" \
  PGDRILL_INTEGRATION_RELEASE_ARCHIVE_SHA256="${archive_sha256}" \
  PGDRILL_INTEGRATION_VERSION="${version}" \
  "${ROOT}/test/integration/walg/run.sh"

latest_run_file="${WALG_CACHE}/latest-run.txt"
[[ -f "${latest_run_file}" ]] ||
  die "integration drill did not record latest-run.txt"
run_dir="$(sed -n '1p' "${latest_run_file}")"
case "${run_dir}" in
  "${WALG_CACHE}/runs/"*) ;;
  *) die "integration drill returned an unexpected artifact directory: ${run_dir}" ;;
esac

for artifact in \
  checksums.txt \
  pitr-config.yaml \
  pitr-report.json \
  pitr-report.txt \
  report.json \
  report.txt \
  runtime.txt \
  source-state.txt; do
  [[ -f "${run_dir}/${artifact}" ]] ||
    die "integration drill did not retain ${artifact}"
done

verify_checksums "${run_dir}" >/dev/null
grep -Eq '^Status[[:space:]]+passed$' "${run_dir}/report.txt" ||
  die "retained report status is not passed"
grep -F "pgdrill ${version} (${commit}," "${run_dir}/report.txt" >/dev/null ||
  die "retained report is not bound to ${version}/${commit}"
grep -Eq '^post_backup_wal_replayed[[:space:]]+sql[[:space:]]+passed' "${run_dir}/report.txt" ||
  die "retained report does not prove post-backup WAL replay"
grep -Eq '^cleanup[[:space:]]+true[[:space:]]+passed' "${run_dir}/report.txt" ||
  die "retained report does not prove owned cleanup"
grep -Eq '^Status[[:space:]]+passed$' "${run_dir}/pitr-report.txt" ||
  die "retained timestamp PITR report status is not passed"
grep -Eq '^Policy[[:space:]]+5 passed, 0 failed, 0 unknown, 0 not configured$' \
  "${run_dir}/pitr-report.txt" ||
  die "retained timestamp PITR policy did not produce five passed verdicts"
grep -Eq '^timestamp_boundary_replayed[[:space:]]+sql[[:space:]]+passed' \
  "${run_dir}/pitr-report.txt" ||
  die "retained timestamp PITR report does not prove the before/after boundary"
grep -Eq '^cleanup[[:space:]]+true[[:space:]]+passed' "${run_dir}/pitr-report.txt" ||
  die "retained timestamp PITR report does not prove owned cleanup"
grep -F '"type": "timestamp"' "${run_dir}/pitr-report.json" >/dev/null ||
  die "retained timestamp PITR report has the wrong recovery target"

printf '\n[demo/local] PASS\n'
printf 'release_archive_sha256=%s\n' "${archive_sha256}"
printf 'latest_report_sha256=%s\n' "$(sha256_file "${run_dir}/report.json")"
printf 'pitr_report_sha256=%s\n' "$(sha256_file "${run_dir}/pitr-report.json")"
printf 'artifacts=%s\n' "${run_dir}"
printf 'latest_report=%s\n' "${run_dir}/report.txt"
printf 'pitr_report=%s\n' "${run_dir}/pitr-report.txt"
