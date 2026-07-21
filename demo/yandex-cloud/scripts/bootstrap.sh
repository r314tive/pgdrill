#!/usr/bin/env bash

set -Eeuo pipefail
umask 077

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR
DEMO_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
readonly DEMO_DIR
readonly DEFAULT_TERRAFORM_DIR="${DEMO_DIR}/terraform"

archive="${PGDRILL_ARCHIVE:-}"
identity="${SSH_IDENTITY_FILE:-}"
terraform_dir="${TERRAFORM_DIR:-${DEFAULT_TERRAFORM_DIR}}"

usage() {
  cat <<'EOF'
Usage: bootstrap.sh --archive PATH --identity PATH [--terraform-dir PATH]

Installs PostgreSQL 18, pinned WAL-G, and a supplied pgdrill Linux amd64
release archive on an already-applied Terraform demo topology.
EOF
}

while [[ "$#" -gt 0 ]]; do
  case "$1" in
    --archive)
      archive="${2:-}"
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

[[ -n "${archive}" && -f "${archive}" ]] || {
  printf 'a readable --archive is required\n' >&2
  exit 2
}
archive_name="$(basename -- "${archive}")"
[[ "${archive_name}" =~ ^pgdrill_[0-9A-Za-z.+-]+_linux_amd64\.tar\.gz$ ]] || {
  printf 'the demo requires a pgdrill Linux amd64 release archive\n' >&2
  exit 2
}
[[ -n "${identity}" && -f "${identity}" ]] || {
  printf 'a readable private --identity is required\n' >&2
  exit 2
}
[[ -d "${terraform_dir}" ]] || {
  printf 'Terraform directory does not exist: %s\n' "${terraform_dir}" >&2
  exit 2
}

for command in terraform ssh scp; do
  command -v "${command}" >/dev/null || {
    printf 'required local command is missing: %s\n' "${command}" >&2
    exit 1
  }
done

sha256_file() {
  if command -v sha256sum >/dev/null; then
    sha256sum "$1" | awk '{print $1}'
    return
  fi
  if command -v shasum >/dev/null; then
    shasum -a 256 "$1" | awk '{print $1}'
    return
  fi
  printf 'neither sha256sum nor shasum is available\n' >&2
  return 1
}

owner_user="$(terraform -chdir="${terraform_dir}" output -raw owner_user)"
runner_public_ip="$(terraform -chdir="${terraform_dir}" output -raw runner_public_ip)"
source_private_ip="$(terraform -chdir="${terraform_dir}" output -raw source_private_ip)"
repository_private_ip="$(terraform -chdir="${terraform_dir}" output -raw repository_private_ip)"
archive_sha256="$(sha256_file "${archive}")"

state_dir="${DEMO_DIR}/.state"
known_hosts="${state_dir}/known_hosts"
mkdir -p "${state_dir}"
touch "${known_hosts}"
chmod 0600 "${known_hosts}"

runner="${owner_user}@${runner_public_ip}"
source="${owner_user}@${source_private_ip}"
repository="${owner_user}@${repository_private_ip}"
ssh_common=(
  -i "${identity}"
  -o IdentitiesOnly=yes
  -o "UserKnownHostsFile=${known_hosts}"
  -o StrictHostKeyChecking=accept-new
  -o ConnectTimeout=10
)
jump=(-o "ProxyJump=${runner}")

wait_for_ssh() {
  local target="$1"
  shift
  for _ in {1..60}; do
    if ssh "${ssh_common[@]}" "$@" "${target}" true >/dev/null 2>&1; then
      return 0
    fi
    sleep 5
  done
  printf 'SSH did not become ready for %s\n' "${target}" >&2
  return 1
}

printf '[pgdrill-demo] waiting for runner SSH\n'
wait_for_ssh "${runner}"
printf '[pgdrill-demo] waiting for private VM SSH through the runner\n'
wait_for_ssh "${source}" "${jump[@]}"
wait_for_ssh "${repository}" "${jump[@]}"

printf '[pgdrill-demo] waiting for cloud-init on all three VMs\n'
ssh "${ssh_common[@]}" "${runner}" 'cloud-init status --wait'
ssh "${ssh_common[@]}" "${jump[@]}" "${source}" 'cloud-init status --wait'
ssh "${ssh_common[@]}" "${jump[@]}" "${repository}" 'cloud-init status --wait'

remote_stage="/tmp/pgdrill-demo-bootstrap"
ssh "${ssh_common[@]}" "${runner}" 'rm -rf /tmp/pgdrill-demo-bootstrap'
ssh "${ssh_common[@]}" "${jump[@]}" "${source}" 'rm -rf /tmp/pgdrill-demo-bootstrap'
scp "${ssh_common[@]}" -r "${SCRIPT_DIR}/remote" "${runner}:${remote_stage}"
scp "${ssh_common[@]}" "${jump[@]}" -r "${SCRIPT_DIR}/remote" "${source}:${remote_stage}"

printf '[pgdrill-demo] bootstrapping source\n'
ssh "${ssh_common[@]}" "${jump[@]}" "${source}" \
  'sudo /tmp/pgdrill-demo-bootstrap/bootstrap-source.sh'

remote_archive="${remote_stage}/${archive_name}"
scp "${ssh_common[@]}" "${archive}" "${runner}:${remote_archive}"
scp "${ssh_common[@]}" "${DEMO_DIR}/config/pgdrill.yaml" "${runner}:${remote_stage}/pgdrill.yaml"

printf '[pgdrill-demo] bootstrapping runner\n'
# The archive name is restricted above and the digest is lowercase hex.
# shellcheck disable=SC2029
ssh "${ssh_common[@]}" "${runner}" \
  "sudo '${remote_stage}/bootstrap-runner.sh' '${remote_archive}' '${archive_sha256}' '${remote_stage}/pgdrill.yaml'"

printf '[pgdrill-demo] running read-only pgdrill doctor\n'
ssh "${ssh_common[@]}" "${runner}" \
  'sudo -u postgres /usr/local/sbin/pgdrill-demo-doctor'

cat <<EOF

[pgdrill-demo] bootstrap complete
runner:     ${runner_public_ip}
source:     ${source_private_ip} (via runner)
repository: ${repository_private_ip} (via runner)
archive:    ${archive_sha256}

Prepare and exercise the complete scenario with:
  PGDRILL_DEMO_CONFIRM=YES ${SCRIPT_DIR}/scenario.sh --identity ${identity}
EOF
