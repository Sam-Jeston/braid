#!/usr/bin/env bash
# Installs or updates braid-server as a systemd service. Run it as root on the
# target machine (scripts/deploy-server.sh uploads and runs it for you):
#
#   sudo ./install.sh ./braid-server [./braid-server.service]
#
# Each run stops the service, snapshots the database into backups/ next to it
# (keeping the last 5), swaps the binary (the old one stays as
# braid-server.prev) and starts the service again. If the new binary fails its
# health check, the previous one is put back.
set -euo pipefail

bin=${1:?usage: install.sh BINARY [UNIT_FILE]}
unit=${2:-}
dest=/usr/local/bin/braid-server
unit_dest=/etc/systemd/system/braid-server.service
keep_backups=5

[ "$(id -u)" = 0 ] || { echo "install.sh must run as root, e.g. with sudo." >&2; exit 1; }

# setting NAME DEFAULT: the value the service sees, honouring /etc/default/braid-server.
setting() {
  (
    [ -f /etc/default/braid-server ] && . /etc/default/braid-server
    printf '%s\n' "${!1:-$2}"
  )
}

# put SRC: replace the installed binary without writing into a running executable.
put() {
  install -m 0755 "$1" "$dest.new"
  mv -f "$dest.new" "$dest"
}

# get URL: succeeds when URL answers 2xx.
get() {
  if command -v curl >/dev/null; then
    curl -fsk --max-time 2 -o /dev/null "$1"
  else
    wget -q -T 2 --no-check-certificate -O /dev/null "$1"
  fi
}

# Refuse a binary built for another architecture before touching the service.
new_version=$("$bin" version) || { echo "$bin doesn't run on this machine. Build it for $(uname -m)." >&2; exit 1; }

db=$(setting BRAID_DB /var/lib/braid/braid.db)
addr=$(setting BRAID_ADDR 127.0.0.1:8080)
case $addr in
  :*) addr=127.0.0.1$addr ;;
  0.0.0.0:*) addr=127.0.0.1:${addr#*:} ;;
esac
scheme=http
[ -n "$(setting BRAID_TLS_CERT "")" ] && scheme=https
url=$scheme://$addr/healthz

id braid >/dev/null 2>&1 || useradd --system --user-group --home-dir /var/lib/braid --no-create-home --shell /usr/sbin/nologin braid

if [ -n "$unit" ] && ! cmp -s "$unit" "$unit_dest"; then
  install -m 0644 "$unit" "$unit_dest"
  systemctl daemon-reload
fi
[ -f "$unit_dest" ] || { echo "No braid-server unit is installed yet. Pass the unit file as the second argument." >&2; exit 1; }

systemctl stop braid-server 2>/dev/null || true

snap=
if [ -f "$db" ]; then
  backups=$(dirname "$db")/backups
  snap=$backups/$(date +%Y%m%d-%H%M%S)
  mkdir -p "$snap"
  # The service is stopped, so the files are consistent; -wal/-shm only exist after an unclean stop.
  for f in "$db" "$db-wal" "$db-shm"; do
    if [ -f "$f" ]; then cp -p "$f" "$snap/"; fi
  done
  chown -R braid:braid "$backups"
  ls -1d "$backups"/*/ | head -n -"$keep_backups" | xargs -r rm -rf --
  echo "Database snapshot: $snap"
fi

if [ -f "$dest" ]; then cp -p "$dest" "$dest.prev"; fi
put "$bin"
systemctl enable --quiet braid-server
systemctl start braid-server

for _ in $(seq 20); do
  sleep 1
  if get "$url"; then
    echo "$new_version is running ($url)."
    echo "Logs: journalctl -u braid-server -f"
    echo "New team setup code: sudo -u braid $dest setup-code --db $db"
    exit 0
  fi
done

echo "$new_version failed its health check at $url." >&2
journalctl -u braid-server -n 20 --no-pager >&2 || true
if [ -f "$dest.prev" ]; then
  put "$dest.prev"
  systemctl restart braid-server
  echo "Restored the previous binary ($("$dest" version))." >&2
  if [ -n "$snap" ]; then echo "If it can't open the database, stop the service and copy $snap back to $(dirname "$db")." >&2; fi
fi
exit 1
