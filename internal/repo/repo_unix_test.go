//go:build !windows

package repo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sam-jeston/braid/internal/api"
	"github.com/sam-jeston/braid/internal/host"
	"github.com/sam-jeston/braid/internal/host/hosttest"
)

// Clones made for a WSL host are made by git inside the distro.
func TestSyncThroughWSL(t *testing.T) {
	src := gitRepo(t, map[string]string{"main.go": "package main"})
	exe := hosttest.FakeWSL(t)
	m := NewManager(t.TempDir(), nil)
	ctx := context.Background()
	if _, err := m.Sync(ctx, "team_1", api.RepoRef{Name: "app", URL: src}, "", false); err != nil {
		t.Fatal(err)
	}

	base := filepath.Join(t.TempDir(), "repos")
	m.SetHost(&host.WSL{Distro: "Ubuntu", Exe: exe, Home: t.TempDir(), Mount: "/mnt/"}, base)
	if st := m.Status("team_1", "app"); st.State != "none" {
		t.Errorf("switching hosts should forget the old clone: %+v", st)
	}
	st, err := m.Sync(ctx, "team_1", api.RepoRef{Name: "app", URL: src}, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if st.Dir != filepath.Join(base, "team_1", "app") || st.Commit != run(t, src, "rev-parse", "HEAD") {
		t.Errorf("status = %+v", st)
	}
	if _, err := os.Stat(filepath.Join(st.Dir, "main.go")); err != nil {
		t.Error("not cloned")
	}
	if b, _ := os.ReadFile(filepath.Join(filepath.Dir(exe), "distros")); !strings.Contains(string(b), "Ubuntu") {
		t.Error("git didn't run in the distro")
	}
	if st, err := m.Sync(ctx, "team_1", api.RepoRef{Name: "app", URL: src}, src, false); err != nil || st.Commit == "" {
		t.Errorf("override through WSL = %+v, %v", st, err)
	}
}
