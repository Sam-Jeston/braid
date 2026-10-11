package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/sam-jeston/braid/internal/api"
	"github.com/sam-jeston/braid/internal/host"
)

// Claude runs Claude Code headlessly (`claude -p --output-format stream-json`).
//
// Isolation: --restricted removes command-running tools and ignores the
// user's settings files; --tools limits it to file read/search/edit; file
// tools are confined to the working directories, and explicit deny rules make
// the codebase checkout read-only. Only the Braid scratch dir is writable.
type Claude struct{}

func (c *Claude) ID() string   { return "claude" }
func (c *Claude) Name() string { return "Claude Code" }

func (c *Claude) Detect(ctx context.Context, h host.Host) (string, error) {
	bin, err := h.LookPath(ctx, "claude")
	if err != nil {
		return "", err
	}
	v, err := version(ctx, h, bin, "--version")
	return strings.TrimSuffix(v, " (Claude Code)"), err
}

func (c *Claude) Args(spec Spec) []string {
	args := []string{
		"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages",
		"--restricted", "--tools", "Read,Grep,Glob,Write,Edit",
		"--permission-mode", "acceptEdits", "--permission-prompts", "none",
		"--no-session-persistence",
	}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if spec.System != "" {
		args = append(args, "--append-system-prompt", spec.System)
	}
	if len(spec.Repos) > 0 {
		var dirs, deny []string
		for _, r := range spec.Repos {
			dir := strings.TrimRight(spec.host().Path(r.Dir), "/")
			dirs = append(dirs, dir)
			deny = append(deny, "Write(/"+dir+"/**)", "Edit(/"+dir+"/**)")
		}
		args = append(args, "--add-dir")
		args = append(args, dirs...)
		args = append(args, "--disallowedTools")
		args = append(args, deny...)
	}
	return args
}

func (c *Claude) Run(ctx context.Context, spec Spec, emit func(api.RunOutput)) (Result, error) {
	h := spec.host()
	bin, err := h.LookPath(ctx, "claude")
	if err != nil {
		return Result{}, err
	}
	p := &claudeParser{emit: emit, roots: spec.roots()}
	runErr := runLines(ctx, h, bin, c.Args(spec), spec.Dir, spec.Prompt, p.line)
	if p.done {
		if p.isError {
			return p.result, fmt.Errorf("claude: %s", clip(p.errMsg, 500))
		}
		return p.result, nil
	}
	if runErr != nil {
		return p.result, runErr
	}
	return p.result, errors.New("claude exited without a result")
}

type claudeLine struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Model   string `json:"model"`
	Event   *struct {
		Type  string `json:"type"`
		Delta *struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Thinking string `json:"thinking"`
		} `json:"delta"`
		ContentBlock *struct {
			Type string `json:"type"`
		} `json:"content_block"`
	} `json:"event"`
	Message *struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	ParentToolUseID *string `json:"parent_tool_use_id"`
	Result          string  `json:"result"`
	IsError         bool    `json:"is_error"`
	TotalCostUSD    float64 `json:"total_cost_usd"`
	Errors          []any   `json:"errors"`
}

type claudeBlock struct {
	Type    string          `json:"type"`
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	IsError bool            `json:"is_error"`
	Content json.RawMessage `json:"content"`
}

type claudeParser struct {
	emit     func(api.RunOutput)
	roots    []root
	result   Result
	done     bool
	isError  bool
	errMsg   string
	sawText  bool
	newBlock bool
}

func (p *claudeParser) line(b []byte) {
	var l claudeLine
	if err := json.Unmarshal(b, &l); err != nil {
		return
	}
	// Ignore subagent chatter; only the main agent's stream is shown.
	if l.ParentToolUseID != nil && *l.ParentToolUseID != "" {
		return
	}
	switch l.Type {
	case "system":
		if l.Subtype == "init" {
			msg := "Claude Code started"
			if l.Model != "" {
				msg += " · " + l.Model
			}
			p.emit(api.RunOutput{Kind: "status", Text: msg})
		}
	case "stream_event":
		if l.Event == nil {
			return
		}
		switch l.Event.Type {
		case "content_block_start":
			if l.Event.ContentBlock != nil && l.Event.ContentBlock.Type == "text" {
				p.newBlock = true
			}
		case "content_block_delta":
			d := l.Event.Delta
			if d == nil {
				return
			}
			switch d.Type {
			case "text_delta":
				if d.Text == "" {
					return
				}
				if p.newBlock && p.sawText {
					p.emit(api.RunOutput{Kind: "text", Text: "\n\n"})
				}
				p.newBlock, p.sawText = false, true
				p.emit(api.RunOutput{Kind: "text", Text: d.Text})
			case "thinking_delta":
				if d.Thinking != "" {
					p.emit(api.RunOutput{Kind: "thinking", Text: d.Thinking})
				}
			}
		}
	case "assistant":
		for _, blk := range p.blocks(l) {
			if blk.Type == "tool_use" {
				p.emit(api.RunOutput{Kind: "tool", Tool: blk.Name, Detail: p.toolDetail(blk.Name, blk.Input)})
			}
		}
	case "user":
		for _, blk := range p.blocks(l) {
			if blk.Type == "tool_result" && blk.IsError {
				p.emit(api.RunOutput{Kind: "tool_error", Detail: clip(stripTags(contentText(blk.Content)), 300)})
			}
		}
	case "result":
		p.done = true
		p.result.Reply = strings.TrimSpace(l.Result)
		p.result.CostUSD = l.TotalCostUSD
		if l.IsError || (l.Subtype != "" && l.Subtype != "success") {
			p.isError = true
			p.errMsg = l.Result
			if p.errMsg == "" {
				p.errMsg = l.Subtype
				if len(l.Errors) > 0 {
					p.errMsg = fmt.Sprint(l.Errors...)
				}
			}
		}
	}
}

func (p *claudeParser) blocks(l claudeLine) []claudeBlock {
	if l.Message == nil {
		return nil
	}
	var bs []claudeBlock
	_ = json.Unmarshal(l.Message.Content, &bs)
	return bs
}

func (p *claudeParser) toolDetail(name string, raw json.RawMessage) string {
	var in map[string]any
	_ = json.Unmarshal(raw, &in)
	str := func(k string) string {
		if v, ok := in[k].(string); ok {
			return v
		}
		return ""
	}
	switch name {
	case "Read", "Write", "Edit", "MultiEdit", "NotebookEdit":
		return rel(str("file_path"), p.roots)
	case "Grep":
		d := fmt.Sprintf("%q", str("pattern"))
		if path := rel(str("path"), p.roots); path != "" {
			d += " in " + path
		}
		if g := str("glob"); g != "" {
			d += " (" + g + ")"
		}
		return d
	case "Glob":
		d := str("pattern")
		if path := rel(str("path"), p.roots); path != "" {
			d += " in " + path
		}
		return d
	}
	return clip(string(raw), 160)
}

func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(raw, &parts)
	var b strings.Builder
	for _, x := range parts {
		b.WriteString(x.Text)
	}
	return b.String()
}

func stripTags(s string) string {
	s = strings.ReplaceAll(s, "<tool_use_error>", "")
	return strings.ReplaceAll(s, "</tool_use_error>", "")
}
