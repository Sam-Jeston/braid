// Package host runs the external tools Braid drives (agent CLIs and git),
// either on this computer or inside a WSL distribution, where Windows users
// usually keep those tools and their logins. It also maps paths between
// Braid's view of the filesystem and the tool's.
package host

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Host is where external tools run.
type Host interface {
	// ID is "local" or "wsl:<distro>", as stored in config.json.
	ID() string
	// Label names the host for people, e.g. "WSL (Ubuntu)".
	Label() string
	// Path converts a path Braid uses into the path the tool sees.
	Path(p string) string
	// FromToolPath converts a path written the way the tool sees it, like
	// /home/me/code/app inside WSL, into the path Braid uses.
	FromToolPath(p string) string
	// DataDir is where Braid keeps managed clones and run dirs for this host.
	DataDir() string
	// Command prepares bin to run with args in dir (a Braid path; "" for
	// any). env holds extra KEY=value pairs.
	Command(ctx context.Context, bin string, args []string, dir string, env ...string) *exec.Cmd
	// LookPath finds a CLI on PATH or in a common install location.
	LookPath(ctx context.Context, name string) (string, error)
}

// LocalID is the ID of the host Braid itself runs on.
const LocalID = "local"

// Option is a host the member can choose to run agents on.
type Option struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Options lists the hosts agents can run on: this computer and, on Windows,
// each WSL distribution.
func Options(ctx context.Context) []Option {
	out := []Option{{ID: LocalID, Label: Local{}.Label()}}
	if runtime.GOOS != "windows" {
		return out
	}
	return append(out, wslOptions(ctx, wslExe)...)
}

// Resolve returns the host with the given ID. data is Braid's own data dir,
// used by the local host.
func Resolve(ctx context.Context, id, data string) (Host, error) {
	if id == "" || id == LocalID {
		return Local{Data: data}, nil
	}
	if distro, ok := strings.CutPrefix(id, "wsl:"); ok && distro != "" {
		return NewWSL(ctx, wslExe, distro)
	}
	return nil, fmt.Errorf("unknown place to run agents %q", id)
}

// Local runs tools directly on this computer.
type Local struct {
	Data string
}

func (Local) ID() string { return LocalID }

func (Local) Label() string {
	if runtime.GOOS == "windows" {
		return "Windows"
	}
	return "this computer"
}

func (Local) Path(p string) string         { return p }
func (Local) FromToolPath(p string) string { return p }
func (l Local) DataDir() string            { return l.Data }

func (Local) Command(ctx context.Context, bin string, args []string, dir string, env ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = append(cleanEnv(), env...)
	hideWindow(cmd)
	return cmd
}

// LookPath falls back to common install locations because GUI apps often
// start with a minimal PATH.
func (Local) LookPath(_ context.Context, name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	home, _ := os.UserHomeDir()
	found := ""
	for _, c := range candidates(home, name, filepath.Join) {
		if strings.Contains(c, "*") {
			// Only the nvm pattern has a wildcard; prefer its newest node.
			if m, _ := filepath.Glob(c); len(m) > 0 {
				c = m[len(m)-1]
			} else {
				continue
			}
		}
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			found = c
			break
		}
	}
	if found == "" {
		return "", fmt.Errorf("%s not found on PATH", name)
	}
	return found, nil
}

// candidates are the usual install locations of a CLI outside PATH, in the
// order they're tried. The last one is a glob over nvm's node versions.
func candidates(home, name string, join func(...string) string) []string {
	return []string{
		join(home, ".local", "bin", name),
		join(home, ".claude", "local", name),
		join(home, ".npm-global", "bin", name),
		join(home, ".bun", "bin", name),
		"/opt/homebrew/bin/" + name,
		"/usr/local/bin/" + name,
		join(home, ".nvm", "versions", "node", "*", "bin", name),
	}
}

// cleanEnv is this process's environment without the variables that would
// make a child CLI think it is nested inside another agent session. Add to
// the list rather than clearing the environment: users' provider settings
// (Bedrock, proxies) live in env vars.
func cleanEnv() []string {
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
