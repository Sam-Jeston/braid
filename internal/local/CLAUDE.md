# internal/local

The desktop core, shared by `cmd/braid` (browser UI) and `desktop/` (Wails).

## Concurrency

- **Lock order is `Core.mu` → `session.mu`.** Never acquire `Core.mu` while holding `session.mu`. `session.profileSnapshot()` takes `Core.mu`, so don't call it under `session.mu`.
- Don't do network I/O while holding either lock.
- A session (`session.go`) owns the websocket to braid-server. It reconnects with backoff, replays from `lastSeq`, keeps member and team caches fresh, and forwards every remote event to the UI `Bus` verbatim.

## Runs (`runner.go`)

- `StartRun` / `StartScan` create the remote run **first**, which claims the stage so teammates see it immediately. Execution then continues in a goroutine tracked by `c.wg`.
- Each run is stateless: it rebuilds context from the thread (`buildPromptInput`) and writes stage docs into a scratch dir (`materialize`). The approved upstream docs go under `upstream/`.
- Only paths allowed by `workflow.ValidArtifactPath` / `ValidScanPath` are collected. Deleted mockups become tombstones.
- `pump` batches output every 300ms and doubles as the heartbeat. Server-side, a run that misses heartbeats for 2 minutes is marked abandoned. A 409 from the server means the run was closed remotely, so stop work on it.
- Always report a finish (`c.finish` retries). Cancelled runs upload no artifacts.
- Each team repository has its own managed clone, synced concurrently (`syncRepos`). Don't `git reset` a clone while a run is using it (`activeRunsFor`): later runs only sync what's missing.

## Loopback API (`api.go`)

- Every route requires the per-launch bearer token, except static assets and `/local/ws`, which takes `?token=`. Keep the `Host` check and the CORS origin allowlist. Never add an unauthenticated route that returns data.
- `/remote/*` proxies to braid-server with this device's auth. Its denylist (auth, setup, redeem, ws) must stay in place.
- Core-originated UI events use the `local.*` type prefix (`Bus.PublishLocal`). The UI handles them in `frontend/src/store.ts` `applyEvent`.

## Secrets and config

- `config.json` holds non-secret settings only. Device private keys (`device-key:<profile>`) and tracker tokens go through `Secrets`: the OS keychain, or `secrets.json` (0600) when there's no keychain or `BRAID_SECRETS=file` is set. The backend is chosen once and recorded.
- Each team profile has its own keypair, and `server_id` is pinned at join time.
- Local checkouts that replace managed clones are per repository name (`Profile.RepoOverrides`). The older single `RepoOverride` still applies to the first repository until the member saves an override.

Tests: `e2e_test.go` runs two cores against a real in-process server with the fake agent. Use `-race`.
