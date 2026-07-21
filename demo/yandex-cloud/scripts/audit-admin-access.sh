#!/usr/bin/env bash

set -Eeuo pipefail
umask 077

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR
DEMO_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
readonly DEMO_DIR
readonly DEFAULT_TERRAFORM_DIR="${DEMO_DIR}/terraform"

admin=""
identity="${SSH_IDENTITY_FILE:-}"
terraform_dir="${TERRAFORM_DIR:-${DEFAULT_TERRAFORM_DIR}}"

usage() {
  cat <<'EOF'
Usage: audit-admin-access.sh --admin LOGIN --identity PATH [--terraform-dir PATH]

After a successful scenario run, verifies one invited administrator's SSH,
fixed-command sudo, direct repository/report restrictions, and repository VM
denial without changing the demo environment.
EOF
}

die() {
  printf '[pgdrill-demo] ERROR: %s\n' "$*" >&2
  exit 1
}

while [[ "$#" -gt 0 ]]; do
  case "$1" in
    --admin)
      admin="${2:-}"
      shift 2
      ;;
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

[[ "${admin}" =~ ^[a-z][a-z0-9-]{0,30}$ ]] || die "a safe --admin login is required"
[[ -n "${identity}" && -f "${identity}" ]] || die "a readable private --identity is required"
[[ -d "${terraform_dir}" ]] || die "Terraform directory does not exist: ${terraform_dir}"

for command in jq ssh terraform; do
  command -v "${command}" >/dev/null || die "required local command is missing: ${command}"
done

terraform -chdir="${terraform_dir}" output -json admin_users |
  jq -e --arg admin "${admin}" 'index($admin) != null' >/dev/null ||
  die "administrator ${admin} is not present in Terraform state"

runner_public_ip="$(terraform -chdir="${terraform_dir}" output -raw runner_public_ip)"
source_private_ip="$(terraform -chdir="${terraform_dir}" output -raw source_private_ip)"
repository_private_ip="$(terraform -chdir="${terraform_dir}" output -raw repository_private_ip)"
runner="${admin}@${runner_public_ip}"
source="${admin}@${source_private_ip}"
repository="${admin}@${repository_private_ip}"

state_dir="${DEMO_DIR}/.state"
known_hosts="${state_dir}/known_hosts"
mkdir -p "${state_dir}"
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
jump=(-o "ProxyJump=${runner}")

printf '[pgdrill-demo] checking invited runner login and fixed commands\n'
ssh "${ssh_common[@]}" "${runner}" \
  'test "$(id -un)" != root && test ! -w /var/lib/pgdrill-demo/reports && test ! -r /mnt/pgdrill-repository/.pgdrill-demo-repository'
if ssh "${ssh_common[@]}" "${runner}" 'sudo -n /usr/bin/true' >/dev/null 2>&1; then
  die "administrator unexpectedly has general sudo on the runner"
fi
ssh "${ssh_common[@]}" "${runner}" \
  'sudo -n -u postgres /usr/local/sbin/pgdrill-demo-doctor >/dev/null'
ssh "${ssh_common[@]}" "${runner}" \
  'sudo -n -u postgres /usr/local/sbin/pgdrill-demo-report >/dev/null'
ssh "${ssh_common[@]}" "${runner}" \
  'sudo -n -l | grep -Fq /usr/local/sbin/pgdrill-demo-run'

printf '[pgdrill-demo] checking invited source login and fixed command\n'
ssh "${ssh_common[@]}" "${jump[@]}" "${source}" \
  'test "$(id -un)" != root && test ! -r /mnt/pgdrill-repository/.pgdrill-demo-repository'
if ssh "${ssh_common[@]}" "${jump[@]}" "${source}" \
  'sudo -n /usr/bin/true' >/dev/null 2>&1; then
  die "administrator unexpectedly has general sudo on the source"
fi
ssh "${ssh_common[@]}" "${jump[@]}" "${source}" \
  'sudo -n -u postgres /usr/local/sbin/pgdrill-demo-source-status >/dev/null'

printf '[pgdrill-demo] checking owner-only repository login\n'
if ssh "${ssh_common[@]}" "${jump[@]}" "${repository}" true >/dev/null 2>&1; then
  die "administrator unexpectedly has SSH access to the repository"
fi

printf '[pgdrill-demo] invited administrator access audit passed: %s\n' "${admin}"
