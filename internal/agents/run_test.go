//go:build !windows

package agents

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sam-jeston/braid/internal/api"
	"github.com/sam-jeston/braid/internal/host"
	"github.com/sam-jeston/braid/internal/host/hosttest"
)

// stubCLI puts an executable shell script called name first on PATH.
func stubCLI(t *testing.T, name, script string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func TestClaudeRunStreamsFixture(t *testing.T) {
	fixture, _ := filepath.Abs("testdata/claude.jsonl")
	work := t.TempDir()
	// The prompt arrives on stdin and the agent runs in the scratch dir.
	stubCLI(t, "claude", `cat > "$PWD/prompt.txt"; printf '%s\n' "$@" > "$PWD/args.txt"; cat '`+fixture+`'`)
	var out []api.RunOutput
	res, err := (&Claude{}).Run(context.Background(), Spec{Dir: work, Prompt: "Spar on this idea", System: "sys"}, func(o api.RunOutput) { out = append(out, o) })
	if err != nil {
		t.Fatal(err)
	}
	if res.Reply == "" || len(out) == 0 {
		t.Fatalf("reply %q, %d outputs", res.Reply, len(out))
	}
	if b, _ := os.ReadFile(filepath.Join(work, "prompt.txt")); string(b) != "Spar on this idea" {
		t.Errorf("stdin = %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(work, "args.txt")); !strings.Contains(string(b), "--restricted") || strings.Contains(string(b), "Spar on this idea") {
		t.Errorf("the prompt must go over stdin, not argv: %s", b)
	}
}

func TestClaudeRunReportsCrash(t *testing.T) {
	stubCLI(t, "claude", `cat > /dev/null; echo "authentication required" >&2; exit 3`)
	_, err := (&Claude{}).Run(context.Background(), Spec{Dir: t.TempDir(), Prompt: "x"}, func(api.RunOutput) {})
	if err == nil || !strings.Contains(err.Error(), "authentication required") {
		t.Fatalf("error should carry the CLI's stderr: %v", err)
	}
}

func TestCodexRunStreamsFixture(t *testing.T) {
	fixture, _ := filepath.Abs("testdata/codex.jsonl")
	work := t.TempDir()
	stubCLI(t, "codex", `cat > "$PWD/prompt.txt"; cat '`+fixture+`'`)
	var out []api.RunOutput
	res, err := (&Codex{}).Run(context.Background(), Spec{Dir: work, Prompt: "Plan it", System: "Be terse"}, func(o api.RunOutput) { out = append(out, o) })
	if err != nil {
		t.Fatal(err)
	}
	if res.Reply == "" || len(out) == 0 {
		t.Fatalf("reply %q, %d outputs", res.Reply, len(out))
	}
	// Codex has no system prompt flag, so it's prepended to stdin.
	if b, _ := os.ReadFile(filepath.Join(work, "prompt.txt")); !strings.Contains(string(b), "<instructions>\nBe terse\n</instructions>") || !strings.HasSuffix(string(b), "Plan it") {
		t.Errorf("stdin = %q", b)
	}
	args := strings.Join((&Codex{}).Args(Spec{Dir: work, Model: "gpt-5"}), " ")
	for _, want := range []string{"-s workspace-write", "-C " + work, "-m gpt-5", "--ephemeral"} {
		if !strings.Contains(args, want) {
			t.Errorf("codex args missing %q: %s", want, args)
		}
	}
}

func TestRunCancelStopsTheProcessGroup(t *testing.T) {
	// The CLI starts a background child, which ignores SIGINT like many
	// tool subprocesses do, so cancelling has to escalate to SIGKILL.
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	stubCLI(t, "claude", `sh -c 'echo $$ > `+pidFile+`; exec sleep 60' & sleep 60`)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := (&Claude{}).Run(ctx, Spec{Dir: t.TempDir(), Prompt: "x"}, func(api.RunOutput) {})
		done <- err
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("run didn't stop after cancel")
	}
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	for i := 0; syscall.Kill(pid, 0) == nil; i++ {
		if i > 40 {
			t.Fatalf("child %d outlived the cancelled run", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestDetect(t *testing.T) {
	stubCLI(t, "claude", `echo "9.9.9 (Claude Code)"`)
	v, err := (&Claude{}).Detect(context.Background(), host.Local{})
	if err != nil || v != "9.9.9" {
		t.Errorf("claude version = %q, %v", v, err)
	}
	t.Setenv("BRAID_FAKE_AGENT", "1")
	infos := Detect(context.Background(), host.Local{})
	if len(infos) != 3 || infos[0].ID != "claude" || !infos[0].Available || infos[2].ID != "fake" {
		t.Errorf("detect = %+v", infos)
	}
	if _, ok := Get("nope"); ok {
		t.Error("unknown agent found")
	}
}

// In WSL the CLI is found in its usual install dir (wsl.exe's PATH doesn't
// include it), runs in the run dir, and still gets the prompt on stdin.
func TestClaudeRunsInWSL(t *testing.T) {
	fixture, _ := filepath.Abs("testdata/claude.jsonl")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/usr/bin:/bin")
	claude := filepath.Join(home, ".local", "bin", "claude")
	if err := os.MkdirAll(filepath.Dir(claude), 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
case $1 in --version) echo "2.1.0 (Claude Code)"; exit ;; esac
cat > "$PWD/prompt.txt"; printf '%s\n' "$@" > "$PWD/args.txt"; cat '` + fixture + `'`
	if err := os.WriteFile(claude, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	w := &host.WSL{Distro: "Ubuntu", Exe: hosttest.FakeWSL(t), Home: home, Mount: "/mnt/"}
	if v, err := (&Claude{}).Detect(context.Background(), w); err != nil || v != "2.1.0" {
		t.Fatalf("detect in WSL = %q, %v", v, err)
	}
	work, repo := t.TempDir(), t.TempDir()
	res, err := (&Claude{}).Run(context.Background(), Spec{Host: w, Dir: work, Repos: []Repo{{Name: "app", Dir: repo}}, Prompt: "Spar on this idea"}, func(api.RunOutput) {})
	if err != nil || res.Reply == "" {
		t.Fatalf("reply %q, %v", res.Reply, err)
	}
	if b, _ := os.ReadFile(filepath.Join(work, "prompt.txt")); string(b) != "Spar on this idea" {
		t.Errorf("stdin = %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(work, "args.txt")); !strings.Contains(string(b), "Write(/"+repo+"/**)") {
		t.Errorf("the repo must stay read-only: %s", b)
	}
}
