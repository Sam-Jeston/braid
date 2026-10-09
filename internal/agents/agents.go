// Package agents runs local coding-agent CLIs (Claude Code, Codex) headlessly
// and normalizes their streaming output into Braid run events.
package agents

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sam-jeston/braid/internal/api"
)

// Spec describes one agent invocation.
type Spec struct {
	Dir     string // writable working directory (Braid scratch space)
	RepoDir string // read-only codebase checkout; empty when unavailable
	System  string // appended to the agent's system prompt
	Prompt  string // task + context, sent on stdin
	Model   string // optional model override
	Stage   string // informational
}

type Result struct {
	Reply   string
	CostUSD float64
}

type Agent interface {
	ID() string
	Name() string
	// Detect returns the installed version, or an error if unavailable.
	Detect(ctx context.Context) (string, error)
	Run(ctx context.Context, spec Spec, emit func(api.RunOutput)) (Result, error)
}

type Info struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Version   string `json:"version"`
	Error     string `json:"error,omitempty"`
}

// All returns the supported agents. The fake agent is only offered when
// BRAID_FAKE_AGENT=1 (tests and demos).
func All() []Agent {
	out := []Agent{&Claude{}, &Codex{}}
	if os.Getenv("BRAID_FAKE_AGENT") == "1" {
		out = append(out, &Fake{})
	}
	return out
}

func Get(id string) (Agent, bool) {
	for _, a := range All() {
		if a.ID() == id {
			return a, true
		}
	}
	return nil, false
}

func Detect(ctx context.Context) []Info {
	var out []Info
	for _, a := range All() {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		v, err := a.Detect(ctx)
		cancel()
		info := Info{ID: a.ID(), Name: a.Name(), Available: err == nil, Version: v}
		if err != nil {
			info.Error = err.Error()
		}
		out = append(out, info)
	}
	return out
}

// sessionEnv strips variables that would make a child CLI think it is nested
// inside another agent session.
func sessionEnv() []string {
	drop := map[string]bool{
		"CLAUDECODE": true, "CLAUDE_CODE_ENTRYPOINT": true, "CLAUDE_CODE_SESSION_ID": true,
		"CLAUDE_CODE_CHILD_SESSION": true, "CLAUDE_CODE_MESSAGING_SOCKET": true, "CLAUDE_CODE_MESSAGING_TOKEN": true,
		"CLAUDE_CODE_SESSION_ATTENDED": true, "CLAUDE_PID": true, "CLAUDE_EFFORT": true, "CLAUDE_CODE_EXECPATH": true,
		"AI_AGENT": true, "CODEX_THREAD_ID": true, "CODEX_SANDBOX": true,
	}
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if !drop[k] {
			env = append(env, kv)
		}
	}
	return env
}

// lookPath finds a CLI on PATH, falling back to common install locations
// because GUI apps often start with a minimal PATH.
func lookPath(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(home, ".local", "bin", name),
		filepath.Join(home, ".claude", "local", name),
		filepath.Join(home, ".npm-global", "bin", name),
		filepath.Join(home, ".bun", "bin", name),
		"/opt/homebrew/bin/" + name,
		"/usr/local/bin/" + name,
	}
	if nvm, _ := filepath.Glob(filepath.Join(home, ".nvm", "versions", "node", "*", "bin", name)); len(nvm) > 0 {
		candidates = append(candidates, nvm[len(nvm)-1])
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c, nil
		}
	}
	return "", fmt.Errorf("%s not found on PATH", name)
}

func version(ctx context.Context, bin string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = sessionEnv()
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s %s failed: %w", filepath.Base(bin), strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// tail keeps the last n bytes written to it.
type tail struct {
	mu  sync.Mutex
	n   int
	buf []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.n {
		t.buf = t.buf[len(t.buf)-t.n:]
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf))
}

// runLines starts bin, feeds stdin, and calls onLine for every stdout line.
// Cancelling ctx interrupts the whole process group.
func runLines(ctx context.Context, bin string, args []string, dir, stdin string, onLine func([]byte)) error {
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = sessionEnv()
	cmd.Stdin = strings.NewReader(stdin)
	stderr := &tail{n: 8 << 10}
	cmd.Stderr = stderr
	setProcessGroup(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", filepath.Base(bin), err)
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			interruptGroup(cmd)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				killGroup(cmd)
			}
		case <-done:
		}
	}()
	rd := bufio.NewReaderSize(stdout, 1<<20)
	for {
		line, err := rd.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			onLine(line)
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && ctx.Err() == nil {
				killGroup(cmd)
			}
			break
		}
	}
	werr := cmd.Wait()
	close(done)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if werr != nil {
		if msg := stderr.String(); msg != "" {
			return fmt.Errorf("%s exited: %v: %s", filepath.Base(bin), werr, lastLines(msg, 6))
		}
		return fmt.Errorf("%s exited: %v", filepath.Base(bin), werr)
	}
	return nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// rel shortens absolute paths for display; a root itself becomes "".
func rel(p string, roots ...string) string {
	for _, r := range roots {
		if r == "" {
			continue
		}
		r = strings.TrimRight(r, "/")
		if p == r {
			return ""
		}
		if x, ok := strings.CutPrefix(p, r+"/"); ok {
			return x
		}
	}
	return p
}

// relAll shortens every occurrence of the roots inside a command line.
func relAll(s string, roots ...string) string {
	for _, r := range roots {
		if r != "" {
			s = strings.ReplaceAll(s, strings.TrimRight(r, "/")+"/", "")
		}
	}
	return s
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
