package workflow

import (
	"fmt"
	"strings"

	"github.com/sam-jeston/braid/internal/api"
)

// MaxRepos caps how many repositories one team can reference.
const MaxRepos = 10

// NormalizeRepos validates a team's repository list. It trims fields, drops
// rows without a URL, derives missing names from URLs and makes names unique.
// Order is kept; the first repository is the primary one.
func NormalizeRepos(in []api.RepoRef) ([]api.RepoRef, error) {
	out := []api.RepoRef{}
	urls := map[string]bool{}
	names := map[string]bool{}
	for _, r := range in {
		r.URL, r.Branch = strings.TrimSpace(r.URL), strings.TrimSpace(r.Branch)
		if r.URL == "" {
			continue
		}
		if len(r.URL) > 500 || strings.HasPrefix(r.URL, "-") || strings.ContainsFunc(r.URL, isSpaceOrControl) {
			return nil, fmt.Errorf("%.80q isn't a valid repository URL or path", r.URL)
		}
		if !validBranch(r.Branch) {
			return nil, fmt.Errorf("%.80q isn't a valid branch name", r.Branch)
		}
		key := strings.ToLower(strings.TrimSuffix(strings.TrimRight(r.URL, "/"), ".git"))
		if urls[key] {
			return nil, fmt.Errorf("%s is listed twice", r.URL)
		}
		urls[key] = true
		name := slugName(r.Name)
		if name == "" {
			name = RepoName(r.URL)
		}
		r.Name = name
		for i := 2; names[r.Name]; i++ {
			r.Name = fmt.Sprintf("%s-%d", name, i)
		}
		names[r.Name] = true
		out = append(out, r)
	}
	if len(out) > MaxRepos {
		return nil, fmt.Errorf("a team can reference at most %d repositories", MaxRepos)
	}
	return out, nil
}

// RepoName derives a short name from a clone URL or local path, e.g.
// "git@github.com:acme/web.git" → "web".
func RepoName(url string) string {
	u := strings.TrimRight(strings.TrimSpace(url), `/\`)
	u = strings.TrimRight(strings.TrimSuffix(u, ".git"), `/\`)
	if i := strings.LastIndexAny(u, `/:\`); i >= 0 {
		u = u[i+1:]
	}
	if n := slugName(u); n != "" {
		return n
	}
	return "repo"
}

func slugName(s string) string {
	b := []byte(strings.ToLower(strings.TrimSpace(s)))
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			b[i] = '-'
		}
	}
	n := strings.Trim(string(b), "-_.")
	if len(n) > 40 {
		n = strings.Trim(n[:40], "-_.")
	}
	return n
}

// validBranch accepts empty (the remote's default) or a plausible git ref
// name. Branches reach git on every member's machine, so anything that could
// be read as an option is refused.
func validBranch(b string) bool {
	if b == "" {
		return true
	}
	return len(b) <= 200 && !strings.HasPrefix(b, "-") && !strings.HasPrefix(b, "/") && !strings.HasSuffix(b, "/") &&
		!strings.HasSuffix(b, ".lock") && !strings.Contains(b, "..") && !strings.Contains(b, "@{") &&
		!strings.ContainsAny(b, `~^:?*[\`) && !strings.ContainsFunc(b, isSpaceOrControl)
}

func isSpaceOrControl(r rune) bool { return r <= ' ' || r == 0x7f }

// Checkout is one repository checked out on the machine running the agent.
type Checkout struct {
	Name   string
	URL    string
	Dir    string
	Commit string
}

// RepoInfo describes the codebase checkouts available to a local run.
type RepoInfo struct {
	Checkouts []Checkout // available on this machine, in team order
	Missing   []string   // "name (reason)" for configured repositories this machine couldn't sync
	Note      string     // why there is no codebase at all, e.g. none configured
}

func (r RepoInfo) Available() bool { return len(r.Checkouts) > 0 }

// Unavailable explains why no checkout is available.
func (r RepoInfo) Unavailable() string {
	switch {
	case r.Note != "":
		return r.Note
	case len(r.Missing) > 0:
		return "couldn't access " + strings.Join(r.Missing, "; ")
	}
	return "no checkout configured"
}

// multi reports whether the team works across several repositories, in which
// case paths are cited with the repository name in front.
func (r RepoInfo) multi() bool { return len(r.Checkouts)+len(r.Missing) > 1 }

// Revision records what the agent saw, for Run.CommitSHA: the SHA for a
// single repository, otherwise "name@sha" pairs.
func (r RepoInfo) Revision() string {
	if !r.multi() && len(r.Checkouts) == 1 {
		return r.Checkouts[0].Commit
	}
	var parts []string
	for _, c := range r.Checkouts {
		if c.Commit != "" {
			parts = append(parts, c.Name+"@"+short(c.Commit))
		}
	}
	return strings.Join(parts, " ")
}
