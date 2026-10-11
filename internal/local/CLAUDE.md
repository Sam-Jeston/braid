# internal/local

The desktop core, shared by `cmd/braid` (browser UI) and `desktop/` (Wails).

## Concurrency

- **Lock order is `Core.mu` → `session.mu`.** Never acquire `Core.mu` while holding `session.mu`. `session.profileSnapshot()` takes `Core.mu`, so don't call it under `session.mu`.
- Don't do network I/O while holding either lock.
- A session (`session.go`) owns the websocket to braid-server. It reconnects with backoff, replays from `lastSeq`, keeps member and team caches fresh, and forwards every remote event to the UI `Bus` verbatim.
- Each (re)connect starts with `GET /v1/info` and `client.CheckCompatible`. On a mismatch the status becomes `incompatible`, with `State.Update` saying which side to update, and the session rechecks every minute. `revoked` and `incompatible` stay set across retries so the UI doesn't flicker.

## Runs (`runner.go`)

- `StartRun` / `StartScan` create the remote run **first**, which claims the stage so teammates see it immediately. Execution then continues in a goroutine tracked by `c.wg`.
- Each run is stateless: it rebuilds context from the thread (`buildPromptInput`) and writes stage docs into a scratch dir (`materialize`). The approved upstream docs go under `upstream/`.
- A run keeps the agent host it started with (`pickAgent`). Its scratch dir and the managed clones live under that host's `DataDir()`: the Braid home locally, `~/.local/share/braid` inside WSL. Prompts get checkout paths as the agent sees them (`Host.Path`).
- Only paths allowed by `workflow.ValidArtifactPath` / `ValidScanPath` are collected. Deleted mockups become tombstones.
- `pump` batches output every 300ms and doubles as the heartbeat. Server-side, a run that misses heartbeats for 2 minutes is marked abandoned. A 409 from the server means the run was closed remotely, so stop work on it.
- Always report a finish (`c.finish` retries). Cancelled runs upload no artifacts.
- Each team repository has its own managed clone, synced concurrently (`syncRepos`). Don't `git reset` a clone while a run is using it (`activeRunsFor`): later runs only sync what's missing.

## Agent host

- `Config.AgentHost` is "local" or "wsl:<distro>". While it's empty, `refreshAgents` picks the default WSL distro if agents are installed there and not natively. That only happens on Windows; an explicit choice is never overridden.
- Host discovery goes through `Core.hosts` so tests can stand in for WSL (`internal/host/hosttest.FakeWSL`). `refreshAgents` calls wsl.exe, so never call it under `Core.mu`.

## Delivery tracking (`delivery.go`)

- While connected, the session runs `trackDeliveries` (5s after connecting, then every 15 minutes; reconnecting doesn't bring the next check forward). It lists planned exports and reads their issues with **this member's** tracker credentials.
- Jira credentials only go to the team's configured site, over HTTPS, and only for issues whose URL is on that site (`issueTracker`). Export items are written by teammates' apps, so never derive a host from them.
- A 409 from `/delivery` means a teammate's app or an engineer got there first, so ignore it.
- Timings and the Linear endpoint are fields on `Core` (`deliveryTiming`, `linearEndpoint`) so tests can change them without racing the session's goroutines.

## Loopback API (`api.go`)

- Every route requires the per-launch bearer token, except static assets and `/local/ws`, which takes `?token=`. Keep the `Host` check and the CORS origin allowlist. Never add an unauthenticated route that returns data.
- `/remote/*` proxies to braid-server with this device's auth. Its denylist (auth, setup, redeem, ws) must stay in place.
- Core-originated UI events use the `local.*` type prefix (`Bus.PublishLocal`). The UI handles them in `frontend/src/store.ts` `applyEvent`.

## Secrets and config

- `config.json` holds non-secret settings only. Device private keys (`device-key:<profile>`) and tracker tokens go through `Secrets`: the OS keychain, or `secrets.json` (0600) when there's no keychain or `BRAID_SECRETS=file` is set. The backend is chosen once and recorded.
- Each team profile has its own keypair, and `server_id` is pinned at join time.
- Local checkouts that replace managed clones are per repository name (`Profile.RepoOverrides`). The older single `RepoOverride` still applies to the first repository until the member saves an override.

Tests: `e2e_test.go` runs two cores against a real in-process server with the fake agent. Use `-race`.
