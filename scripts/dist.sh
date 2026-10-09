#!/usr/bin/env bash
# Cross-compiles the CGO-free binaries (braid-server and the browser-mode
# braid app) for every supported platform and archives each into dist/.
# The UI must already be built into frontend/dist (make frontend).
set -euo pipefail

version=${VERSION:?set VERSION, e.g. VERSION=v0.1.0}
out=$(mkdir -p "${DIST:-dist}" && cd "${DIST:-dist}" && pwd)
targets=${TARGETS:-"linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64"}
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# zipdir ARCHIVE DIR, run from DIR's parent. Falls back to Python where zip isn't installed.
zipdir() {
  if command -v zip >/dev/null; then zip -qr "$1" "$2"; else python3 -m zipfile -c "$1" "$2"; fi
}

for target in $targets; do
  os=${target%/*} arch=${target#*/}
  ext=""
  [ "$os" = windows ] && ext=.exe
  for cmd in braid-server braid; do
    name="${cmd}_${version}_${os}_${arch}"
    mkdir -p "$work/$name"
    CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$work/$name/$cmd$ext" "./cmd/$cmd"
    cp LICENSE README.md "$work/$name/"
    if [ "$os" = windows ]; then
      (cd "$work" && zipdir "$out/$name.zip" "$name")
    else
      tar -C "$work" -czf "$out/$name.tar.gz" "$name"
    fi
    echo "dist: $name"
  done
done
