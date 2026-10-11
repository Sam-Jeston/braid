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
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sam-jeston/braid/internal/api"
	"github.com/sam-jeston/braid/internal/host"
)

// Spec describes one agent invocation. Dirs are Braid's paths; the agent
// gets them as Host.Path maps them.
type Spec struct {
	Host   host.Host // where the CLI runs; nil means this computer
	Dir    string    // writable working directory (Braid scratch space)
	Repos  []Repo    // read-only codebase checkouts; empty when unavailable
	System string    // appended to the agent's system prompt
	Prompt string    // task + context, sent on stdin
	Model  string    // optional model override
	Stage  string    // informational
}

func (s Spec) host() host.Host {
	if s.Host == nil {
		return host.Local{}
	}
	return s.Host
}

// Repo is one read-only checkout the agent may read.
type Repo struct {
	Name string
	Dir  string
}

// roots maps the directories an agent touches, as the agent sees them, to
// the labels shown in run activity: paths in the run dir and in a lone repo
// are shown bare, and with several repos they're prefixed by the repo's name.
func (s Spec) roots() []root {
	h := s.host()
	var out []root
	for _, r := range s.Repos {
		label := ""
		if len(s.Repos) > 1 {
			label = r.Name
		}
		out = append(out, root{dir: h.Path(r.Dir), label: label})
	}
	return append(out, root{dir: h.Path(s.Dir)})
}

type root struct{ dir, label string }

type Result struct {
	Reply   string
	CostUSD float64
}

type Agent interface {
	ID() string
	Name() string
	// Detect returns the version installed on h, or an error if unavailable.
	Detect(ctx context.Context, h host.Host) (string, error)
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

// Detect reports which agents are installed on h.
func Detect(ctx context.Context, h host.Host) []Info {
	var out []Info
	for _, a := range All() {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		v, err := a.Detect(ctx, h)
		cancel()
		info := Info{ID: a.ID(), Name: a.Name(), Available: err == nil, Version: v}
		if err != nil {
			info.Error = err.Error()
		}
		out = append(out, info)
	}
	return out
}

func version(ctx context.Context, h host.Host, bin string, args ...string) (string, error) {
	out, err := h.Command(ctx, bin, args, "").Output()
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

// runLines starts bin on h in dir, feeds stdin, and calls onLine for every
// stdout line. Cancelling ctx interrupts the whole process group.
func runLines(ctx context.Context, h host.Host, bin string, args []string, dir, stdin string, onLine func([]byte)) error {
	// Not ctx: cancelling interrupts first and only kills after a grace period.
	cmd := h.Command(context.Background(), bin, args, dir)
	cmd.Stdin = strings.NewReader(stdin)
	stderr := &tail{n: 8 << 10}
	cmd.Stderr = stderr
	host.SetProcessGroup(cmd)
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
			host.InterruptGroup(cmd)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				host.KillGroup(cmd)
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
				host.KillGroup(cmd)
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
// rel labels an absolute path for run activity. Paths are compared with
// forward slashes so Windows paths shorten too.
func rel(p string, roots []root) string {
	p = filepath.ToSlash(p)
	for _, r := range roots {
		dir := strings.TrimRight(filepath.ToSlash(r.dir), "/")
		if dir == "" {
			continue
		}
		if p == dir {
			return r.label
		}
		if x, ok := strings.CutPrefix(p, dir+"/"); ok {
			if r.label != "" {
				return r.label + "/" + x
			}
			return x
		}
	}
	return p
}

// relAll shortens every occurrence of the roots inside a command line.
func relAll(s string, roots []root) string {
	for _, r := range roots {
		dir := strings.TrimRight(filepath.ToSlash(r.dir), "/")
		if dir == "" {
			continue
		}
		prefix := ""
		if r.label != "" {
			prefix = r.label + "/"
		}
		s = strings.ReplaceAll(s, dir+"/", prefix)
		if native := filepath.FromSlash(dir); native != dir {
			s = strings.ReplaceAll(s, native+string(filepath.Separator), prefix)
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
