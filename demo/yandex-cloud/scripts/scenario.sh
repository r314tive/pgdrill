#!/usr/bin/env bash

set -Eeuo pipefail
umask 077

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR
DEMO_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
readonly DEMO_DIR
readonly DEFAULT_TERRAFORM_DIR="${DEMO_DIR}/terraform"

identity="${SSH_IDENTITY_FILE:-}"
terraform_dir="${TERRAFORM_DIR:-${DEFAULT_TERRAFORM_DIR}}"

usage() {
  cat <<'EOF'
Usage: PGDRILL_DEMO_CONFIRM=YES scenario.sh --identity PATH [--terraform-dir PATH]

Resets the marker-guarded disposable repository, creates the source backup and
post-backup WAL sentinel, runs pgdrill, validates the report, and downloads the
evidence into demo/yandex-cloud/.state/reports/.
EOF
}

sha256_files() {
  if command -v sha256sum >/dev/null; then
    sha256sum "$@"
    return
  fi
  if command -v shasum >/dev/null; then
    shasum -a 256 "$@"
    return
  fi
  printf 'neither sha256sum nor shasum is available\n' >&2
  return 1
}

while [[ "$#" -gt 0 ]]; do
  case "$1" in
    --identity)
      identity="${2:-}"
      shift 2
      ;;
    --terraform-dir)
      terraform_dir="${2:-}"
      shift 2
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    *)
      printf 'unknown argument: %s\n' "$1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

[[ "${PGDRILL_DEMO_CONFIRM:-}" == "YES" ]] || {
  printf 'PGDRILL_DEMO_CONFIRM=YES is required because this resets the disposable repository\n' >&2
  exit 2
}
[[ -n "${identity}" && -f "${identity}" ]] || {
  printf 'a readable private --identity is required\n' >&2
  exit 2
}

for command in jq scp ssh terraform; do
  command -v "${command}" >/dev/null || {
    printf 'required local command is missing: %s\n' "${command}" >&2
    exit 1
  }
done

owner_user="$(terraform -chdir="${terraform_dir}" output -raw owner_user)"
runner_public_ip="$(terraform -chdir="${terraform_dir}" output -raw runner_public_ip)"
source_private_ip="$(terraform -chdir="${terraform_dir}" output -raw source_private_ip)"
runner="${owner_user}@${runner_public_ip}"
source="${owner_user}@${source_private_ip}"

state_dir="${DEMO_DIR}/.state"
known_hosts="${state_dir}/known_hosts"
report_dir="${state_dir}/reports"
mkdir -p "${report_dir}"
touch "${known_hosts}"
chmod 0600 "${known_hosts}"

ssh_common=(
  -i "${identity}"
  -o IdentitiesOnly=yes
  -o "UserKnownHostsFile=${known_hosts}"
  -o StrictHostKeyChecking=accept-new
  -o ConnectTimeout=10
)
jump=(-o "ProxyJump=${runner}")

printf '[pgdrill-demo] preparing source backup and post-backup WAL\n'
ssh "${ssh_common[@]}" "${jump[@]}" "${source}" \
  'sudo /usr/local/sbin/pgdrill-demo-prepare-backup --reset'

printf '[pgdrill-demo] source boundary evidence\n'
ssh "${ssh_common[@]}" "${jump[@]}" "${source}" \
  'sudo -u postgres /usr/local/sbin/pgdrill-demo-source-status'

printf '[pgdrill-demo] read-only dependency preflight\n'
ssh "${ssh_common[@]}" "${runner}" \
  'sudo -u postgres /usr/local/sbin/pgdrill-demo-doctor'

printf '[pgdrill-demo] running restore drill\n'
ssh "${ssh_common[@]}" "${runner}" \
  'sudo -u postgres /usr/local/sbin/pgdrill-demo-run'

stamp="$(date -u +%Y%m%dT%H%M%SZ)"
local_report="${report_dir}/${stamp}.report.json"
local_source_state="${report_dir}/${stamp}.source-state.json"
local_runner_inventory="${report_dir}/${stamp}.runner-inventory.json"
local_terraform_inventory="${report_dir}/${stamp}.terraform-inventory.json"
scp "${ssh_common[@]}" \
  "${runner}:/var/lib/pgdrill-demo/reports/current.json" \
  "${local_report}"
scp "${ssh_common[@]}" "${jump[@]}" \
  "${source}:/var/lib/pgdrill-demo/source-state.json" \
  "${local_source_state}"
scp "${ssh_common[@]}" \
  "${runner}:/var/lib/pgdrill-demo/runner-inventory.json" \
  "${local_runner_inventory}"
terraform -chdir="${terraform_dir}" output -json demo_inventory \
  >"${local_terraform_inventory}"

backup_name="$(jq -er '.backup_name' "${local_source_state}")"
jq -e \
  --arg backup_name "${backup_name}" '
  .schema_version == "pgdrill.report/v1alpha1" and
  .status == "passed" and
  .backup.provider == "wal-g" and
  .backup.provider_id == $backup_name and
  ([.checks[] | select(.name == "post_backup_wal_replayed" and .status == "passed")] | length) == 1 and
  ([.policy_evaluation.verdicts[] | select(.required == true and .status != "passed")] | length) == 0
' "${local_report}" >/dev/null
jq -e '
  .schema_version == "pgdrill.demo-source-state/v1alpha1" and
  .provider == "wal-g" and
  .base_backup_row_count == 100 and
  .expected_recovered_row_count == 101 and
  .post_backup_wal_sentinel == "post-backup-wal-sentinel"
' "${local_source_state}" >/dev/null
jq -e '
  .schema_version == "pgdrill.demo-runner-inventory/v1alpha1" and
  .repository_mode == "read_only" and
  .postgres_uid == 2000 and
  .postgres_gid == 2000 and
  .pgdg_key_fingerprint == "B97B0AFCAA1A47F044F244A07FCC7D46ACCC4CF8" and
  (.pgdrill_archive_sha256 | test("^[0-9a-f]{64}$"))
' "${local_runner_inventory}" >/dev/null
jq -e '
  .runner_public_ip != "" and
  .runner_private_ip != "" and
  .source_ip != "" and
  .repository_ip != "" and
  (.preemptible | type) == "boolean"
' "${local_terraform_inventory}" >/dev/null

printf '[pgdrill-demo] terminal report and policy gates passed\n'
printf 'report:              %s\n' "${local_report}"
printf 'source state:         %s\n' "${local_source_state}"
printf 'runner inventory:     %s\n' "${local_runner_inventory}"
printf 'Terraform inventory:  %s\n' "${local_terraform_inventory}"
sha256_files \
  "${local_report}" \
  "${local_source_state}" \
  "${local_runner_inventory}" \
  "${local_terraform_inventory}"
