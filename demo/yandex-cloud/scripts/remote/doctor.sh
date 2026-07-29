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

case "$(basename -- "$0")" in
  pgdrill-demo-doctor | doctor.sh)
    readonly CONFIG="/etc/pgdrill/demo.yaml"
    ;;
  pgdrill-demo-pgbackrest-doctor)
    readonly CONFIG="/etc/pgdrill/pgbackrest.yaml"
    ;;
  *)
    printf 'unsupported demo doctor wrapper: %s\n' "$0" >&2
    exit 1
    ;;
esac

exec /usr/local/bin/pgdrill doctor -f "${CONFIG}"
