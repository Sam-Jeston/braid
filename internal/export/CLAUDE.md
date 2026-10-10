# internal/export

Publishes a validated `workflow.Plan` to the team's tracker. This code runs **on the publishing member's machine with their own credentials**. Never move it server-side or send credentials anywhere except the tracker's API.

- **Linear** (GraphQL):
  - Personal API keys go in the `Authorization` header *without* a `Bearer` prefix.
  - The epic is a parent issue, tickets are sub-issues (`parentId`), and dependencies become `blocks` relations.
- **Jira Cloud** (REST v3):
  - Basic auth with email and API token.
  - Descriptions must be ADF, so use `ADF()` (it handles the markdown subset agents produce).
  - Tickets are parented to the epic, with a retry without `parent` for projects that don't support it.
  - Dependencies become `Blocks` issue links.
- Relations and links are best-effort. A failure there must not fail the export.
- **Reading states back** (`tracker.go`, `Tracker.States`) drives delivery tracking. A key that no longer exists must be left out, not fail the batch:
  - Linear: don't batch `issue(id:)` with aliases. Its result is non-null, so one missing issue nulls the whole response. Filter `issues` by team key and number instead, with `includeArchived: true`.
  - Jira: use `POST /rest/api/3/issue/bulkfetch`, not a JQL `key in (…)` search, which can fail outright on a deleted key.
- `Outcome` decides planned / built / cancelled from item states. Keep its rules in sync with `docs/ARCHITECTURE.md`.
- On a partial failure, return the items already created along with the error. The core records them so nobody publishes twice.
- Tests use `httptest` mocks of each API (`export_test.go`). Keep request shapes asserted there when changing payloads.
