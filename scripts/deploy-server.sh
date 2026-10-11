#!/usr/bin/env bash
# Builds braid-server for a Linux machine and installs or updates it there as
# a systemd service (packaging/linux), over ssh. The ssh user needs sudo.
#
#   scripts/deploy-server.sh user@host    (or: make deploy-server DEPLOY_HOST=user@host)
set -euo pipefail

host=${1:?usage: scripts/deploy-server.sh user@host}
cd "$(dirname "$0")/.."
version=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}

case $(ssh "$host" uname -m) in
  x86_64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) echo "deploy-server.sh: $host isn't amd64 or arm64." >&2; exit 1 ;;
esac

work=$(mktemp -d)
remote=
trap 'rm -rf "$work"; if [ -n "$remote" ]; then ssh "$host" rm -rf "$remote"; fi' EXIT

echo "Building braid-server $version for linux/$arch"
CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$work/braid-server" ./cmd/braid-server

remote=$(ssh "$host" mktemp -d)
scp -q "$work/braid-server" packaging/linux/braid-server.service packaging/linux/install.sh "$host:$remote/"
# A terminal lets sudo ask for a password.
tty=
[ -t 0 ] && tty=-t
ssh $tty "$host" sudo bash "$remote/install.sh" "$remote/braid-server" "$remote/braid-server.service"
