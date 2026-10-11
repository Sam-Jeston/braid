package agents

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sam-jeston/braid/internal/api"
	"github.com/sam-jeston/braid/internal/host"
)

// Fake is a deterministic stand-in agent for tests and offline demos
// (enabled with BRAID_FAKE_AGENT=1). It writes plausible stage documents
// without calling any model.
type Fake struct{}

func (f *Fake) ID() string   { return "fake" }
func (f *Fake) Name() string { return "Fake agent (demo)" }

func (f *Fake) Detect(ctx context.Context, h host.Host) (string, error) { return "1.0", nil }

func (f *Fake) Run(ctx context.Context, spec Spec, emit func(api.RunOutput)) (Result, error) {
	delay := 40 * time.Millisecond
	if v, err := strconv.Atoi(os.Getenv("BRAID_FAKE_DELAY_MS")); err == nil {
		delay = time.Duration(v) * time.Millisecond
	}
	step := func(o api.RunOutput) error {
		emit(o)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
			return nil
		}
	}
	write := func(name, content string) error {
		p := filepath.Join(spec.Dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := step(api.RunOutput{Kind: "tool", Tool: "Write", Detail: name}); err != nil {
			return err
		}
		return os.WriteFile(p, []byte(content), 0o644)
	}

	if err := step(api.RunOutput{Kind: "status", Text: "Fake agent started"}); err != nil {
		return Result{}, err
	}
	files := repoFiles(spec, 6)
	for _, f := range files {
		if err := step(api.RunOutput{Kind: "tool", Tool: "Read", Detail: f}); err != nil {
			return Result{}, err
		}
	}
	for _, w := range strings.Fields("Looking at the codebase and the conversation so far.") {
		if err := step(api.RunOutput{Kind: "text", Text: w + " "}); err != nil {
			return Result{}, err
		}
	}
	title := firstHeading(spec.Prompt)
	var reply string
	var err error
	switch spec.Stage {
	case "scan":
		err = write("codebase-brief.md", fmt.Sprintf("# Codebase brief\n\n## What this product is\nA demo codebase.\n\n## Key domains\n%s\n", bullet(files)))
		if err == nil {
			err = write("rules.md", "- Keep changes small and reviewed.\n- Follow existing patterns in the files listed in the brief.\n")
		}
		reply = fmt.Sprintf("Scanned %d files. Wrote a codebase brief and starter rules.", len(files))
	case "sparring":
		err = write("brief.md", fmt.Sprintf(`# %s

## Problem
Users can't do this today without workarounds.

## Users & jobs to be done
- Team leads who need it weekly.

## Proposed solution
A focused first version.

## Scope
**In:** the core flow.
**Out:** admin tooling.

## Viability vs. codebase
Existing building blocks:
%s
Effort signal: **M**.

## Success metrics
- Weekly active usage of the new flow.

## Open questions
- Who owns this after launch?

## Decisions log
- Started sparring.
`, title, bullet(files)))
		reply = "Drafted `brief.md`. Biggest risk: we haven't confirmed who has this problem. **Question:** can we cut admin tooling from v1?"
	case "design":
		err = write("design.md", fmt.Sprintf("# Design: %s\n\n## UX goals\n- Fast to scan.\n\n## Variations\n1. **Compact list** — `mockups/compact-list.html`\n2. **Guided wizard** — `mockups/guided-wizard.html`\n\n## Recommendation\nCompact list.\n", title))
		if err == nil {
			err = write("mockups/compact-list.html", mockup("Compact list", "#5b5bd6"))
		}
		if err == nil {
			err = write("mockups/guided-wizard.html", mockup("Guided wizard", "#0f9d8a"))
		}
		reply = "Two variations are up: a compact list and a guided wizard. I recommend the compact list because it reuses the existing table component."
	case "technical":
		err = write("technical.md", fmt.Sprintf("# Technical plan: %s\n\n## Summary\nAdd an endpoint and a view.\n\n## Work breakdown\n1. API endpoint — 3 pts\n2. UI view — 3 pts\n3. Tests — 2 pts\n", title))
		reply = "Technical plan drafted: 3 work items, ~8 points. No migrations needed."
	case "plan":
		err = write("plan.json", `{
  "summary": "Ship the first version of the feature.",
  "epic": {"title": "`+jsonEscape(title)+`", "description": "Deliver the approved scope."},
  "tickets": [
    {"ref": "T1", "title": "Add API endpoint", "type": "task", "role": "engineering", "description": "Implement the endpoint.", "acceptance_criteria": ["Returns 200", "Validates input"], "estimate": 3, "labels": ["backend"], "depends_on": []},
    {"ref": "T2", "title": "Build the view", "type": "story", "role": "engineering", "description": "Implement the compact list mockup.", "acceptance_criteria": ["Matches mockup"], "estimate": 3, "labels": ["frontend"], "depends_on": ["T1"]},
    {"ref": "T3", "title": "End-to-end tests", "type": "task", "role": "engineering", "description": "Cover the happy path.", "acceptance_criteria": ["CI green"], "estimate": 2, "labels": ["qa"], "depends_on": ["T2"]}
  ]
}`)
		if err == nil {
			err = write("plan.md", "# Plan\n\n3 tickets, 8 points.\n")
		}
		reply = "Plan ready: 1 epic, 3 tickets, 8 points."
	default:
		reply = "Nothing to do for stage " + spec.Stage
	}
	if err != nil {
		return Result{}, err
	}
	return Result{Reply: reply}, nil
}

// repoFiles lists up to n files from the checkouts, labelled as run activity shows them.
func repoFiles(spec Spec, n int) []string {
	// The fake agent runs in-process, so it walks Braid's paths, not the host's.
	spec.Host = nil
	roots := spec.roots()
	var out []string
	for _, r := range spec.Repos {
		var files []string
		_ = filepath.WalkDir(r.Dir, func(p string, d os.DirEntry, err error) error {
			if err != nil || len(out)+len(files) >= n {
				return filepath.SkipDir
			}
			if d.IsDir() && strings.HasPrefix(d.Name(), ".") && p != r.Dir {
				return filepath.SkipDir
			}
			if !d.IsDir() {
				files = append(files, rel(p, roots))
			}
			return nil
		})
		sort.Strings(files)
		out = append(out, files...)
	}
	return out
}

func bullet(xs []string) string {
	if len(xs) == 0 {
		return "- (no files available)\n"
	}
	var b strings.Builder
	for _, x := range xs {
		fmt.Fprintf(&b, "- `%s`\n", x)
	}
	return b.String()
}

func firstHeading(prompt string) string {
	for _, l := range strings.Split(prompt, "\n") {
		if t, ok := strings.CutPrefix(l, "# Thread: "); ok {
			return strings.TrimSpace(t)
		}
	}
	return "Untitled feature"
}

func jsonEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s)
}

func mockup(name, accent string) string {
	return `<!doctype html><html><head><meta charset="utf-8"><style>
body{font-family:system-ui,sans-serif;margin:0;background:#f6f7f9;color:#1d2330}
header{background:` + accent + `;color:#fff;padding:16px 24px;font-weight:600}
main{padding:24px;display:grid;gap:12px;max-width:960px}
.card{background:#fff;border-radius:10px;padding:16px;box-shadow:0 1px 2px rgba(0,0,0,.08)}
</style></head><body><header>` + name + `</header><main>
<div class="card"><b>Weekly report</b><p>Ready to send to 12 recipients.</p></div>
<div class="card"><b>Drafts</b><p>2 drafts waiting for review.</p></div>
</main></body></html>`
}
