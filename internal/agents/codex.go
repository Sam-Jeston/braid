package agents

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/sam-jeston/braid/internal/api"
)

// Codex runs the OpenAI Codex CLI headlessly (`codex exec --json`). Its
// workspace-write sandbox lets it read anywhere but only write inside the
// Braid scratch dir, which is its working root.
type Codex struct{}

func (c *Codex) ID() string   { return "codex" }
func (c *Codex) Name() string { return "Codex" }

func (c *Codex) Detect(ctx context.Context) (string, error) {
	bin, err := lookPath("codex")
	if err != nil {
		return "", err
	}
	v, err := version(ctx, bin, "--version")
	return strings.TrimPrefix(v, "codex-cli "), err
}

func (c *Codex) Args(spec Spec) []string {
	args := []string{"exec", "--json", "-s", "workspace-write", "-C", spec.Dir, "--skip-git-repo-check", "--ephemeral"}
	if spec.Model != "" {
		args = append(args, "-m", spec.Model)
	}
	return append(args, "-")
}

func (c *Codex) Run(ctx context.Context, spec Spec, emit func(api.RunOutput)) (Result, error) {
	bin, err := lookPath("codex")
	if err != nil {
		return Result{}, err
	}
	// Codex has no append-system-prompt flag; the instructions lead the prompt.
	prompt := spec.Prompt
	if spec.System != "" {
		prompt = "<instructions>\n" + spec.System + "\n</instructions>\n\n" + spec.Prompt
	}
	p := &codexParser{emit: emit, roots: []string{spec.RepoDir, spec.Dir}}
	runErr := runLines(ctx, bin, c.Args(spec), spec.Dir, prompt, p.line)
	if p.failure != "" {
		return Result{Reply: p.last}, errors.New("codex: " + clip(p.failure, 500))
	}
	if runErr != nil {
		return Result{Reply: p.last}, runErr
	}
	if !p.completed {
		return Result{Reply: p.last}, errors.New("codex exited without completing its turn")
	}
	return Result{Reply: strings.TrimSpace(p.last)}, nil
}

type codexLine struct {
	Type  string `json:"type"`
	Item  *codexItem
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
	Message string `json:"message"`
}

type codexItem struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Command  string `json:"command"`
	ExitCode *int   `json:"exit_code"`
	Status   string `json:"status"`
	Query    string `json:"query"`
	Tool     string `json:"tool"`
	Server   string `json:"server"`
	Changes  []struct {
		Path string `json:"path"`
		Kind string `json:"kind"`
	} `json:"changes"`
}

type codexParser struct {
	emit      func(api.RunOutput)
	roots     []string
	last      string
	failure   string
	completed bool
	sawText   bool
}

func (p *codexParser) line(b []byte) {
	var l codexLine
	if err := json.Unmarshal(b, &l); err != nil {
		return
	}
	switch l.Type {
	case "thread.started":
		p.emit(api.RunOutput{Kind: "status", Text: "Codex started"})
	case "item.started":
		if l.Item == nil {
			return
		}
		switch l.Item.Type {
		case "command_execution":
			p.emit(api.RunOutput{Kind: "tool", Tool: "Shell", Detail: clip(relAll(unwrapShell(l.Item.Command), p.roots...), 200)})
		case "mcp_tool_call":
			p.emit(api.RunOutput{Kind: "tool", Tool: l.Item.Tool, Detail: l.Item.Server})
		case "web_search":
			p.emit(api.RunOutput{Kind: "tool", Tool: "WebSearch", Detail: l.Item.Query})
		}
	case "item.completed":
		if l.Item == nil {
			return
		}
		switch l.Item.Type {
		case "agent_message":
			if p.sawText {
				p.emit(api.RunOutput{Kind: "text", Text: "\n\n"})
			}
			p.sawText = true
			p.last = l.Item.Text
			p.emit(api.RunOutput{Kind: "text", Text: strings.TrimSpace(l.Item.Text)})
		case "reasoning":
			p.emit(api.RunOutput{Kind: "thinking", Text: l.Item.Text + "\n"})
		case "file_change":
			var paths []string
			for _, c := range l.Item.Changes {
				paths = append(paths, rel(c.Path, p.roots...))
			}
			p.emit(api.RunOutput{Kind: "tool", Tool: "Edit", Detail: strings.Join(paths, ", ")})
		}
	case "turn.completed":
		p.completed = true
	case "turn.failed":
		if l.Error != nil {
			p.failure = l.Error.Message
		} else {
			p.failure = "turn failed"
		}
	case "error":
		msg := l.Message
		if msg == "" && l.Error != nil {
			msg = l.Error.Message
		}
		p.emit(api.RunOutput{Kind: "error", Text: msg})
	}
}

var shellWrap = regexp.MustCompile(`^\S*(?:sh|bash|zsh)\s+-l?c\s+(?:'(.*)'|"(.*)")$`)

func unwrapShell(cmd string) string {
	if m := shellWrap.FindStringSubmatch(strings.TrimSpace(cmd)); m != nil {
		if m[1] != "" {
			return m[1]
		}
		return m[2]
	}
	return cmd
}
