#!/usr/bin/env bash

set -Eeuo pipefail
umask 027

[[ "$#" -eq 0 ]] || {
  printf 'demo run does not accept arguments\n' >&2
  exit 2
}
[[ "${EUID}" -eq "$(id -u postgres)" ]] || {
  printf 'demo run must execute as postgres\n' >&2
  exit 1
}

case "$(basename -- "$0")" in
  pgdrill-demo-run | run-drill.sh)
    readonly PROVIDER="wal-g"
    readonly RUN_PREFIX="yc-walg-demo"
    readonly CONFIG="/etc/pgdrill/demo.yaml"
    readonly CURRENT_REPORT="/var/lib/pgdrill-demo/reports/current.json"
    readonly WORK_DIR="/var/lib/pgdrill-demo/work/restore"
    ;;
  pgdrill-demo-pgbackrest-run)
    readonly PROVIDER="pgbackrest"
    readonly RUN_PREFIX="yc-pgbackrest-demo"
    readonly CONFIG="/etc/pgdrill/pgbackrest.yaml"
    readonly CURRENT_REPORT="/var/lib/pgdrill-demo/reports/pgbackrest-current.json"
    readonly WORK_DIR="/var/lib/pgdrill-demo/work/pgbackrest-restore"
    ;;
  *)
    printf 'unsupported demo run wrapper: %s\n' "$0" >&2
    exit 1
    ;;
esac

readonly REPORT_DIR="/var/lib/pgdrill-demo/reports"
export PGPASSFILE="/etc/pgdrill/pgpass"

exec 9>"${REPORT_DIR}/.run.lock"
flock --nonblock 9 || {
  printf 'another pgdrill demo run is active\n' >&2
  exit 1
}

if [[ -e "${WORK_DIR}" ]]; then
  printf 'owned restore work directory still exists: %s\n' "${WORK_DIR}" >&2
  printf 'inspect its operation checkpoints before an owner removes it\n' >&2
  exit 1
fi

stamp="$(date -u +%Y%m%dT%H%M%SZ)-$$"
run_id="${RUN_PREFIX}-${stamp}"
console_log="${REPORT_DIR}/${run_id}.console.log"
archived_report="${REPORT_DIR}/${run_id}.report.json"
run_config="${REPORT_DIR}/${run_id}.config.yaml"

for path in "${console_log}" "${archived_report}" "${archived_report}.sha256" "${run_config}"; do
  [[ ! -e "${path}" ]] || {
    printf 'refusing to replace existing run evidence: %s\n' "${path}" >&2
    exit 1
  }
done
printf 'PGDRILL_DEMO_RUN_ID=%s\n' "${run_id}"

promote_current_report() {
  local temporary
  temporary="$(mktemp "${REPORT_DIR}/.${RUN_PREFIX}.current.XXXXXX")"
  if ! cp -- "${archived_report}" "${temporary}"; then
    rm -f -- "${temporary}"
    return 1
  fi
  if ! chmod 0640 "${temporary}"; then
    rm -f -- "${temporary}"
    return 1
  fi
  mv -f -- "${temporary}" "${CURRENT_REPORT}"
}

sed \
  "s#path: ${CURRENT_REPORT}#path: ${archived_report}#" \
  "${CONFIG}" >"${run_config}"
grep -qF "path: ${archived_report}" "${run_config}" || {
  printf 'could not bind the run-specific report path\n' >&2
  exit 1
}

set +e
/usr/local/bin/pgdrill run \
  -f "${run_config}" \
  -run-id "${run_id}" \
  -attempt-id attempt-1 2>&1 | tee "${console_log}"
status="${PIPESTATUS[0]}"
set -e

if [[ -f "${archived_report}" ]]; then
  sha256sum "${archived_report}" >"${archived_report}.sha256"
  chmod 0640 \
    "${archived_report}" \
    "${archived_report}.sha256" \
    "${console_log}" \
    "${run_config}"

  if ! /usr/local/bin/pgdrill report show "${archived_report}"; then
    printf 'pgdrill report validation failed\n' >&2
    status=1
  elif ! jq -e \
    --arg provider "${PROVIDER}" \
    --arg run_id "${run_id}" \
    '.provider == $provider and .id == $run_id' \
    "${archived_report}" >/dev/null; then
    printf 'pgdrill report identity does not match provider %s and run %s\n' \
      "${PROVIDER}" "${run_id}" >&2
    status=1
  elif ! promote_current_report; then
    printf 'could not atomically publish the current demo report\n' >&2
    status=1
  fi
  if [[ "${status}" -eq 0 ]] && ! jq -e '.status == "passed"' "${archived_report}" >/dev/null; then
    printf 'pgdrill report status is not passed\n' >&2
    status=1
  fi
else
  printf 'pgdrill did not persist the required terminal report\n' >&2
  status=1
fi

exit "${status}"
