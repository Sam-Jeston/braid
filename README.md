# Braid

**Three strands, one plan.** Braid gives product, design and engineering one shared thread for deciding what to build. The agents that do the work run on each person's own machine, and everyone sees everything as it happens.

- **Sparring.** Product posts an idea. An agent that has read the real codebase challenges it: viability, scope cuts, risks, and open questions. It maintains a living `brief.md`.
- **Design.** The agent proposes 2–3 distinct HTML mockups built from the product's existing UI patterns and tokens. A designer picks a direction and iterates.
- **Technical.** The agent turns the brief and the chosen mockup into a code-grounded build plan with a work breakdown.
- **Plan.** Once each stage's owner approves, the agent produces tickets and one click publishes them to **Linear**, **Jira** or **Markdown**.

Every stage keeps a conversation where anyone on the team can weigh in, and anyone can ask the agent to respond. Only the owning role can approve a stage, and reopening a stage re-locks everything downstream.

## How it fits together

```
┌──────────────── each teammate's machine ───────────────┐        ┌──── your server ────┐
│  Braid app (Wails window or browser)                   │        │  braid-server       │
│     │  loopback API + per-launch token                 │        │  (Go + SQLite)      │
│  local core ─────────── device-signed HTTPS + WS ──────┼───────►│  threads, docs,     │
│     ├─ runs Claude Code / Codex headlessly             │        │  run transcripts,   │
│     ├─ keeps a read-only clone of the team repo        │◄───────┤  realtime fan-out   │
│     └─ publishes to Linear/Jira with *your* creds      │ events │                     │
└────────────────────────────────────────────────────────┘        └─────────────────────┘
```

- **Agents only ever run locally**, with your own Claude Code or Codex install and subscription. Every run is stateless: it rebuilds its context from the shared thread, so it doesn't matter whose laptop ran the previous turn.
- **State is remote and shared.** Messages, documents (versioned), and each agent's live tool calls and text stream through `braid-server` to every teammate in real time.
- **No shared secrets.** Each install registers an Ed25519 device key with an invite code, then signs a challenge for short-lived tokens. Tracker API keys stay in your OS keychain and never reach the server.

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the protocol, data model and security details.

## Quick start

Requirements: Go 1.27+, Bun 1.3+, git, and at least one of [Claude Code](https://docs.anthropic.com/en/docs/claude-code) or [Codex](https://github.com/openai/codex) on each teammate's machine.

### 1. Run a team server

```sh
make server
./bin/braid-server serve --addr :8080 --db ./data/braid.db
```

On first start it prints a single-use **setup code** for creating the team. You can mint more with `braid-server setup-code`. Or use Docker:

```sh
docker build -t braid-server . && docker run -p 8080:8080 -v braid-data:/data braid-server
```

The app only accepts HTTPS servers, except on localhost. In production, put the server behind a TLS proxy (e.g. `caddy reverse-proxy --from braid.example.com --to :8080`) and pass `--trust-proxy`, or use `--tls-cert/--tls-key`.

### 2. Run the app

**Browser mode** works anywhere: it's the same core, and the UI opens in your browser.

```sh
make app
./bin/braid            # prints and opens http://127.0.0.1:<port>/#token=…
```

**Native desktop app** ([Wails](https://wails.io)):

```sh
# Linux needs: sudo apt install build-essential pkg-config libgtk-3-dev libwebkit2gtk-4.0-dev
make desktop                         # Ubuntu 24.04+: make desktop WAILS_TAGS=webkit2_41
./bin/braid-desktop

make desktop-windows                 # cross-compiles bin/Braid.exe (WebView2)
```

### 3. Create or join a team

- **Create:** server URL + setup code, your roles, the repository (any git URL your machine can clone; add more if your product spans several), and where plans go (Linear team key / Jira site + project / Markdown). Braid then scans the codebase on your machine and writes a shared *codebase brief* and *agent rules* that every future run gets.
- **Join:** server URL + an invite code from **Settings → Members & invites**. Invites carry roles and expire.

Then pick your agent and add tracker credentials in **Settings → This device**.

### Try it without spending tokens

`BRAID_FAKE_AGENT=1` adds a deterministic "demo agent" that writes plausible documents and mockups offline:

```sh
make dev-server        # terminal 1
make demo              # terminal 2
```

## Development

```sh
make test              # go vet, go test (incl. a two-teammate end-to-end pipeline test), tsc
make dev-server        # team server on :8080
make dev-app           # rebuilds the UI on change and serves it from disk
```

| Path | What |
|---|---|
| `cmd/braid-server` | server binary (`serve`, `setup-code`) |
| `cmd/braid` | local core + browser UI |
| `desktop/` | Wails native shell around the same core |
| `internal/server` | HTTP API, SQLite store, websocket hub, run reaper |
| `internal/local` | desktop core: profiles, keys, sync, runner, loopback API |
| `internal/agents` | Claude Code / Codex adapters (stream parsing, sandboxing) |
| `internal/workflow` | stages, ownership rules, prompts, plan schema |
| `internal/export` | Markdown, Linear (GraphQL), Jira (REST + ADF) |
| `internal/repo` | managed read-only clones |
| `frontend/` | React UI, built with Bun and embedded via `go:embed` |

Useful environment variables: `BRAID_HOME` (app data dir), `BRAID_SECRETS=file` (skip the OS keychain), `BRAID_KEEP_RUNS=1` (keep agent scratch dirs), `BRAID_MAX_RUN_MINUTES`, `BRAID_FAKE_AGENT=1`.

## Status & roadmap

This is a working v1. Known gaps and natural next steps:

- Identity is per team (name + device key). No SSO yet, and one person on several devices joins with several invites.
- One server process. SQLite with a single writer is plenty for teams, but it isn't horizontally scalable.
- Linear and Jira exporters are tested against mocked APIs, not live workspaces.
- The Linux desktop build isn't packaged (no `.deb`/AppImage). macOS builds need signing/notarization.
- More agents (Gemini CLI, Aider) only need an adapter in `internal/agents`.
