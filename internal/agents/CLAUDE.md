# internal/agents

Headless adapters for local coding-agent CLIs. They normalize each CLI's stream into `api.RunOutput` items (`text`, `thinking`, `tool`, `tool_error`, `status`, `error`).

## Isolation is a security property; don't loosen it

- **Claude Code** runs with:
  - `--restricted`: no shell or web tools, and the user's own settings and hooks are ignored;
  - `--tools Read,Grep,Glob,Write,Edit`;
  - `--add-dir` with every repo checkout, plus deny rules `Write(/<repo>/**)` and `Edit(/<repo>/**)` for each, which keep them read-only;
  - `--permission-prompts none`.
- **Codex** runs with `-s workspace-write -C <run dir>`: it can read anywhere but write only to the run dir.
- The agent's working directory is always the Braid scratch run dir, never the repo.
- `TestClaudeArgsLockRepo` / `TestClaudeArgsLockEveryRepo` guard the Claude flags. Update them deliberately if they change.

## Conventions

- Prompts go over **stdin**, never argv: Linux caps a single argument at 128 KiB. Only the short system prompt is passed as a flag (Claude's `--append-system-prompt`; Codex gets it prepended to stdin).
- `sessionEnv()` strips variables that would make a child CLI think it's nested inside another agent session. Add to that list instead of clearing the environment, because users' provider settings (Bedrock, proxies) live in env vars.
- Children run in their own process group. Cancel sends SIGINT, then SIGKILL after 5s (`proc_unix.go`).
- Paths in tool details are shown relative to the run dir or repo (`rel`, `relAll` with `Spec.roots()`). With several repos they're prefixed by the repo's name, matching how prompts ask agents to cite them.
- `Fake` (enabled by `BRAID_FAKE_AGENT=1`) must keep producing valid documents for every stage, including a `plan.json` that passes `workflow.ParsePlan`. Tests and `make demo` depend on it.

## Stream formats

`testdata/claude.jsonl` and `testdata/codex.jsonl` are real captures with personal paths and config removed. When a CLI changes its output format, recapture a small run (read a file, write a file, one denied write), sanitize it the same way, and update the parser tests.

A new agent needs an `Agent` implementation, registration in `All()`, a fixture, and a parser test.
