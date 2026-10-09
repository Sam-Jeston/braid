package agents

import (
	"bufio"
	"os"
	"strings"
	"testing"

	"github.com/sam-jeston/braid/internal/api"
)

func feed(t *testing.T, path string, line func([]byte)) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line(append([]byte(nil), sc.Bytes()...))
	}
}

func collectOutputs() (*[]api.RunOutput, func(api.RunOutput)) {
	var out []api.RunOutput
	return &out, func(o api.RunOutput) { out = append(out, o) }
}

// The fixture is a real `claude -p --output-format stream-json` capture in
// which the agent reads a repo file, writes a summary and is denied a write
// into the read-only repo.
func TestClaudeParser(t *testing.T) {
	out, emit := collectOutputs()
	p := &claudeParser{emit: emit, roots: []string{"/work/repo", "/work/run"}}
	feed(t, "testdata/claude.jsonl", p.line)

	if !p.done || p.isError {
		t.Fatalf("expected a successful result, got done=%v isError=%v", p.done, p.isError)
	}
	if !strings.Contains(p.result.Reply, "Summary written") || p.result.CostUSD <= 0 {
		t.Fatalf("unexpected result %+v", p.result)
	}
	var tools []string
	var text strings.Builder
	sawDenied := false
	for _, o := range *out {
		switch o.Kind {
		case "tool":
			tools = append(tools, o.Tool+":"+o.Detail)
		case "text":
			text.WriteString(o.Text)
		case "tool_error":
			sawDenied = strings.Contains(o.Detail, "denied")
		}
	}
	if len(tools) < 2 || tools[0] != "Read:math.ts" {
		t.Fatalf("unexpected tools %v", tools)
	}
	if !sawDenied {
		t.Error("expected the denied repo write to surface as a tool_error")
	}
	if text.Len() == 0 {
		t.Error("expected streamed text deltas")
	}
	if (*out)[0].Kind != "status" {
		t.Errorf("first event should be the init status, got %+v", (*out)[0])
	}
}

func TestClaudeArgsLockRepo(t *testing.T) {
	args := strings.Join((&Claude{}).Args(Spec{Dir: "/tmp/run", RepoDir: "/src/app/", System: "sys", Model: "sonnet"}), " ")
	for _, want := range []string{"--restricted", "--add-dir /src/app", "Write(//src/app/**)", "Edit(//src/app/**)", "--model sonnet", "--permission-prompts none"} {
		if !strings.Contains(args, want) {
			t.Errorf("args missing %q: %s", want, args)
		}
	}
}

func TestCodexParser(t *testing.T) {
	out, emit := collectOutputs()
	p := &codexParser{emit: emit, roots: []string{"/work/repo", "/work/run2"}}
	feed(t, "testdata/codex.jsonl", p.line)
	if !p.completed || p.failure != "" || strings.TrimSpace(p.last) != "done" {
		t.Fatalf("unexpected parser state %+v", p)
	}
	var shell, edit string
	for _, o := range *out {
		if o.Tool == "Shell" {
			shell = o.Detail
		}
		if o.Tool == "Edit" {
			edit = o.Detail
		}
	}
	if shell != "cat math.ts" {
		t.Errorf("shell detail = %q", shell)
	}
	if edit != "summary.md" {
		t.Errorf("edit detail = %q", edit)
	}
}

func TestUnwrapShell(t *testing.T) {
	cases := map[string]string{
		`/usr/bin/zsh -lc 'rg foo src'`: "rg foo src",
		`bash -lc "ls -la"`:             "ls -la",
		`git status`:                    "git status",
	}
	for in, want := range cases {
		if got := unwrapShell(in); got != want {
			t.Errorf("unwrapShell(%q) = %q, want %q", in, got, want)
		}
	}
}
