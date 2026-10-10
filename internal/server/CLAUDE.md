# internal/server

braid-server: shared team state plus realtime fan-out. It never runs agents.

## Database

- `openDB` sets `MaxOpenConns(1)`. **Inside an `s.tx(...)` closure, only use the `tx`.** Calling `s.db` there deadlocks waiting for the single connection. The same applies to helpers: pass them the `tx` (they take a `queryer`).
- Close `rows` before issuing other statements on the same connection (see `handleListThreads`, `replay`).
- Schema changes: **append** a new string to `migrations` in `store.go`. Never edit an existing entry, because applied versions are tracked with `PRAGMA user_version`.
- Timestamps are stored as unix milliseconds (`ms`, `fromMs`, `nullMs`). Booleans are INTEGER, JSON columns are TEXT (`roles`, `output_json`, `items`).
- Artifacts are append-only versions. Empty content is a tombstone, and `latestArtifacts` hides it.

## Events

- Every state change emits its event **in the same transaction** via `tx.emit(...)`. That gives it a durable `seq`, and it is published to websockets only after commit. Don't publish to the hub directly for persisted state.
- Ephemeral messages (`presence`, `hello`, `resync`) have no seq and are never stored.
- Clients reconnect with `/v1/ws?since=<seq>` and the server replays from the `events` table. Event payloads therefore need to be complete snapshots (e.g. the whole `Thread`), not diffs.

## Handlers

- Wrap with `s.h` (public) or `s.authed` (bearer token → `authCtx`). Return `errf(status, code, msg)` for expected failures. The message reaches users as-is.
- Authorization: `requireAdmin(a)` for admin actions, `api.HasRole(a.member.Roles, def.Owner)` for stage ownership. Always scope queries by `a.teamID()`.
- Stage state changes go through `mutateThread`, which reloads, mutates, saves and emits `thread.updated`. Approval and reopen rules live in `handlers_threads.go` and must match `docs/ARCHITECTURE.md`.
- Only one active run per stage (`thread_stages.active_run_id`). Anything that finishes, reaps or cancels a run must clear it.
- Unauthenticated endpoints that accept codes or signatures must be wrapped in `s.limited`.
- Delivery (`handlers_delivery.go`): engineers set it by hand; members' apps report tracker states, which may only move a plan on from `planned`. Write and emit only when something changed, because every connected app reports periodically.
- `checkProtocol` (in `Handler()`) answers apps older than `MinClientProtocol` with `426 client_outdated`. `/v1/info` must stay public and outside that gate, because it's how an app finds out what the server speaks. See "Versioning" in the root `CLAUDE.md`.

Tests: `server_test.go` (auth), plus the end-to-end test in `internal/local`.
