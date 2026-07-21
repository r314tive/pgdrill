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

readonly REPORT="/var/lib/pgdrill-demo/reports/current.json"
[[ -f "${REPORT}" ]] || {
  printf 'no current demo report exists\n' >&2
  exit 1
}

/usr/local/bin/pgdrill report show "${REPORT}"
printf '\nPrometheus projection:\n'
/usr/local/bin/pgdrill report metrics "${REPORT}"
