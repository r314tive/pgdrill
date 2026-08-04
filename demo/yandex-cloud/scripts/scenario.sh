#!/usr/bin/env bash

set -Eeuo pipefail
umask 077

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR
DEMO_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
readonly DEMO_DIR
readonly DEFAULT_TERRAFORM_DIR="${DEMO_DIR}/terraform"

identity="${SSH_IDENTITY_FILE:-}"
provider="wal-g"
terraform_dir="${TERRAFORM_DIR:-${DEFAULT_TERRAFORM_DIR}}"

usage() {
  cat <<'EOF'
Usage: PGDRILL_DEMO_CONFIRM=YES scenario.sh [--provider wal-g|pgbackrest] --identity PATH [--terraform-dir PATH]

Resets only the selected provider's marker-guarded disposable repository,
creates the source backup and post-backup WAL sentinel, runs pgdrill, validates
the report, and downloads evidence into demo/yandex-cloud/.state/reports/.
EOF
}

require_option_argument() {
  [[ "$#" -ge 2 ]] || {
    printf '%s requires a value\n' "$1" >&2
    usage >&2
    exit 2
  }
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
    --provider)
      require_option_argument "$@"
      provider="$2"
      shift 2
      ;;
    --identity)
      require_option_argument "$@"
      identity="$2"
      shift 2
      ;;
    --terraform-dir)
      require_option_argument "$@"
      terraform_dir="$2"
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

case "${provider}" in
  wal-g)
    source_prepare="/usr/local/sbin/pgdrill-demo-prepare-backup"
    source_status="/usr/local/sbin/pgdrill-demo-source-status"
    runner_doctor="/usr/local/sbin/pgdrill-demo-doctor"
    runner_run="/usr/local/sbin/pgdrill-demo-run"
    remote_source_state="/var/lib/pgdrill-demo/source-state.json"
    local_suffix=""
    required_provider_checks=("wal-g-wal-verify-integrity")
    ;;
  pgbackrest)
    source_prepare="/usr/local/sbin/pgdrill-demo-pgbackrest-prepare-backup"
    source_status="/usr/local/sbin/pgdrill-demo-pgbackrest-source-status"
    runner_doctor="/usr/local/sbin/pgdrill-demo-pgbackrest-doctor"
    runner_run="/usr/local/sbin/pgdrill-demo-pgbackrest-run"
    remote_source_state="/var/lib/pgdrill-demo/pgbackrest-source-state.json"
    local_suffix=".pgbackrest"
    required_provider_checks=("pgbackrest-verify")
    ;;
  *)
    printf '%s\n' '--provider must be wal-g or pgbackrest' >&2
    exit 2
    ;;
esac
readonly provider source_prepare source_status runner_doctor runner_run
readonly remote_source_state local_suffix
readonly -a required_provider_checks

[[ "${PGDRILL_DEMO_CONFIRM:-}" == "YES" ]] || {
  printf 'PGDRILL_DEMO_CONFIRM=YES is required because this resets the disposable repository\n' >&2
  exit 2
}
[[ -n "${identity}" && -f "${identity}" ]] || {
  printf 'a readable private --identity is required\n' >&2
  exit 2
}

for command in jq scp ssh tee terraform; do
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
  -o BatchMode=yes
  -o IdentitiesOnly=yes
  -o "UserKnownHostsFile=${known_hosts}"
  -o StrictHostKeyChecking=accept-new
  -o ConnectTimeout=10
)
printf -v proxy_command \
  'ssh -i %q -o BatchMode=yes -o IdentitiesOnly=yes -o UserKnownHostsFile=%q -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 -W %%h:%%p %q' \
  "${identity}" "${known_hosts}" "${runner}"
jump=(-o "ProxyCommand=${proxy_command}")

stamp="$(date -u +%Y%m%dT%H%M%SZ)-$$"
local_report="${report_dir}/${stamp}${local_suffix}.report.json"
local_source_state="${report_dir}/${stamp}${local_suffix}.source-state.json"
local_source_log="${report_dir}/${stamp}${local_suffix}.source-prepare.log"
local_runner_log="${report_dir}/${stamp}${local_suffix}.runner-console.log"
local_runner_session="${report_dir}/${stamp}${local_suffix}.runner-session.log"
local_runner_inventory="${report_dir}/${stamp}${local_suffix}.runner-inventory.json"
local_terraform_inventory="${report_dir}/${stamp}${local_suffix}.terraform-inventory.json"

printf '[pgdrill-demo/%s] preparing source backup and post-backup WAL\n' "${provider}"
# Command paths are selected only from the closed provider mapping above.
set +e
# shellcheck disable=SC2029
ssh "${ssh_common[@]}" "${jump[@]}" "${source}" \
  "sudo '${source_prepare}' --reset" 2>&1 | tee "${local_source_log}"
source_prepare_status="${PIPESTATUS[0]}"
set -e
if [[ "${source_prepare_status}" -ne 0 ]]; then
  printf '[pgdrill-demo/%s] source preparation failed; log retained at %s\n' \
    "${provider}" "${local_source_log}" >&2
  exit "${source_prepare_status}"
fi

printf '[pgdrill-demo/%s] source boundary evidence\n' "${provider}"
# shellcheck disable=SC2029
ssh "${ssh_common[@]}" "${jump[@]}" "${source}" \
  "sudo -u postgres '${source_status}'"

printf '[pgdrill-demo/%s] read-only dependency preflight\n' "${provider}"
# shellcheck disable=SC2029
ssh "${ssh_common[@]}" "${runner}" \
  "sudo -u postgres '${runner_doctor}'"

printf '[pgdrill-demo/%s] running restore drill\n' "${provider}"
set +e
# shellcheck disable=SC2029
ssh "${ssh_common[@]}" "${runner}" \
  "sudo -u postgres '${runner_run}'" 2>&1 | tee "${local_runner_session}"
run_status="${PIPESTATUS[0]}"
set -e

run_id="$(awk -F= '/^PGDRILL_DEMO_RUN_ID=/ { print substr($0, index($0, "=") + 1); exit }' "${local_runner_session}")"
evidence_status=0
downloaded_evidence=("${local_source_log}" "${local_runner_session}")
if [[ ! "${run_id}" =~ ^[0-9A-Za-z._-]+$ ]]; then
  printf 'runner did not return one safe run-specific evidence identity\n' >&2
  evidence_status=1
else
  remote_run_report="/var/lib/pgdrill-demo/reports/${run_id}.report.json"
  remote_run_log="/var/lib/pgdrill-demo/reports/${run_id}.console.log"
  if scp "${ssh_common[@]}" "${runner}:${remote_run_report}" "${local_report}"; then
    downloaded_evidence+=("${local_report}")
  else
    printf 'could not download run-specific report %s\n' "${remote_run_report}" >&2
    evidence_status=1
  fi
  if scp "${ssh_common[@]}" "${runner}:${remote_run_log}" "${local_runner_log}"; then
    downloaded_evidence+=("${local_runner_log}")
  else
    printf 'could not download run-specific console log %s\n' "${remote_run_log}" >&2
    evidence_status=1
  fi
fi

# shellcheck disable=SC2029
if scp "${ssh_common[@]}" "${jump[@]}" \
  "${source}:${remote_source_state}" \
  "${local_source_state}"; then
  downloaded_evidence+=("${local_source_state}")
else
  printf 'could not download source state %s\n' "${remote_source_state}" >&2
  evidence_status=1
fi
if scp "${ssh_common[@]}" \
  "${runner}:/var/lib/pgdrill-demo/runner-inventory.json" \
  "${local_runner_inventory}"; then
  downloaded_evidence+=("${local_runner_inventory}")
else
  printf 'could not download runner inventory\n' >&2
  evidence_status=1
fi
if terraform -chdir="${terraform_dir}" output -json demo_inventory \
  >"${local_terraform_inventory}"; then
  downloaded_evidence+=("${local_terraform_inventory}")
else
  rm -f -- "${local_terraform_inventory}"
  printf 'could not render Terraform inventory\n' >&2
  evidence_status=1
fi

if [[ "${run_status}" -ne 0 || "${evidence_status}" -ne 0 ]]; then
  printf '[pgdrill-demo/%s] restore drill failed; evidence retained\n' "${provider}" >&2
  printf 'retained evidence files:\n' >&2
  printf '  %s\n' "${downloaded_evidence[@]}" >&2
  sha256_files "${downloaded_evidence[@]}"
  if [[ "${run_status}" -ne 0 ]]; then
    exit "${run_status}"
  fi
  exit 1
fi

jq -e --arg run_id "${run_id}" '.id == $run_id' "${local_report}" >/dev/null || {
  printf 'terminal report identity does not match wrapper run id %s\n' "${run_id}" >&2
  exit 1
}

backup_name="$(jq -er '.backup_name' "${local_source_state}")"
jq -e \
  --arg backup_name "${backup_name}" \
  --arg provider "${provider}" '
  .schema_version == "pgdrill.report/v2" and
  .status == "passed" and
  .backup.provider == $provider and
  .backup.provider_id == $backup_name and
  ([.checks[] | select(.name == "post_backup_wal_replayed" and .status == "passed")] | length) == 1 and
  ([.policy_evaluation.verdicts[] | select(.required == true and .status != "passed")] | length) == 0
' "${local_report}" >/dev/null
jq -e --arg provider "${provider}" '
  .schema_version == "pgdrill.demo-source-state/v1alpha1" and
  .provider == $provider and
  .base_backup_row_count == 100 and
  .expected_recovered_row_count == 101 and
  .post_backup_wal_sentinel == "post-backup-wal-sentinel"
' "${local_source_state}" >/dev/null
for check in "${required_provider_checks[@]}"; do
  jq -e --arg check "${check}" \
    '([.checks[] | select(.name == $check and .status == "passed")] | length) == 1' \
    "${local_report}" >/dev/null ||
    {
      printf 'required provider check did not pass: %s\n' "${check}" >&2
      exit 1
    }
done
jq -e '
  .schema_version == "pgdrill.demo-runner-inventory/v1alpha1" and
  .repository_mode == "read_only" and
  .postgres_uid == 2000 and
  .postgres_gid == 2000 and
  .pgdg_key_fingerprint == "B97B0AFCAA1A47F044F244A07FCC7D46ACCC4CF8" and
  (.pgdrill_archive_sha256 | test("^[0-9a-f]{64}$"))
' "${local_runner_inventory}" >/dev/null
if [[ "${provider}" == "pgbackrest" ]]; then
  jq -e '
    .native_validation.pgbackrest_check_before_backup == "passed" and
    .native_validation.pgbackrest_check_after_backup_wal == "passed" and
    .native_validation.pgbackrest_verify_selected_backup == "passed"
  ' "${local_source_state}" >/dev/null
  jq -e '
    ([.checks[] |
      select(.name == "pgbackrest-check" and .status == "skipped")
    ] | length) == 1
  ' "${local_report}" >/dev/null
  jq -e '.pgbackrest_version == "pgBackRest 2.58.0"' \
    "${local_runner_inventory}" >/dev/null
fi
jq -e '
  .runner_public_ip != "" and
  .runner_private_ip != "" and
  .source_ip != "" and
  .repository_ip != "" and
  (.preemptible | type) == "boolean"
' "${local_terraform_inventory}" >/dev/null

printf '[pgdrill-demo/%s] terminal report and policy gates passed\n' "${provider}"
printf 'report:              %s\n' "${local_report}"
printf 'source state:         %s\n' "${local_source_state}"
printf 'source prepare log:   %s\n' "${local_source_log}"
printf 'runner console log:   %s\n' "${local_runner_log}"
printf 'runner session log:   %s\n' "${local_runner_session}"
printf 'runner inventory:     %s\n' "${local_runner_inventory}"
printf 'Terraform inventory:  %s\n' "${local_terraform_inventory}"
sha256_files \
  "${local_report}" \
  "${local_source_state}" \
  "${local_source_log}" \
  "${local_runner_log}" \
  "${local_runner_session}" \
  "${local_runner_inventory}" \
  "${local_terraform_inventory}"
