# CI and releases

Two GitHub Actions workflows live in `.github/workflows/`.

## CI (`ci.yml`): every push to `main` and every pull request

| Job | What it checks |
|---|---|
| Frontend | `bun install --frozen-lockfile`, typecheck, `bun test` (store reducer, markdown sanitization, utilities), production build |
| Go (Linux) | `gofmt`, `go vet`, `make cover`: all Go tests with the race detector, **failing below 75% statement coverage** of `internal/...` (`COVER_MIN` in the Makefile), and a cross-compile of the Windows desktop app |
| Go (macOS, Windows) | `go vet` and `go test ./...` on the platforms the desktop app ships to |

Coverage per function is in each run's summary, and `coverage.out` is attached as an artifact. Run the same checks locally with `make test` and `make cover`.

## Build (`build.yml`): pushes to `main`, `v*` tags, or manual

Every run produces these workflow artifacts:

| Artifact | Built on | Contents |
|---|---|---|
| `braid-server_<version>_<os>_<arch>` | Ubuntu (cross-compiled, no CGO) | the team server for linux, darwin and windows on amd64 and arm64 |
| `braid_<version>_<os>_<arch>` | Ubuntu (cross-compiled) | the app in browser mode (`braid`), same platforms |
| `braid-desktop_<version>_linux_amd64.tar.gz` | Ubuntu 24.04 | native app; needs `libwebkit2gtk-4.1` and GTK 3 at runtime |
| `braid-desktop_<version>_windows_amd64.zip` | Ubuntu (cross-compiled) | `Braid.exe`; uses the WebView2 runtime that ships with Windows 10/11 |
| `braid-desktop_<version>_macos_universal.zip` | macOS | `Braid.app` for Apple silicon and Intel, macOS 11+ |

`<version>` comes from `git describe`, so it's the tag on releases and something like `v0.2.0-4-gabc1234` in between.

Pushing a tag that starts with `v` also:

- creates a **GitHub release** with every archive and a `SHA256SUMS` file. Tags with a hyphen (`v0.2.0-rc.1`) become pre-releases.
- pushes a multi-arch **server image** to `ghcr.io/<owner>/braid-server:<version>` (and `<major>.<minor>`, and `latest` for stable releases).

```sh
git tag v0.1.0 && git push origin v0.1.0
```

### Compatibility between releases

Teams upgrade their server and members upgrade their apps separately. If a release raises `MinClientProtocol` or `MinServerProtocol` in `internal/api/version.go`, say so in its notes:

- **`MinClientProtocol` raised:** apps older than this release stop working with an upgraded server, and members are prompted to update.
- **`MinServerProtocol` raised:** this release's app needs an upgraded server; upgrade servers first.

An app that's too old links to the release whose tag matches the server's version, so keep server and app releases under the same tags.

## Setup

### Required

1. **Actions enabled** for the repository (Settings → Actions → General). The workflows use only the built-in `GITHUB_TOKEN`; the release and image jobs request `contents: write` and `packages: write` themselves.
2. **Branch protection on `main`** (Settings → Branches) requiring the CI checks (`Frontend`, `Go (Linux, race + coverage)`, `Go (macos-latest)`, `Go (windows-latest)`) so nothing merges red.
3. After the first image push, make the `braid-server` package public if you want people to pull it without logging in (your profile → Packages → braid-server → Package settings).

### macOS signing and notarization (strongly recommended)

Without these secrets the app is ad-hoc signed. It runs, but Gatekeeper blocks it after download, so users have to right-click → Open or clear the quarantine attribute. With them, the app is signed with your Developer ID, notarized and stapled.

You need an Apple Developer Program membership ($99/year). Add these repository secrets (Settings → Secrets and variables → Actions):

| Secret | Value |
|---|---|
| `MACOS_CERTIFICATE_P12` | Your **Developer ID Application** certificate and private key, exported from Keychain Access as `.p12`, then `base64 -i cert.p12 \| pbcopy` |
| `MACOS_CERTIFICATE_PASSWORD` | The password you set when exporting the `.p12` |
| `MACOS_SIGN_IDENTITY` | The identity name, e.g. `Developer ID Application: Jane Doe (ABCDE12345)` (`security find-identity -v -p codesigning`) |
| `APPLE_ID` | The Apple ID email used for notarization |
| `APPLE_TEAM_ID` | Your 10-character team ID |
| `APPLE_APP_PASSWORD` | An app-specific password for that Apple ID (appleid.apple.com → Sign-In and Security) |

The same script runs locally on a Mac: `make dist-desktop-macos` (with the variables above exported to sign).

### Windows signing (optional)

`Braid.exe` is unsigned, so SmartScreen warns on first launch until the download builds reputation. To sign it, add a step after `make dist-desktop-windows` using either:

- **Azure Trusted Signing** (cheapest for individuals and small orgs): `azure/trusted-signing-action` with an Azure service principal in secrets. This needs a `windows-latest` runner.
- A code-signing certificate from a CA, with `signtool` on a `windows-latest` runner, or `osslsigncode` on Ubuntu.

### Not built yet

- Linux packages (`.deb`, AppImage, Flatpak); the Linux app ships as a bare binary.
- An arm64 Linux desktop build. Add an `ubuntu-24.04-arm` runner to `desktop-linux` if you need one.
- App icons for the desktop builds, and a Windows manifest/version resource (`go-winres` can generate the `.syso`).
- Auto-update.
