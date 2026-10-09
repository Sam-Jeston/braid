#!/usr/bin/env bash
# Builds Braid.app as a universal binary (Apple silicon + Intel), signs it,
# and notarizes it when Apple credentials are set. Run on macOS with Xcode's
# command line tools. The UI must already be built (make frontend).
#
#   MACOS_SIGN_IDENTITY  e.g. "Developer ID Application: Jane Doe (TEAMID)".
#                        Unset: an ad-hoc signature, which runs locally but is
#                        quarantined by Gatekeeper when downloaded.
#   APPLE_ID, APPLE_TEAM_ID, APPLE_APP_PASSWORD
#                        notarize with notarytool (needs a real identity).
set -euo pipefail

version=${VERSION:?set VERSION, e.g. VERSION=v0.1.0}
out=$(mkdir -p "${DIST:-dist}" && cd "${DIST:-dist}" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# What `wails build` adds on macOS: UTType for file dialogs, and a minimum OS.
min=11.0
export CGO_ENABLED=1 MACOSX_DEPLOYMENT_TARGET=$min
export CGO_CFLAGS="-mmacosx-version-min=$min"
export CGO_LDFLAGS="-framework UniformTypeIdentifiers -mmacosx-version-min=$min"
for arch in arm64 amd64; do
  GOOS=darwin GOARCH=$arch go build -trimpath -tags desktop,production \
    -ldflags "-s -w -X main.version=$version" -o "$work/braid-$arch" ./desktop
done

app="$work/Braid.app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
lipo -create -output "$app/Contents/MacOS/Braid" "$work/braid-arm64" "$work/braid-amd64"
# Bundle versions must be numeric; untagged builds get 0.0.0.
short=$(echo "$version" | sed -nE 's/^v?([0-9]+\.[0-9]+\.[0-9]+).*/\1/p')
sed "s/{{VERSION}}/${short:-0.0.0}/g" packaging/macos/Info.plist > "$app/Contents/Info.plist"

identity=${MACOS_SIGN_IDENTITY:--}
if [ "$identity" = "-" ]; then
  codesign --force --sign - "$app"
else
  codesign --force --options runtime --timestamp --sign "$identity" "$app"
fi
codesign --verify --strict "$app"

zip="$out/braid-desktop_${version}_macos_universal.zip"
rm -f "$zip"
ditto -c -k --keepParent "$app" "$zip"
if [ -n "${APPLE_ID:-}" ] && [ "$identity" != "-" ]; then
  xcrun notarytool submit "$zip" --apple-id "$APPLE_ID" --team-id "${APPLE_TEAM_ID:?}" --password "${APPLE_APP_PASSWORD:?}" --wait
  xcrun stapler staple "$app"
  rm "$zip"
  ditto -c -k --keepParent "$app" "$zip"
fi
echo "dist: $(basename "$zip")"
