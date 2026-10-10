# Braid architecture

## Principles

1. **Execution is local, state is shared.** `braid-server` never runs agents and never sees the codebase. It stores what agents *produce* (documents, replies, a transcript of tool calls) and relays it to the team in real time.
2. **Runs are stateless.** Each agent invocation reconstructs its full context from the shared thread (team rules, codebase brief, approved upstream docs, the stage conversation, the current stage docs). Any teammate's machine can run the next turn.
3. **No reusable secrets on the server.** Devices authenticate with keys. Tracker credentials stay on the publisher's machine.

## Components

| Component | Runs where | Responsibilities |
|---|---|---|
| `braid-server` | your infrastructure | Teams, members, invites, device keys, threads, stage state machine, versioned artifacts, run records, event log + websocket fan-out, abandoned-run reaper |
| local core (`internal/local`) | each member's machine | Device identity, realtime session with replay, repo checkouts, agent execution, output streaming, exports, loopback API for the UI |
| UI (`frontend/`) | Wails webview or browser | Talks *only* to the local core. Remote calls go through the core's authenticated proxy (`/remote/v1/*`). |

## Authentication

```
Join (once per device per team)
  app: generate Ed25519 keypair (private key → OS keychain or 0600 file)
  app → POST /v1/invites/redeem {code, member, device:{public_key, signature}}
        signature = Sign("braid-register-v1\n{server_id}\n{normalized code}\n{public_key}")
  server: verify signature, check invite (hashed, unexpired, uses < max), create member + device

Session (hourly)
  app → POST /v1/auth/challenge {device_id}            ← {nonce} (single use, 2 min)
  app → POST /v1/auth/token {device_id, nonce, Sign("braid-token-v1\n{server_id}\n{device_id}\n{nonce}")}
  server: verify with stored public key → opaque bearer token (stored hashed, 1h TTL)
```

- Invite and setup codes are random Crockford base32 (60/80 bits), stored as SHA-256, rate limited per IP, and normalized for case and confusable characters.
- `server_id` is pinned in the client profile at join time. Signatures are bound to it, and each team profile has its own key, so a signature can't be replayed to another server.
- Revoking a device or removing a member deletes its sessions immediately and closes its websockets.
- The app refuses plain-HTTP servers except on loopback, unless you explicitly opt in for a LAN.

The **loopback API** between UI and core listens on `127.0.0.1` with a random per-launch token. Browsers can't add that header cross-site. Host headers must be loopback, which blocks DNS rebinding. In desktop mode the webview gets the token from the Wails asset server, which only the embedded webview can reach.

## Versioning

The server and each member's app upgrade independently. Each build speaks one API **protocol version** (`internal/api/version.go`); the server also states the oldest app it serves, and the app states the oldest server it works with.

```
app → GET /v1/info              ← {version, protocol, min_client_protocol}
      app protocol < min_client_protocol   → "Update Braid" (link to the server's release)
      server protocol < app's minimum      → "Ask whoever runs your server to update it"
app → every request carries Braid-Protocol: N
      server answers N < min_client_protocol with 426 client_outdated
```

The check runs on every (re)connect and on the onboarding probe. While incompatible, the app retries every minute. The rules for bumping the numbers are in the root `CLAUDE.md`.

## Thread workflow

```
            approve (product)        approve (design)          approve (eng)
Sparring ───────────────────► Design ───────────────► Technical ──────────────► Plan ── publish ──► completed (planned → built | cancelled)
  open                         locked→open             locked→open               open
  ▲                                                                                │
  └──── reopen (owner) re-locks every later stage; their docs are kept ────────────┘
```

| Stage | Owner | Documents | Approval requires |
|---|---|---|---|
| Sparring | product | `brief.md` | brief exists, no active run |
| Design | design | `design.md`, `mockups/*.html` | docs exist, a variation is **selected** |
| Technical | engineering | `technical.md` | doc exists |
| Plan | — | `plan.json` (validated schema), `plan.md` | completes when published |

- Anyone can comment on any unlocked stage, and anyone can run the agent in an **open** stage. Only one run per stage at a time; the server enforces this with `thread_stages.active_run_id`.
- Members can hold several roles. Admins edit roles and mint invites.
- Artifacts are append-only versions. An empty-content version is a tombstone (e.g. a rejected mockup). Humans can edit documents between runs; the next run builds on their edits.

## Repositories

A team lists one or more repositories (`teams.repos_json`). One is the norm; more suit products that span several codebases. Each has a short name, unique in the team, derived from its URL (`workflow.NormalizeRepos`). Agents see each checkout under that name and cite paths as `name/path` when there's more than one. Branches and URLs are validated on the server because they reach `git` on every member's machine.

Each member's core keeps its own clone of every repository under `<home>/repos/<team>/<name>`, using that member's git credentials. A member can point any repository at an existing local checkout instead (`repo_overrides` in their profile), which Braid reads as-is and never resets.

## Agent execution

```
UI "Ask agent" ─► core.StartRun
  1. POST /v1/runs (claims the stage; teammates see "Priya's agent is working")
  2. git fetch/reset a managed clone of each team repository, concurrently
     (or use the member's local checkout for it as-is); a repo that can't be
     synced is skipped and the prompt says so
  3. GET thread → build prompt (workflow.StagePrompt)
  4. materialize run dir: current stage docs (editable) + upstream/<stage>/… (reference)
  5. spawn the agent CLI; normalize its stream → RunOutput items
     batched every 300ms to POST /v1/runs/{id}/output (doubles as heartbeat)
  6. collect changed/new stage docs from the run dir (validate plan.json, with one repair attempt)
  7. POST /v1/runs/{id}/finish {status, reply, artifacts, commit_sha, cost}
     (commit_sha is the SHA, or "name@sha name@sha" with several repositories)
     → server stores new artifact versions + the agent's reply message atomically
```

**Claude Code** runs as `claude -p --output-format stream-json --verbose --include-partial-messages --restricted --tools Read,Grep,Glob,Write,Edit --add-dir <repo>… --disallowedTools Write(/<repo>/**) Edit(/<repo>/**)… --permission-mode acceptEdits --permission-prompts none --no-session-persistence`. In that mode:

- there are no shell or web tools, and the user's own settings and hooks are ignored;
- file tools are confined to the run dir and the team's repositories, and every repository is read-only.

The prompt goes over stdin, so there's no argv size limit.

**Codex** runs as `codex exec --json -s workspace-write -C <run dir> --skip-git-repo-check --ephemeral -`. Its sandbox allows reads anywhere and writes only in the run dir.

Safety nets:

- Runs time out after 45 minutes.
- A run whose machine stops heartbeating for 2 minutes is marked *abandoned* and its stage is unlocked.
- Any teammate can request a stop, which is delivered to the owning machine over the event stream.

## Realtime

Every state change is written to `events` in the same transaction as the change, with a monotonic `seq`, then published to websocket subscribers after commit.

- **Reconnect:** a client reconnects with `?since=<last seq>` and the server replays what it missed (or sends `resync` past 5000 events).
- **Snapshots:** thread snapshots carry the `seq` they reflect, so the UI ignores replayed events it already has.
- **Presence:** which thread each member is viewing is ephemeral and never stored.

## Data model (SQLite)

`teams` (repositories and output target as JSON), `members` (roles JSON, admin flag), `devices` (public keys), `sessions` (hashed tokens), `invites` / `setup_codes` (hashed), `threads` (including delivery), `thread_stages` (status, approval, design selection, active run), `messages` (human/agent/system), `runs`, `artifacts` (versioned), `exports`, `events`.

## Publishing

`plan.json` → tracker, executed by whoever clicks **Publish**, using credentials from that person's keychain:

- **Linear** (GraphQL): an epic issue, a sub-issue per ticket, and `blocks` relations for dependencies.
- **Jira Cloud** (REST v3): an epic, then child issues with `parent` (retried without it for projects that don't allow it), descriptions converted from Markdown to ADF, and `Blocks` links.
- **Markdown**: a folder with `README.md`, `plan.json`, one file per ticket, and the approved stage docs.

The export result (keys and URLs) is recorded on the server, which completes the thread and marks its **delivery** `planned`.

### Delivery: planned → built | cancelled

```
member's app, while connected (5s after connecting, then every 15 min)
  GET /v1/exports/planned                 ← latest export of each planned, unarchived thread
  read issue states with this member's own credentials
    Linear: issues(filter: {team.key, number in […]}, includeArchived)
    Jira:   POST /rest/api/3/issue/bulkfetch (status, resolution), only on the team's https site
  POST /v1/threads/{id}/delivery {delivery, tracker, items:[{key, state}]}
    → server stores the issue states (export.updated) and, if it changed, the delivery (thread.updated)
```

- **Rules** (`export.Outcome`): the plan is *built* when the epic is done, or every ticket is closed and at least one was done; *cancelled* when the epic is cancelled, or every ticket was. Issues the tracker no longer returns are ignored. Jira has no cancelled category, so a done issue whose status or resolution reads like "Won't Do", "Duplicate" or "Cancelled" counts as cancelled.
- **Tracker reports only move a plan on from `planned`.** The server rejects them (409) otherwise, so an engineer's decision is never overridden. Concurrent reports from several teammates resolve the same way.
- **Engineers** can set any delivery by hand (`POST …/delivery` without `tracker`). That's the only way to close a Markdown plan, and moving a plan back to `planned` hands it back to the tracker check.
- Only members with tracker credentials can check. Everyone sees the result, and the UI also offers a **Check now** button that runs the same check for one plan.
