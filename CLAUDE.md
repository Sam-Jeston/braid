# Braid

Agentic planning pipeline for product, design and engineering. Agents (Claude Code / Codex) run **locally** on each member's machine. Shared state (threads, documents, run transcripts) lives on a self-hosted `braid-server` and streams to every teammate. Read `docs/ARCHITECTURE.md` before changing the protocol, the auth flow or the stage rules.

## Stack and scope

- **Go 1.27**, module `github.com/sam-jeston/braid`. Everything outside `frontend/` is Go.
- **frontend/** is React + TypeScript, built by Bun into `frontend/dist`, which the Go binaries embed. The parent Bun guidance (`Bun.serve`, `bun:sqlite`, etc.) applies to frontend tooling only. Never add a Node/Bun server: Go serves the UI.
- SQLite via `modernc.org/sqlite` (pure Go, no CGO). WebSockets via `github.com/coder/websocket`. Desktop shell via Wails v2.

## Commands

```sh
make test                 # go vet + go test ./... + frontend typecheck + bun test (run before finishing any change)
make cover                # what CI runs: Go tests with -race, failing below COVER_MIN (75%) statement coverage
make server app           # bin/braid-server, bin/braid (builds the frontend first)
make desktop              # native Wails app; Linux needs libgtk-3-dev + libwebkit2gtk-4.0-dev
make desktop-windows      # cross-compile bin/Braid.exe (no CGO needed); use this to type-check desktop/
make dev-server           # team server on 127.0.0.1:8080, prints a setup code on first run
make demo                 # app with the offline demo agent (BRAID_FAKE_AGENT=1), no model calls
make dist                 # cross-compiled release archives into dist/ (desktop: make dist-desktop-{linux,windows,macos})
go test ./internal/local -run TestPipeline -race   # full two-teammate pipeline against a real server
```

## Layout

| Path | Role |
|---|---|
| `cmd/braid-server` | server binary (`serve`, `setup-code`, `recovery-code`) |
| `cmd/braid` | local core + UI served to the browser |
| `desktop/` | Wails window around the same core (build tags `desktop,production`) |
| `internal/api` | wire types shared by server, core and (mirrored) UI |
| `internal/authn` | device keys, signed messages, invite/setup codes, IDs |
| `internal/server` | HTTP API, SQLite store, event log + websocket hub, run reaper |
| `internal/client` | the core's authenticated client for braid-server |
| `internal/local` | desktop core: profiles, secrets, realtime session, runner, loopback API |
| `internal/agents` | Claude Code / Codex / fake adapters |
| `internal/workflow` | stage definitions, ownership rules, prompts, plan schema |
| `internal/export` | Markdown, Linear, Jira publishers |
| `internal/repo` | managed read-only git clones |
| `internal/host` | where agent CLIs and git run (this computer, or a WSL distro from Windows) and path mapping between the two |

## Cross-cutting rules

- **Keep these in sync** when you change them:
  - `internal/api/types.go` ↔ `frontend/src/types.ts`, and bump the protocol version (see [Versioning](#versioning-server--apps))
  - stage definitions in `internal/workflow/stages.go` ↔ `STAGES` in `frontend/src/util.ts`
- **The server never executes agents or reads code.** Anything that touches the codebase, an agent CLI or tracker credentials belongs in `internal/local`.
- **No secrets on the server.** Devices authenticate with Ed25519 keys. Tracker credentials live in the member's keychain (`internal/local/config.go` `Secrets`), never in `config.json` and never in a request to braid-server.
- **Agent output is untrusted** (documents, replies, mockup HTML). Validate it on the server, sanitize it in the UI.
- **User-facing errors** (`errf` messages on the server, errors returned from the core) are shown verbatim in UI toasts. Write them as plain sentences that tell the user what to do.
- **CI** (`.github/workflows/`, documented in `docs/RELEASING.md`) runs Go tests on Linux, macOS and Windows and enforces the coverage floor. New behaviour needs tests, so don't lower `COVER_MIN` to get a change through. Keep tests portable: use `filepath`, not hard-coded `/`, and put Unix-only tests behind `//go:build !windows`.
- **Testing:** use the fake agent (`BRAID_FAKE_AGENT=1`, `BRAID_FAKE_DELAY_MS`) and `BRAID_SECRETS=file`. Extend `internal/local/e2e_test.go` when you change workflow behaviour, and keep the fake agent's outputs valid for every stage.
- **Embedding:** `frontend/dist/.gitkeep` must exist so `go:embed all:dist` compiles on a fresh clone. Don't delete it; `build.ts` and `make clean` preserve it.
- Match the surrounding code's style and comment density. Go code is `gofmt`ed. The frontend has no formatter config yet, so follow the existing formatting (2-space indent, double quotes, trailing commas, wide lines).

## Versioning (server ↔ apps)

A team upgrades its `braid-server`, and each member upgrades their app, on their own schedule. Either side can therefore be older than the other. `internal/api/version.go` holds the three numbers that decide whether they can work together:

| Constant | Meaning |
|---|---|
| `Protocol` | the API version this build speaks |
| `MinClientProtocol` | the oldest app the server still serves; apps from before versioning count as 0 |
| `MinServerProtocol` | the oldest server the app works with |

How the check works:

- **The app checks.** It sends `Braid-Protocol: <Protocol>` on every request and reads `GET /v1/info` (`protocol`, `min_client_protocol`) whenever it connects or probes a server (`client.CheckCompatible`).
- **The server turns old apps away.** It answers apps below its minimum with `426 client_outdated`; `/v1/info` and `/healthz` are exempt.
- **The member sees what to do.** The connection becomes `incompatible`, and the UI swaps the workspace for **Update required**:
  - app too old: a download link to the release matching the server's version;
  - server too old: a request to get the server updated.
- **Recovery is automatic.** The app rechecks every minute, so it reconnects by itself once the server is updated or rolled back.

**When you change `internal/api`, a server route or an event payload:**

1. **Bump `Protocol`.** Changes that land between two releases can share one bump.
2. **The server stops working with older apps** (you removed or renamed a field or route, changed what one means, or made a request field required): set `MinClientProtocol = Protocol`. Prefer additive changes that older apps can ignore, so members aren't forced to update.
3. **The app now needs something older servers lack** (a new route or field it can't do without): set `MinServerProtocol = Protocol`. Upgrading the server first is the usual path, so this is the cheaper of the two minimums to raise.
4. **Purely additive changes** that both sides can ignore need step 1 only.
5. **Never lower a minimum or reuse a number,** and say in the release notes when a minimum changed.

Release versions (`git describe`, injected with `-X main.version`) are only displayed. Compatibility is decided by these numbers alone, so `dev` builds work too. Tests: `internal/client/client_test.go` (the rules), `internal/server/version_test.go` (the gate), `internal/local/version_test.go` (end to end).
