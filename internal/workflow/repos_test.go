package workflow

import (
	"strings"
	"testing"

	"github.com/sam-jeston/braid/internal/api"
)

func TestRepoName(t *testing.T) {
	cases := map[string]string{
		"https://github.com/acme/web.git": "web",
		"git@github.com:acme/API.git":     "api",
		"github.com/acme/web/":            "web",
		"/home/sam/code/my app":           "my-app",
		`C:\code\billing`:                 "billing",
		"ssh://host/srv/repos/.git":       "repos",
		"":                                "repo",
	}
	for in, want := range cases {
		if got := RepoName(in); got != want {
			t.Errorf("RepoName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeRepos(t *testing.T) {
	got, err := NormalizeRepos([]api.RepoRef{
		{URL: " github.com/acme/web "},
		{URL: ""}, // blank rows from the UI are dropped
		{URL: "github.com/other/web", Branch: " release/2.0 "},
		{Name: "Payments API", URL: "git@github.com:acme/payments.git"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []api.RepoRef{
		{Name: "web", URL: "github.com/acme/web"},
		{Name: "web-2", URL: "github.com/other/web", Branch: "release/2.0"},
		{Name: "payments-api", URL: "git@github.com:acme/payments.git"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("repo %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	// Names that are already set survive a round trip unchanged.
	again, _ := NormalizeRepos(got)
	for i := range got {
		if again[i] != got[i] {
			t.Errorf("not idempotent: %+v → %+v", got[i], again[i])
		}
	}
}

func TestNormalizeReposRejects(t *testing.T) {
	many := make([]api.RepoRef, MaxRepos+1)
	for i := range many {
		many[i].URL = "github.com/acme/r" + strings.Repeat("x", i)
	}
	cases := map[string][]api.RepoRef{
		"option as branch":  {{URL: "github.com/a/b", Branch: "--upload-pack=touch /tmp/x"}},
		"space in branch":   {{URL: "github.com/a/b", Branch: "main; rm -rf"}},
		"ref syntax":        {{URL: "github.com/a/b", Branch: "main@{1}"}},
		"option as URL":     {{URL: "--upload-pack=x"}},
		"newline in URL":    {{URL: "github.com/a/b\nother"}},
		"duplicate URL":     {{URL: "https://github.com/a/b"}, {URL: "https://github.com/A/b.git"}},
		"too many":          many,
		"parent dir branch": {{URL: "github.com/a/b", Branch: "a/../b"}},
	}
	for name, in := range cases {
		if _, err := NormalizeRepos(in); err == nil {
			t.Errorf("%s: accepted %+v", name, in)
		}
	}
}

func TestPromptsDescribeRepos(t *testing.T) {
	one := RepoInfo{Checkouts: []Checkout{{Name: "app", Dir: "/r/app", Commit: "0123456789abcdef"}}}
	two := RepoInfo{
		Checkouts: []Checkout{{Name: "web", Dir: "/r/web", Commit: "aaaaaaaaaaaaaaaa"}},
		Missing:   []string{"api (couldn't clone)"},
	}
	none := RepoInfo{Note: "the team hasn't configured a repository"}

	if got := one.Revision(); got != "0123456789abcdef" {
		t.Errorf("single revision = %q", got)
	}
	if got := two.Revision(); got != "web@aaaaaaaaaa" {
		t.Errorf("multi revision = %q", got)
	}

	sys, _ := StagePrompt(PromptInput{Repo: one, Stage: api.StageSparring})
	if !strings.Contains(sys, "checked out READ-ONLY at `/r/app` (commit 0123456789)") || !strings.Contains(sys, "relative to the repo root") {
		t.Errorf("single-repo preamble:\n%s", sys)
	}
	sys, _ = StagePrompt(PromptInput{Repo: two, Stage: api.StageSparring})
	for _, want := range []string{"- `web` at `/r/web`", "prefixed with the repository name (e.g. `web/", "Not available on this machine: api (couldn't clone)"} {
		if !strings.Contains(sys, want) {
			t.Errorf("multi-repo preamble missing %q:\n%s", want, sys)
		}
	}
	sys, _ = StagePrompt(PromptInput{Repo: none, Stage: api.StageSparring})
	if !strings.Contains(sys, "NOT available on this machine (the team hasn't configured a repository)") {
		t.Errorf("no-repo preamble:\n%s", sys)
	}

	scanSys, scanUser := ScanPrompt(api.Team{Name: "Acme"}, two)
	if !strings.Contains(scanSys, "these repositories") || !strings.Contains(scanUser, "weren't scanned: api") || !strings.Contains(scanUser, "Commit: web@") {
		t.Errorf("multi-repo scan prompt:\n%s\n---\n%s", scanSys, scanUser)
	}
}
