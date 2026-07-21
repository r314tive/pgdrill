#!/usr/bin/env bash

set -Eeuo pipefail

[[ "$#" -eq 0 ]] || {
  printf 'demo doctor does not accept arguments\n' >&2
  exit 2
}
[[ "${EUID}" -eq "$(id -u postgres)" ]] || {
  printf 'demo doctor must execute as postgres\n' >&2
  exit 1
}

exec /usr/local/bin/pgdrill doctor -f /etc/pgdrill/demo.yaml
