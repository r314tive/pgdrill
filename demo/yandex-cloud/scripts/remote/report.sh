#!/usr/bin/env bash

set -Eeuo pipefail

[[ "$#" -eq 0 ]] || {
  printf 'demo report does not accept arguments\n' >&2
  exit 2
}
[[ "${EUID}" -eq "$(id -u postgres)" ]] || {
  printf 'demo report must execute as postgres\n' >&2
  exit 1
}

case "$(basename -- "$0")" in
  pgdrill-demo-report | report.sh)
    readonly REPORT="/var/lib/pgdrill-demo/reports/current.json"
    ;;
  pgdrill-demo-pgbackrest-report)
    readonly REPORT="/var/lib/pgdrill-demo/reports/pgbackrest-current.json"
    ;;
  *)
    printf 'unsupported demo report wrapper: %s\n' "$0" >&2
    exit 1
    ;;
esac

[[ -f "${REPORT}" ]] || {
  printf 'no current demo report exists\n' >&2
  exit 1
}

/usr/local/bin/pgdrill report show "${REPORT}"
printf '\nPrometheus projection:\n'
/usr/local/bin/pgdrill report metrics "${REPORT}"
