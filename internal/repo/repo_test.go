package repo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sam-jeston/braid/internal/api"
)

func gitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755)
		os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
	}
	run(t, dir, "init", "-q", "-b", "main")
	commit(t, dir, "init")
	return dir
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commit(t *testing.T, dir, msg string) {
	run(t, dir, "add", ".")
	run(t, dir, "-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-q", "-m", msg)
}

func TestSyncClonesAndUpdatesEachRepo(t *testing.T) {
	web := gitRepo(t, map[string]string{"index.ts": "1"})
	api_ := gitRepo(t, map[string]string{"main.go": "package main"})
	var changes []Status
	m := NewManager(t.TempDir(), func(_ string, st Status) { changes = append(changes, st) })
	ctx := context.Background()

	stWeb, err := m.Sync(ctx, "team_1", api.RepoRef{Name: "web", URL: web}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	stAPI, err := m.Sync(ctx, "team_1", api.RepoRef{Name: "api", URL: api_, Branch: "main"}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if stWeb.Dir == stAPI.Dir || filepath.Base(stWeb.Dir) != "web" || filepath.Base(stAPI.Dir) != "api" {
		t.Fatalf("checkouts should live side by side: %s, %s", stWeb.Dir, stAPI.Dir)
	}
	if _, err := os.Stat(filepath.Join(stAPI.Dir, "main.go")); err != nil {
		t.Fatal("api wasn't cloned")
	}
	if stWeb.Branch != "main" || stWeb.Commit != run(t, web, "rev-parse", "HEAD") || stWeb.Name != "web" {
		t.Errorf("web status = %+v", stWeb)
	}
	if m.Status("team_1", "api").State != "ready" || m.Status("team_1", "nope").State != "none" {
		t.Error("status isn't tracked per repository")
	}
	if len(changes) == 0 || changes[len(changes)-1].Name != "api" {
		t.Error("changes should be reported with the repository's name")
	}

	// A new upstream commit arrives on the next forced sync, and local edits are discarded.
	os.WriteFile(filepath.Join(web, "index.ts"), []byte("2"), 0o644)
	commit(t, web, "second")
	os.WriteFile(filepath.Join(stWeb.Dir, "stray.txt"), []byte("x"), 0o644)
	stWeb, err = m.Sync(ctx, "team_1", api.RepoRef{Name: "web", URL: web}, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if stWeb.Commit != run(t, web, "rev-parse", "HEAD") {
		t.Error("forced sync didn't fetch the new commit")
	}
	if _, err := os.Stat(filepath.Join(stWeb.Dir, "stray.txt")); err == nil {
		t.Error("sync should leave a pristine checkout")
	}
}

func TestSyncReplacesLegacySingleRepoLayout(t *testing.T) {
	base := t.TempDir()
	legacy := filepath.Join(base, "team_1")
	os.MkdirAll(filepath.Join(legacy, ".git"), 0o755)
	m := NewManager(base, nil)
	st, err := m.Sync(context.Background(), "team_1", api.RepoRef{Name: "app", URL: gitRepo(t, map[string]string{"a": "a"})}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if st.Dir != filepath.Join(legacy, "app") {
		t.Errorf("dir = %s", st.Dir)
	}
	if _, err := os.Stat(filepath.Join(legacy, ".git")); err == nil {
		t.Error("the old clone is still in place")
	}
	entries, _ := os.ReadDir(base)
	if len(entries) != 1 {
		t.Errorf("leftovers in %s: %v", base, entries)
	}
}

func TestSyncOverride(t *testing.T) {
	local := gitRepo(t, map[string]string{"a": "a"})
	m := NewManager(t.TempDir(), nil)
	st, err := m.Sync(context.Background(), "team_1", api.RepoRef{Name: "app", URL: "github.com/acme/app"}, local, false)
	if err != nil || st.State != "override" || st.Dir != local || st.Commit == "" {
		t.Fatalf("override = %+v, %v", st, err)
	}
	if _, err := m.Sync(context.Background(), "team_1", api.RepoRef{Name: "app"}, filepath.Join(local, "missing"), false); err == nil {
		t.Error("a missing override directory should fail")
	}
}

func TestSyncRefusesOptionLikeBranch(t *testing.T) {
	m := NewManager(t.TempDir(), nil)
	st, err := m.Sync(context.Background(), "team_1", api.RepoRef{Name: "app", URL: gitRepo(t, map[string]string{"a": "a"}), Branch: "--upload-pack=touch /tmp/x"}, "", false)
	if err == nil || st.State != "error" {
		t.Fatalf("sync = %+v, %v", st, err)
	}
}

func TestNormalizeURL(t *testing.T) {
	cases := map[string]string{
		"github.com/acme/app":         "https://github.com/acme/app",
		" gitlab.example.com/a/b ":    "https://gitlab.example.com/a/b",
		"git@github.com:acme/app.git": "git@github.com:acme/app.git",
		"https://github.com/acme/app": "https://github.com/acme/app",
		"/srv/git/app":                "/srv/git/app",
		"./app":                       "./app",
		"app":                         "app",
	}
	for in, want := range cases {
		if got := NormalizeURL(in); got != want {
			t.Errorf("NormalizeURL(%q) = %q, want %q", in, got, want)
		}
	}
}
