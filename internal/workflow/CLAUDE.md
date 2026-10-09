# internal/workflow

The single source of truth for the thread pipeline: stages, owners, allowed documents, prompts and the plan schema. The server uses it for validation, the core uses it to build prompts and collect outputs.

- **`Stages`** (`stages.go`) defines order, owner role, the primary document required for approval, and allowed artifact path patterns. Mirror changes in `frontend/src/util.ts` (`STAGES`, `STAGE_ORDER`) and in the approval rules described in `docs/ARCHITECTURE.md`.
- **Output contracts:** every file a prompt tells the agent to write must match that stage's `Outputs` patterns. Otherwise the runner silently drops it. Mockups must be static HTML with no scripts or external resources, because the UI renders them in `sandbox=""` iframes under a strict content policy.
- **Plan schema:** `PlanSchema` (shown to the agent), the `Plan` structs, and `Validate()` must agree. The runner gives the agent one repair attempt when `plan.json` fails validation, and the server re-validates on upload. Exporters consume the `Plan` struct.
- **Prompts are stateless.** `StagePrompt` gets everything from `PromptInput`, which is assembled from shared thread state. Don't reference anything that only exists on one machine, apart from the repo checkout path. Keep the size caps (`maxMessages`, `maxMessageLen`, `maxContextDocs`) so long threads stay inside context limits.
- When a prompt change alters which files get written, update `agents.Fake` to match so tests stay meaningful.
