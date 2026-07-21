#!/usr/bin/env bash

set -Eeuo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR
# shellcheck source=demo/yandex-cloud/scripts/remote/lib.sh
source "${SCRIPT_DIR}/lib.sh"

require_root
require_no_args "$@"

readonly SOURCE_DATA="/var/lib/pgdrill-demo/source-data"

log "installing PostgreSQL ${PG_MAJOR} and WAL-G ${WALG_VERSION} on the source"
install_postgresql
remove_default_postgresql_cluster
install_walg
mount_repository rw

printf '%s\n' 'pgdrill-demo-repository/v1' |
  runuser -u postgres -- tee "${REPOSITORY_MOUNT}/.pgdrill-demo-repository" >/dev/null
runuser -u postgres -- chmod 0640 "${REPOSITORY_MOUNT}/.pgdrill-demo-repository"
runuser -u postgres -- install -d -m 0750 "${WALG_REPOSITORY}"

install -d -o root -g root -m 0755 /etc/pgdrill /usr/local/libexec
cat >/etc/pgdrill/wal-g.env <<EOF
WALG_FILE_PREFIX=${WALG_REPOSITORY}
WALG_COMPRESSION_METHOD=lz4
EOF
chmod 0644 /etc/pgdrill/wal-g.env

cat >/usr/local/libexec/pgdrill-wal-push <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
set -a
# shellcheck disable=SC1091
source /etc/pgdrill/wal-g.env
set +a
exec /usr/local/bin/wal-g wal-push "$1"
EOF
chmod 0755 /usr/local/libexec/pgdrill-wal-push

install -d -o postgres -g postgres -m 0700 "${SOURCE_DATA}"
if [[ ! -s "${SOURCE_DATA}/PG_VERSION" ]]; then
  runuser -u postgres -- "${PGBIN}/initdb" \
    --pgdata "${SOURCE_DATA}" \
    --auth-local peer \
    --auth-host scram-sha-256 \
    --encoding UTF8 \
    --locale C.UTF-8 \
    --data-checksums
fi

cat >"${SOURCE_DATA}/pgdrill-demo.conf" <<'EOF'
listen_addresses = '127.0.0.1'
port = 5432
unix_socket_directories = '/var/run/postgresql'
archive_mode = on
archive_command = '/usr/local/libexec/pgdrill-wal-push "%p"'
archive_timeout = '60s'
wal_level = replica
shared_buffers = '256MB'
log_min_messages = info
EOF
chown postgres:postgres "${SOURCE_DATA}/pgdrill-demo.conf"
chmod 0600 "${SOURCE_DATA}/pgdrill-demo.conf"
if ! grep -qF "include = 'pgdrill-demo.conf'" "${SOURCE_DATA}/postgresql.conf"; then
  printf "\ninclude = 'pgdrill-demo.conf'\n" >>"${SOURCE_DATA}/postgresql.conf"
fi

cat >/etc/systemd/system/pgdrill-demo-source.service <<EOF
[Unit]
Description=pgdrill disposable PostgreSQL source
After=network-online.target remote-fs.target
Wants=network-online.target
RequiresMountsFor=${REPOSITORY_MOUNT}

[Service]
Type=simple
User=postgres
Group=postgres
ExecStart=${PGBIN}/postgres -D ${SOURCE_DATA}
ExecReload=/bin/kill -HUP \$MAINPID
KillSignal=SIGINT
TimeoutStopSec=120
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true
ProtectSystem=full
ReadWritePaths=${SOURCE_DATA} ${REPOSITORY_MOUNT} /var/run/postgresql /var/lib/pgdrill-demo

[Install]
WantedBy=multi-user.target
EOF

install -o root -g root -m 0755 \
  "${SCRIPT_DIR}/prepare-backup.sh" \
  /usr/local/sbin/pgdrill-demo-prepare-backup
install -o root -g root -m 0755 \
  "${SCRIPT_DIR}/source-status.sh" \
  /usr/local/sbin/pgdrill-demo-source-status

cat >/etc/sudoers.d/pgdrill-demo-source <<'EOF'
%pgdrill-demo-admins ALL=(postgres) NOPASSWD: /usr/local/sbin/pgdrill-demo-source-status
EOF
chmod 0440 /etc/sudoers.d/pgdrill-demo-source
visudo --check --file=/etc/sudoers.d/pgdrill-demo-source >/dev/null

systemctl daemon-reload
systemctl enable pgdrill-demo-source.service
systemctl restart pgdrill-demo-source.service
wait_for_postgres

log "source bootstrap complete"
systemctl --no-pager --full status pgdrill-demo-source.service | sed -n '1,12p'
