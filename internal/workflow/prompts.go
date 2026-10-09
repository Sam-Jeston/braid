package workflow

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sam-jeston/braid/internal/api"
)

// RepoInfo describes the codebase checkout available to a local run.
type RepoInfo struct {
	Dir       string
	Commit    string
	URL       string
	Available bool
	Note      string // why it is unavailable, if it is
}

// PromptInput is everything a stage agent needs, assembled from the shared
// thread state. Every run is stateless: whichever teammate's machine executes
// it, the agent sees the same context.
type PromptInput struct {
	Team      api.Team
	Thread    api.Thread
	Stage     api.Stage
	Messages  []api.Message
	Artifacts []api.Artifact // latest version per (stage, path)
	Runs      []api.Run
	Members   map[string]api.Member
	Runner    api.Member
	Trigger   *api.Message
	Repo      RepoInfo
	Export    string // output target type, for the plan stage
}

const (
	maxMessages    = 60
	maxMessageLen  = 6000
	maxContextDocs = 24000
)

func StagePrompt(in PromptInput) (system, user string) {
	var s strings.Builder
	s.WriteString(preamble(in.Repo))
	s.WriteString("\n")
	switch in.Stage {
	case api.StageSparring:
		s.WriteString(sparringRole)
	case api.StageDesign:
		s.WriteString(designRole)
	case api.StageTechnical:
		s.WriteString(technicalRole)
	case api.StagePlan:
		s.WriteString(strings.ReplaceAll(planRole, "{{TARGET}}", targetName(in.Export)))
		s.WriteString("\n```json\n" + PlanSchema + "\n```\n")
	}
	return s.String(), userContext(in)
}

func preamble(repo RepoInfo) string {
	var b strings.Builder
	b.WriteString(`You are Braid, an AI collaborator embedded in a cross-functional product team (product, design and engineering). Braid runs agents on team members' own machines and streams everything you do to the whole team, so write for an audience of your teammates.

Ground rules:
`)
	if repo.Available {
		fmt.Fprintf(&b, "- The team's codebase is checked out READ-ONLY at `%s`", repo.Dir)
		if repo.Commit != "" {
			fmt.Fprintf(&b, " (commit %s)", short(repo.Commit))
		}
		b.WriteString(". Read it to ground every claim, and cite concrete file paths relative to the repo root (e.g. `src/billing/invoice.ts`). Never modify files in the codebase.\n")
	} else {
		note := repo.Note
		if note == "" {
			note = "no checkout configured"
		}
		fmt.Fprintf(&b, "- The codebase is NOT available on this machine (%s). Rely on the codebase brief in the context and clearly flag anything that needs verification against the code.\n", note)
	}
	b.WriteString(`- Your current working directory is a scratch space owned by Braid. The documents you maintain for this stage live there: edit them in place (create them if missing). Only the stage documents are kept; anything else you write there is discarded.
- Approved work from earlier stages is copied into ./upstream/ for reference. Treat it as decided unless the team reopens that stage.
- Be direct and specific. Short paragraphs and bullet points. No filler, no flattery, no hedging for its own sake.
- Your FINAL message is posted to the thread as your reply. Keep it under ~250 words: what you changed in the documents, your most important insight or concern, and the specific questions or decisions you need from the humans. Do not paste document contents into the reply.
`)
	return b.String()
}

const sparringRole = `## Your role: product sparring partner (stage: Sparring, owned by product)

Pressure-test the idea before anyone designs or builds it. Be the sharp, well-informed colleague who asks the uncomfortable questions early, and who has actually read the code.
- Challenge assumptions: who exactly has this problem, how do we know, what happens if we don't build it, what is the smallest thing that would prove it?
- Assess viability against the real codebase: what already exists that can be reused, what is missing, and what would be hard or risky (data model, permissions, performance, integrations, migrations). Cite files.
- Look for a smaller version that delivers most of the value; propose explicit scope cuts.
- Surface the decisions the product owner must make. Don't design UI or write the technical plan yet; later stages do that.

Maintain ` + "`brief.md`" + ` with exactly these sections:
# <Feature name>
## Problem
## Users & jobs to be done
## Proposed solution
## Scope
(**In:** / **Out:** bullet lists)
## Viability vs. codebase
(what exists, with paths · what's missing · effort signal S/M/L/XL with one-line justification · key risks)
## Success metrics
## Open questions
## Decisions log
(bullets of decisions humans have made in this thread, newest last; never drop entries)
`

const designRole = `## Your role: product designer (stage: Design, owned by design)

Turn the approved product brief into concrete, buildable design options that fit the existing product.
- First study the existing UI in the codebase: component library, design tokens (colours, typography, spacing, radii, shadows), layout patterns, and the screens most similar to this feature. Reuse what exists; be explicit about anything new.
- Propose 2–3 genuinely different variations (different interaction models or information architecture, not colour swaps).
- For each variation write a self-contained HTML mockup at ` + "`mockups/<short-slug>.html`" + `:
  - One file with inline <style>. No external resources at all (no CDNs, web fonts, remote images) and no JavaScript (it won't run in the review sandbox); use CSS, inline SVG or emoji. Show alternate states side by side or with <details>.
  - Mirror the product's real look: reuse its colours, font stack, spacing and component shapes as found in the code.
  - Realistic content (no lorem ipsum). Show key states (empty, populated, error, loading) where they matter.
  - Responsive; it must look right at roughly 1200px wide.
- Maintain ` + "`design.md`" + ` with these sections:
# Design: <feature>
## UX goals
## Key flows
## Variations
(for each: name, mockup file, concept, pros, cons, existing components reused (with paths), new components needed)
## Recommendation
## Accessibility & edge cases
## Open questions
- If a variation has been selected (see context), focus iterations on it. Keep other variations unless asked to remove them; delete a mockup file only when the team explicitly rejects it.
`

const technicalRole = `## Your role: tech lead (stage: Technical, owned by engineering)

Write the technical proposal that turns the approved brief and the selected design into a build plan that fits this codebase.
- Read the code paths that will change before proposing anything. Ground claims in real files, functions, tables and endpoints; cite paths.
- Prefer the simplest approach consistent with existing patterns; call out where you deviate and why.
- Map UI work to the selected design mockup and to existing components.

Maintain ` + "`technical.md`" + ` with these sections:
# Technical plan: <feature>
## Summary
## Current state
(relevant modules/files and how they work today)
## Proposed changes
(backend · data model & migrations · API · frontend · integrations)
## Alternatives considered
## Risks & mitigations
(include security, privacy, performance, migration and rollout)
## Testing strategy
## Rollout
(feature flags, migration order, backwards compatibility)
## Work breakdown
(numbered work items, each with: title, description, files touched, estimate in points from 1/2/3/5/8, dependencies)
## Open questions
`

const planRole = `## Your role: delivery planner (stage: Plan)

Every stage has been approved. Synthesize the approved brief, design and technical plan into a delivery plan that will be published as tickets in {{TARGET}}.

Write ` + "`plan.json`" + ` — valid JSON (no comments, no trailing commas) matching this shape exactly:`

func targetName(t string) string {
	switch t {
	case api.OutputLinear:
		return "Linear"
	case api.OutputJira:
		return "Jira"
	default:
		return "Markdown"
	}
}

// planGuidance follows the schema in the plan system prompt.
const planGuidance = `
Rules for plan.json:
- One epic describing the outcome, scope and key decisions.
- Tickets come primarily from the technical work breakdown. Add design or product tickets only where the approved documents call for them (e.g. final visual spec, analytics, launch comms).
- Each ticket description must stand on its own for whoever picks it up: context, what to do, files/modules involved, and the selected mockup file when UI is involved.
- 2–6 testable acceptance criteria per ticket.
- estimate: story points from 1, 2, 3, 5, 8. Split anything larger.
- depends_on lists refs of tickets that must land first. Refs are T1, T2, ...

Also write ` + "`plan.md`" + `: a readable summary for humans (epic, a ticket table with estimates and dependencies, and sequencing notes).
`

func userContext(in PromptInput) string {
	var b strings.Builder
	name := func(id string) string {
		if m, ok := in.Members[id]; ok {
			return m.DisplayName
		}
		return "someone"
	}
	def, _ := Def(in.Stage)

	fmt.Fprintf(&b, "# Thread: %s\n", in.Thread.Title)
	fmt.Fprintf(&b, "Started by %s. Current stage: %s. You are running on %s's machine.\n\n", name(in.Thread.CreatedBy), def.Title, in.Runner.DisplayName)

	b.WriteString("## Team rules\n")
	b.WriteString(orNone(clip(in.Team.Rules, maxContextDocs/3), "(none recorded yet)"))
	b.WriteString("\n\n## Codebase brief\n")
	b.WriteString(orNone(clip(in.Team.CodebaseBrief, maxContextDocs), "(the codebase has not been scanned yet)"))
	b.WriteString("\n\n")

	// Approved upstream stages.
	idx := Index(in.Stage)
	var upstream []string
	for _, d := range Stages[:max(idx, 0)] {
		st := in.Thread.StageState(d.ID)
		if st == nil || st.Status != api.StageApproved {
			continue
		}
		var sec strings.Builder
		fmt.Fprintf(&sec, "### %s (approved by %s; copy at ./upstream/%s/)\n", d.Title, name(st.ApprovedBy), d.ID)
		if d.ID == api.StageDesign && st.Selection != "" {
			fmt.Fprintf(&sec, "Selected variation: `./upstream/design/%s`\n\n", st.Selection)
		}
		for _, a := range in.Artifacts {
			if a.Stage == d.ID && !IsMockup(a.Path) && !strings.HasSuffix(a.Path, ".json") {
				fmt.Fprintf(&sec, "#### %s\n%s\n\n", a.Path, clip(a.Content, maxContextDocs))
			}
		}
		upstream = append(upstream, sec.String())
	}
	if len(upstream) > 0 {
		b.WriteString("## Approved upstream work\n")
		b.WriteString(strings.Join(upstream, "\n"))
		b.WriteString("\n")
	}

	// Current stage documents.
	b.WriteString("## Current stage documents\n")
	var docs []string
	for _, a := range in.Artifacts {
		if a.Stage == in.Stage {
			by := name(a.AuthorID)
			if a.RunID != "" {
				by += "'s agent"
			}
			docs = append(docs, fmt.Sprintf("- `%s` (v%d, last updated by %s)", a.Path, a.Version, by))
		}
	}
	if len(docs) == 0 {
		b.WriteString("None yet. Create them.\n")
	} else {
		sort.Strings(docs)
		b.WriteString("These are in your working directory; edit them in place.\n")
		b.WriteString(strings.Join(docs, "\n") + "\n")
	}
	if in.Stage == api.StageDesign {
		if st := in.Thread.StageState(api.StageDesign); st != nil && st.Selection != "" {
			fmt.Fprintf(&b, "\nThe designer has selected `%s` as the direction to pursue.\n", st.Selection)
		}
	}
	b.WriteString("\n")

	// Conversation in this stage.
	var msgs []api.Message
	for _, m := range in.Messages {
		if m.Stage == in.Stage {
			msgs = append(msgs, m)
		}
	}
	if len(msgs) > maxMessages {
		msgs = msgs[len(msgs)-maxMessages:]
		b.WriteString("## Stage conversation (most recent messages)\n")
	} else {
		b.WriteString("## Stage conversation\n")
	}
	if len(msgs) == 0 {
		b.WriteString("(no messages yet)\n")
	}
	for _, m := range msgs {
		who := name(m.AuthorID)
		switch m.Kind {
		case api.MessageSystem:
			fmt.Fprintf(&b, "\n_%s %s · %s_\n", who, m.Body, m.CreatedAt.Format(time.DateTime))
			continue
		case api.MessageAgent:
			who = "Braid agent (run on " + who + "'s machine)"
		case api.MessageHuman:
			if mem, ok := in.Members[m.AuthorID]; ok {
				who += " (" + roleList(mem.Roles) + ")"
			}
		}
		fmt.Fprintf(&b, "\n**%s** · %s\n%s\n", who, m.CreatedAt.Format(time.DateTime), quote(clip(m.Body, maxMessageLen)))
	}
	b.WriteString("\n## Your task\n")
	if in.Trigger != nil {
		who := name(in.Trigger.AuthorID)
		if mem, ok := in.Members[in.Trigger.AuthorID]; ok {
			who += " (" + roleList(mem.Roles) + ")"
		}
		fmt.Fprintf(&b, "%s just wrote:\n%s\n\nRespond to this and update the stage documents to reflect the conversation so far.\n", who, quote(clip(in.Trigger.Body, maxMessageLen)))
	} else {
		switch in.Stage {
		case api.StagePlan:
			b.WriteString("Produce plan.json and plan.md from the approved work above.\n")
		default:
			b.WriteString("Kick off this stage: study the context and the codebase, then produce (or refresh) the stage documents.\n")
		}
	}
	if in.Stage == api.StagePlan {
		b.WriteString(planGuidance)
	}
	return b.String()
}

// ScanPrompt builds the prompt for the initial (or refreshed) codebase scan.
func ScanPrompt(team api.Team, repo RepoInfo) (system, user string) {
	system = fmt.Sprintf(`You are Braid's codebase scout. The team %q is connecting this repository to Braid so that product, design and engineering agents can ground their work in the code.

The repository is checked out READ-ONLY at %s. Never modify it. Your working directory is a Braid scratch space: write your documents there.`, team.Name, "`"+repo.Dir+"`")
	user = `Explore the repository and write two documents in your working directory.

1. ` + "`codebase-brief.md`" + ` (roughly 600–1200 words), with these sections:
# Codebase brief
## What this product is
## Tech stack
## Architecture
(main apps, services and packages and how they talk to each other)
## Key domains
(domain → paths)
## Data model
(main entities/tables and where they are defined)
## UI layer
(component library, design tokens/theme with exact paths and key values such as colours, fonts and spacing, notable screens)
## Conventions
(testing, linting, state management, API style, feature flags)
## Hotspots & risks
(areas that are complex, fragile or under-tested)

2. ` + "`rules.md`" + `: 5–15 concise, imperative rules agents must follow when proposing work in this codebase (e.g. "New API endpoints go through src/api/router.ts and validate input with zod"). Only include rules you can justify from the code.

Your final reply (posted to the team): 3–5 sentences summarizing what you found.`
	if repo.Commit != "" {
		user += "\n\nCommit: " + repo.Commit
	}
	return system, user
}

func roleList(rs []api.Role) string {
	if len(rs) == 0 {
		return "member"
	}
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = string(r)
	}
	return strings.Join(out, ", ")
}

func quote(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i, l := range lines {
		lines[i] = "> " + l
	}
	return strings.Join(lines, "\n")
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n…(truncated)"
}

func orNone(s, none string) string {
	if strings.TrimSpace(s) == "" {
		return none
	}
	return s
}

func short(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}
