// Package workflow defines Braid's thread pipeline: the stages, who owns
// them, which documents each stage produces, and the prompts agents receive.
package workflow

import (
	"path"
	"strings"

	"github.com/sam-jeston/braid/internal/api"
)

type StageDef struct {
	ID    api.Stage `json:"id"`
	Title string    `json:"title"`
	// Owner is the role whose approval completes the stage. The plan stage has
	// no owner: it completes when the plan is published.
	Owner api.Role `json:"owner"`
	// Primary must exist before the stage can be approved.
	Primary string `json:"primary"`
	// Outputs are the artifact path patterns agents and humans may write.
	Outputs []string `json:"outputs"`
}

var Stages = []StageDef{
	{ID: api.StageSparring, Title: "Sparring", Owner: api.RoleProduct, Primary: "brief.md", Outputs: []string{"brief.md"}},
	{ID: api.StageDesign, Title: "Design", Owner: api.RoleDesign, Primary: "design.md", Outputs: []string{"design.md", "mockups/*.html"}},
	{ID: api.StageTechnical, Title: "Technical", Owner: api.RoleEngineering, Primary: "technical.md", Outputs: []string{"technical.md"}},
	{ID: api.StagePlan, Title: "Plan", Primary: "plan.json", Outputs: []string{"plan.json", "plan.md"}},
}

// ScanOutputs are the documents produced by a codebase scan run.
var ScanOutputs = []string{"codebase-brief.md", "rules.md"}

const (
	MaxArtifactBytes = 1 << 20
	MaxArtifactsPer  = 24
)

func Def(s api.Stage) (StageDef, bool) {
	for _, d := range Stages {
		if d.ID == s {
			return d, true
		}
	}
	return StageDef{}, false
}

func Index(s api.Stage) int {
	for i, d := range Stages {
		if d.ID == s {
			return i
		}
	}
	return -1
}

func Next(s api.Stage) (api.Stage, bool) {
	i := Index(s)
	if i < 0 || i+1 >= len(Stages) {
		return "", false
	}
	return Stages[i+1].ID, true
}

// InitialStages is the stage state for a brand new thread.
func InitialStages() []api.StageState {
	out := make([]api.StageState, len(Stages))
	for i, d := range Stages {
		st := api.StageLocked
		if i == 0 {
			st = api.StageOpen
		}
		out[i] = api.StageState{Stage: d.ID, Status: st}
	}
	return out
}

// SafePath reports whether p is a clean relative artifact path.
func SafePath(p string) bool {
	if p == "" || len(p) > 160 || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return false
	}
	if path.Clean(p) != p || strings.HasPrefix(p, "..") || strings.Contains(p, "/../") {
		return false
	}
	for _, r := range p {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '.' || r == '-' || r == '_' || r == '/'
		if !ok {
			return false
		}
	}
	return true
}

func matchAny(patterns []string, p string) bool {
	if !SafePath(p) {
		return false
	}
	for _, pat := range patterns {
		if ok, _ := path.Match(pat, p); ok {
			return true
		}
	}
	return false
}

// ValidArtifactPath reports whether stage s may store an artifact at p.
func ValidArtifactPath(s api.Stage, p string) bool {
	d, ok := Def(s)
	return ok && matchAny(d.Outputs, p)
}

func ValidScanPath(p string) bool { return matchAny(ScanOutputs, p) }

func IsMockup(p string) bool {
	ok, _ := path.Match("mockups/*.html", p)
	return ok
}
