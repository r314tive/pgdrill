#!/usr/bin/env bash

set -Eeuo pipefail

[[ "$#" -eq 0 ]] || {
  printf 'pgBackRest source status does not accept arguments\n' >&2
  exit 2
}
[[ "${EUID}" -eq "$(id -u postgres)" ]] || {
  printf 'pgBackRest source status must run as postgres\n' >&2
  exit 1
}

readonly PGBIN="/usr/lib/postgresql/18/bin"
readonly PGBACKREST="/usr/bin/pgbackrest"
readonly CONFIG="/etc/pgdrill/pgbackrest-source.conf"
readonly PORT="5433"

backup_info="$("${PGBACKREST}" --config="${CONFIG}" --stanza=demo info --output=json)"
row_count="$("${PGBIN}/psql" -h /var/run/postgresql -p "${PORT}" -d postgres -Atqc \
  'SELECT count(*) FROM public.pgdrill_demo_probe;')"
sentinel_count="$("${PGBIN}/psql" -h /var/run/postgresql -p "${PORT}" -d postgres -Atqc \
  "SELECT count(*) FROM public.pgdrill_demo_probe WHERE payload = 'post-backup-wal-sentinel';")"
last_archived_wal="$("${PGBIN}/psql" -h /var/run/postgresql -p "${PORT}" -d postgres -Atqc \
  "SELECT COALESCE(last_archived_wal, '') FROM pg_stat_archiver;")"

jq -n \
  --argjson backups "${backup_info}" \
  --argjson row_count "${row_count}" \
  --argjson sentinel_count "${sentinel_count}" \
  --arg last_archived_wal "${last_archived_wal}" \
  '{
    source_row_count: $row_count,
    post_backup_sentinel_count: $sentinel_count,
    last_archived_wal: $last_archived_wal,
    backups: $backups
  }'
