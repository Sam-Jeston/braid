package agents

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sam-jeston/braid/internal/api"
	"github.com/sam-jeston/braid/internal/host"
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
	p := &claudeParser{emit: emit, roots: Spec{Dir: "/work/run", Repos: []Repo{{Name: "app", Dir: "/work/repo"}}}.roots()}
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
	args := strings.Join((&Claude{}).Args(Spec{Dir: "/tmp/run", Repos: []Repo{{Name: "app", Dir: "/src/app/"}}, System: "sys", Model: "sonnet"}), " ")
	for _, want := range []string{"--restricted", "--add-dir /src/app", "Write(//src/app/**)", "Edit(//src/app/**)", "--model sonnet", "--permission-prompts none"} {
		if !strings.Contains(args, want) {
			t.Errorf("args missing %q: %s", want, args)
		}
	}
}

func TestClaudeArgsLockEveryRepo(t *testing.T) {
	args := strings.Join((&Claude{}).Args(Spec{Dir: "/tmp/run", Repos: []Repo{{Name: "web", Dir: "/src/web"}, {Name: "api", Dir: "/src/api"}}}), " ")
	want := "--add-dir /src/web /src/api --disallowedTools Write(//src/web/**) Edit(//src/web/**) Write(//src/api/**) Edit(//src/api/**)"
	if !strings.Contains(args, want) {
		t.Errorf("args = %s\nwant them to contain %s", args, want)
	}
}

func TestClaudeArgsWithoutRepo(t *testing.T) {
	args := strings.Join((&Claude{}).Args(Spec{Dir: "/tmp/run"}), " ")
	if strings.Contains(args, "--add-dir") || strings.Contains(args, "--disallowedTools") {
		t.Errorf("no repo, but args grant or deny one: %s", args)
	}
}

func TestRelLabelsPaths(t *testing.T) {
	one := Spec{Dir: "/run", Repos: []Repo{{Name: "app", Dir: "/repos/app/"}}}.roots()
	two := Spec{Dir: "/run", Repos: []Repo{{Name: "web", Dir: "/repos/web"}, {Name: "api", Dir: "/repos/api"}}}.roots()
	cases := []struct {
		roots []root
		in    string
		want  string
	}{
		{one, "/repos/app/src/main.go", "src/main.go"},
		{one, "/repos/app", ""},
		{one, "/run/brief.md", "brief.md"},
		{one, "/etc/hosts", "/etc/hosts"},
		{two, "/repos/api/server.go", "api/server.go"},
		{two, "/repos/web", "web"},
		{two, "/run/brief.md", "brief.md"},
	}
	for _, c := range cases {
		if got := rel(c.in, c.roots); got != c.want {
			t.Errorf("rel(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := relAll("rg auth /repos/web/src /repos/api/internal", two); got != "rg auth web/src api/internal" {
		t.Errorf("relAll = %q", got)
	}
}

func TestFakeReadsEveryRepo(t *testing.T) {
	webDir, apiDir := t.TempDir(), t.TempDir()
	for dir, file := range map[string]string{webDir: "index.ts", apiDir: "main.go"} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files := repoFiles(Spec{Repos: []Repo{{Name: "web", Dir: webDir}, {Name: "api", Dir: apiDir}}}, 6)
	if strings.Join(files, ",") != "web/index.ts,api/main.go" {
		t.Errorf("files = %v", files)
	}
}

func TestCodexParser(t *testing.T) {
	out, emit := collectOutputs()
	p := &codexParser{emit: emit, roots: Spec{Dir: "/work/run2", Repos: []Repo{{Name: "app", Dir: "/work/repo"}}}.roots()}
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

// With a WSL host, every path the agent is given is the distro's own, so the
// read-only rules apply to the files it actually opens.
func TestArgsUseTheHostsPaths(t *testing.T) {
	w := &host.WSL{Distro: "Ubuntu", Root: `\\wsl.localhost\Ubuntu`, Home: "/home/sam", Mount: "/mnt/"}
	data := `\\wsl.localhost\Ubuntu\home\sam\.local\share\braid`
	spec := Spec{Host: w, Dir: data + `\runs\r1`, Repos: []Repo{{Name: "app", Dir: data + `\repos\t\app`}, {Name: "web", Dir: `C:\code\web`}}}
	args := strings.Join((&Claude{}).Args(spec), " ")
	for _, want := range []string{
		"--add-dir /home/sam/.local/share/braid/repos/t/app /mnt/c/code/web",
		"Write(//home/sam/.local/share/braid/repos/t/app/**)", "Edit(//mnt/c/code/web/**)",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("claude args missing %q: %s", want, args)
		}
	}
	if args := strings.Join((&Codex{}).Args(spec), " "); !strings.Contains(args, "-C /home/sam/.local/share/braid/runs/r1") {
		t.Errorf("codex args: %s", args)
	}
	if got := rel("/home/sam/.local/share/braid/repos/t/app/src/x.ts", spec.roots()); got != "app/src/x.ts" {
		t.Errorf("activity paths should be relative to the repo: %q", got)
	}
}
