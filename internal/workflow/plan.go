package workflow

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Plan is the structured output of the plan stage; exporters turn it into
// tickets in the team's tracker.
type Plan struct {
	Summary string       `json:"summary"`
	Epic    PlanEpic     `json:"epic"`
	Tickets []PlanTicket `json:"tickets"`
}

type PlanEpic struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}

type PlanTicket struct {
	Ref                string   `json:"ref"`
	Title              string   `json:"title"`
	Type               string   `json:"type"`
	Role               string   `json:"role"`
	Description        string   `json:"description"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	Estimate           int      `json:"estimate"`
	Labels             []string `json:"labels"`
	DependsOn          []string `json:"depends_on"`
}

// PlanSchema is shown to the agent verbatim.
const PlanSchema = `{
  "summary": "2-4 sentence summary of what will be delivered and why",
  "epic": { "title": "string", "description": "markdown: goal, scope, links to key decisions" },
  "tickets": [
    {
      "ref": "T1",
      "title": "imperative, specific title",
      "type": "story | task | spike",
      "role": "engineering | design | product",
      "description": "markdown: context, what to do, files/modules involved, design reference",
      "acceptance_criteria": ["testable criterion", "..."],
      "estimate": 3,
      "labels": ["frontend"],
      "depends_on": ["T0"]
    }
  ]
}`

var validEstimates = map[int]bool{0: true, 1: true, 2: true, 3: true, 5: true, 8: true, 13: true}

func ParsePlan(s string) (*Plan, error) {
	var p Plan
	dec := json.NewDecoder(strings.NewReader(s))
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("plan.json is not valid JSON: %w", err)
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

func (p *Plan) Validate() error {
	if strings.TrimSpace(p.Epic.Title) == "" {
		return fmt.Errorf("plan.json: epic.title is required")
	}
	if len(p.Tickets) == 0 {
		return fmt.Errorf("plan.json: at least one ticket is required")
	}
	if len(p.Tickets) > 60 {
		return fmt.Errorf("plan.json: too many tickets (%d > 60)", len(p.Tickets))
	}
	refs := map[string]bool{}
	for i, t := range p.Tickets {
		if strings.TrimSpace(t.Ref) == "" {
			return fmt.Errorf("plan.json: ticket %d has no ref", i+1)
		}
		if refs[t.Ref] {
			return fmt.Errorf("plan.json: duplicate ticket ref %q", t.Ref)
		}
		refs[t.Ref] = true
		if strings.TrimSpace(t.Title) == "" {
			return fmt.Errorf("plan.json: ticket %s has no title", t.Ref)
		}
		if !validEstimates[t.Estimate] {
			return fmt.Errorf("plan.json: ticket %s has estimate %d (use 1, 2, 3, 5, 8 or 13)", t.Ref, t.Estimate)
		}
	}
	for _, t := range p.Tickets {
		for _, d := range t.DependsOn {
			if !refs[d] {
				return fmt.Errorf("plan.json: ticket %s depends on unknown ref %q", t.Ref, d)
			}
			if d == t.Ref {
				return fmt.Errorf("plan.json: ticket %s depends on itself", t.Ref)
			}
		}
	}
	return nil
}

func (p *Plan) TotalPoints() int {
	n := 0
	for _, t := range p.Tickets {
		n += t.Estimate
	}
	return n
}

// Body renders a ticket's full markdown description for trackers.
func (t PlanTicket) Body() string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(t.Description))
	if len(t.AcceptanceCriteria) > 0 {
		b.WriteString("\n\n## Acceptance criteria\n")
		for _, c := range t.AcceptanceCriteria {
			fmt.Fprintf(&b, "- [ ] %s\n", strings.TrimSpace(c))
		}
	}
	var meta []string
	if t.Estimate > 0 {
		meta = append(meta, fmt.Sprintf("Estimate: %d pts", t.Estimate))
	}
	if t.Role != "" {
		meta = append(meta, "Role: "+t.Role)
	}
	if len(t.DependsOn) > 0 {
		meta = append(meta, "Depends on: "+strings.Join(t.DependsOn, ", "))
	}
	if len(meta) > 0 {
		b.WriteString("\n\n---\n" + strings.Join(meta, " · ") + "\n")
	}
	return strings.TrimSpace(b.String()) + "\n"
}

// Markdown renders the whole plan as a single document.
func (p *Plan) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", p.Epic.Title)
	if p.Summary != "" {
		b.WriteString(strings.TrimSpace(p.Summary) + "\n\n")
	}
	if p.Epic.Description != "" {
		b.WriteString(strings.TrimSpace(p.Epic.Description) + "\n\n")
	}
	fmt.Fprintf(&b, "## Tickets (%d, %d pts)\n\n| Ref | Title | Type | Role | Est. | Depends on |\n|---|---|---|---|---|---|\n", len(p.Tickets), p.TotalPoints())
	for _, t := range p.Tickets {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %d | %s |\n", t.Ref, escapeCell(t.Title), t.Type, t.Role, t.Estimate, strings.Join(t.DependsOn, ", "))
	}
	for _, t := range p.Tickets {
		fmt.Fprintf(&b, "\n## %s · %s\n\n%s", t.Ref, t.Title, t.Body())
	}
	return b.String()
}

func escapeCell(s string) string { return strings.ReplaceAll(s, "|", "\\|") }
