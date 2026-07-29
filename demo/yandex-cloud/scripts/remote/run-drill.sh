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

stamp="$(date -u +%Y%m%dT%H%M%SZ)"
run_id="${RUN_PREFIX}-${stamp}"
console_log="${REPORT_DIR}/${run_id}.console.log"
archived_report="${REPORT_DIR}/${run_id}.report.json"
run_config="${REPORT_DIR}/${run_id}.config.yaml"

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
  cp "${archived_report}" "${CURRENT_REPORT}"
  sha256sum "${archived_report}" >"${archived_report}.sha256"
  chmod 0640 \
    "${CURRENT_REPORT}" \
    "${archived_report}" \
    "${archived_report}.sha256" \
    "${console_log}" \
    "${run_config}"

  if ! /usr/local/bin/pgdrill report show "${CURRENT_REPORT}"; then
    printf 'pgdrill report validation failed\n' >&2
    if [[ "${status}" -eq 0 ]]; then
      status=1
    fi
  fi
  if [[ "${status}" -eq 0 ]] && ! jq -e '.status == "passed"' "${CURRENT_REPORT}" >/dev/null; then
    printf 'pgdrill report status is not passed\n' >&2
    status=1
  fi
  if [[ "${status}" -eq 0 ]] &&
    ! jq -e --arg provider "${PROVIDER}" '.provider == $provider' "${CURRENT_REPORT}" >/dev/null; then
    printf 'pgdrill report provider does not match %s\n' "${PROVIDER}" >&2
    status=1
  fi
else
  printf 'pgdrill did not persist the required terminal report\n' >&2
  status=1
fi

exit "${status}"
