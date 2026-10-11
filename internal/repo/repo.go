// Package repo manages Braid's private, read-only checkouts of each team's
// repositories. Every member's app keeps its own clones (using that member's
// git credentials) so agents can read real code locally.
package repo

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sam-jeston/braid/internal/api"
	"github.com/sam-jeston/braid/internal/host"
)

type Status struct {
	Name     string     `json:"name"`  // the team's name for the repository
	State    string     `json:"state"` // none | cloning | syncing | ready | override | error
	Dir      string     `json:"dir"`
	Commit   string     `json:"commit"`
	URL      string     `json:"url"`
	Branch   string     `json:"branch"`
	Error    string     `json:"error,omitempty"`
	SyncedAt *time.Time `json:"synced_at,omitempty"`
}

func (s Status) Available() bool { return s.State == "ready" || s.State == "override" }

type Manager struct {
	onChange func(teamID string, st Status)

	mu     sync.Mutex
	host   host.Host // where git runs
	base   string    // Braid path of the clones
	locks  map[string]*sync.Mutex
	status map[string]Status
}

// NewManager keeps clones under base, running git on this computer.
func NewManager(base string, onChange func(string, Status)) *Manager {
	if onChange == nil {
		onChange = func(string, Status) {}
	}
	return &Manager{host: host.Local{}, base: base, onChange: onChange, locks: map[string]*sync.Mutex{}, status: map[string]Status{}}
}

// SetHost moves managed clones to base on h. Statuses are forgotten, so
// the next sync clones there.
func (m *Manager) SetHost(h host.Host, base string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.host.ID() == h.ID() && m.base == base {
		return
	}
	m.host, m.base, m.status = h, base, map[string]Status{}
}

func (m *Manager) where() (host.Host, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.host, m.base
}

func key(teamID, name string) string { return teamID + "/" + name }

func (m *Manager) lock(k string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.locks[k] == nil {
		m.locks[k] = &sync.Mutex{}
	}
	return m.locks[k]
}

func (m *Manager) set(teamID string, st Status) Status {
	m.mu.Lock()
	m.status[key(teamID, st.Name)] = st
	m.mu.Unlock()
	m.onChange(teamID, st)
	return st
}

// Status reports the checkout of one of the team's repositories.
func (m *Manager) Status(teamID, name string) Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	if st, ok := m.status[key(teamID, name)]; ok {
		return st
	}
	return Status{Name: name, State: "none"}
}

// NormalizeURL turns "github.com/org/repo" into a cloneable https URL and
// leaves ssh, file and local paths alone.
func NormalizeURL(u string) string {
	u = strings.TrimSpace(u)
	if u == "" || strings.Contains(u, "://") || strings.Contains(u, "@") || strings.HasPrefix(u, "/") || strings.HasPrefix(u, ".") || strings.HasPrefix(u, "~") {
		return u
	}
	if strings.Count(u, "/") >= 2 && strings.Contains(strings.SplitN(u, "/", 2)[0], ".") {
		return "https://" + u
	}
	return u
}

// Sync makes sure the checkout of one of the team's repositories exists and
// matches the remote branch. With override set, that directory is used as-is
// and never modified.
func (m *Manager) Sync(ctx context.Context, teamID string, r api.RepoRef, override string, force bool) (Status, error) {
	l := m.lock(key(teamID, r.Name))
	l.Lock()
	defer l.Unlock()
	url, branch := r.URL, r.Branch
	h, base := m.where()

	if override != "" {
		st := Status{Name: r.Name, State: "override", Dir: override, URL: url, Branch: branch}
		if fi, err := os.Stat(override); err != nil || !fi.IsDir() {
			st.State, st.Error = "error", "local checkout "+override+" not found"
			return m.set(teamID, st), fmt.Errorf("%s", st.Error)
		}
		st.Commit, _ = git(ctx, h, override, "rev-parse", "HEAD")
		now := time.Now()
		st.SyncedAt = &now
		return m.set(teamID, st), nil
	}
	url = NormalizeURL(url)
	if url == "" {
		return m.set(teamID, Status{Name: r.Name, State: "none", Error: "no repository URL configured"}), fmt.Errorf("no repository URL configured for %s", r.Name)
	}
	if strings.HasPrefix(branch, "-") {
		// Teams created before branch validation could still hold one.
		st := Status{Name: r.Name, State: "error", URL: url, Branch: branch, Error: "invalid branch name " + branch}
		return m.set(teamID, st), fmt.Errorf("%s", st.Error)
	}

	prev := m.Status(teamID, r.Name)
	if !force && prev.State == "ready" && prev.URL == url && prev.Branch == branch && prev.SyncedAt != nil && time.Since(*prev.SyncedAt) < 30*time.Second {
		return prev, nil
	}

	m.dropLegacyClone(base, teamID)
	dir := filepath.Join(base, safeName(teamID), safeName(r.Name))
	st := Status{Name: r.Name, Dir: dir, URL: url, Branch: branch}
	fail := func(err error) (Status, error) {
		st.State, st.Error = "error", err.Error()
		return m.set(teamID, st), err
	}

	if origin, err := git(ctx, h, dir, "config", "--get", "remote.origin.url"); err != nil || origin != url {
		os.RemoveAll(dir)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		st.State = "cloning"
		m.set(teamID, st)
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return fail(err)
		}
		tmp := dir + ".partial"
		os.RemoveAll(tmp)
		args := []string{"clone", "--filter=blob:none", "--single-branch"}
		if branch != "" {
			args = append(args, "--branch", branch)
		}
		args = append(args, "--", url, h.Path(tmp))
		cctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
		_, err := git(cctx, h, "", args...)
		cancel()
		if err != nil {
			os.RemoveAll(tmp)
			return fail(fmt.Errorf("couldn't clone %s: %w", url, err))
		}
		if err := os.Rename(tmp, dir); err != nil {
			return fail(err)
		}
	} else {
		st.State = "syncing"
		m.set(teamID, st)
		ref := "HEAD"
		if branch != "" {
			ref = branch
		}
		fctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		_, err := git(fctx, h, dir, "fetch", "--prune", "--filter=blob:none", "--", "origin", ref)
		cancel()
		if err != nil {
			return fail(fmt.Errorf("couldn't fetch %s: %w", url, err))
		}
		if _, err := git(ctx, h, dir, "reset", "--hard", "FETCH_HEAD"); err != nil {
			return fail(err)
		}
		if _, err := git(ctx, h, dir, "clean", "-ffdx"); err != nil {
			return fail(err)
		}
	}
	sha, err := git(ctx, h, dir, "rev-parse", "HEAD")
	if err != nil {
		return fail(err)
	}
	if branch == "" {
		st.Branch, _ = git(ctx, h, dir, "rev-parse", "--abbrev-ref", "HEAD")
	}
	now := time.Now()
	st.State, st.Commit, st.SyncedAt, st.Error = "ready", sha, &now, ""
	return m.set(teamID, st), nil
}

// dropLegacyClone removes a checkout in the layout used before teams could
// have several repositories, when <base>/<team> was itself the clone.
func (m *Manager) dropLegacyClone(base, teamID string) {
	dir := filepath.Join(base, safeName(teamID))
	m.mu.Lock()
	var old string
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		old = fmt.Sprintf("%s.legacy-%d", dir, time.Now().UnixNano())
		if os.Rename(dir, old) != nil {
			old = ""
		}
	}
	m.mu.Unlock()
	if old != "" {
		os.RemoveAll(old)
	}
}

// git runs on h in dir (a Braid path; "" for any).
func git(ctx context.Context, h host.Host, dir string, args ...string) (string, error) {
	env := []string{"GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never"}
	if h.ID() != host.LocalID || os.Getenv("GIT_SSH_COMMAND") == "" {
		// The app can't answer ssh prompts; fail fast instead of hanging.
		env = append(env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
	}
	cmd := h.Command(ctx, "git", args, dir, env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		if lines := strings.Split(msg, "\n"); len(lines) > 4 {
			msg = strings.Join(lines[len(lines)-4:], "\n")
		}
		return "", fmt.Errorf("%s", msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}

func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, s)
}
