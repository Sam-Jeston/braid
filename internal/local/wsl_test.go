//go:build !windows

package local

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sam-jeston/braid/internal/api"
	"github.com/sam-jeston/braid/internal/host"
	"github.com/sam-jeston/braid/internal/host/hosttest"
	"github.com/sam-jeston/braid/internal/server"
)

// fakeHosts offers this computer and an "Ubuntu" WSL distribution, run
// through a stand-in wsl.exe, whose home directory is home.
func fakeHosts(t *testing.T, home string) (hostFinder, string) {
	t.Helper()
	exe := hosttest.FakeWSL(t)
	w := &host.WSL{Distro: "Ubuntu", Exe: exe, Home: home, Mount: "/mnt/"}
	return hostFinder{
		options: func(context.Context) []host.Option {
			return []host.Option{{ID: host.LocalID, Label: "Windows"}, {ID: w.ID(), Label: w.Label()}}
		},
		resolve: func(ctx context.Context, id, data string) (host.Host, error) {
			switch id {
			case w.ID():
				return w, nil
			case "wsl:Gone":
				return nil, errors.New("couldn't start WSL (Gone): There is no distribution with the supplied name.")
			}
			return host.Resolve(ctx, id, data)
		},
		defaultDistro: func(context.Context) string { return "Ubuntu" },
	}, exe
}

func TestAgentHostChoice(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", "/usr/bin:/bin")
	wslHome := t.TempDir()
	claude := filepath.Join(wslHome, ".local", "bin", "claude")
	os.MkdirAll(filepath.Dir(claude), 0o755)
	os.WriteFile(claude, []byte("#!/bin/sh\necho '9.9.9 (Claude Code)'\n"), 0o755)

	c := newCore(t, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer c.Shutdown()
	c.hosts, _ = fakeHosts(t, wslHome)
	ctx := context.Background()

	// Agents installed only in WSL: Braid picks WSL by itself.
	c.refreshAgents(ctx)
	st := c.State()
	if st.AgentHost != "wsl:Ubuntu" || c.cfg.AgentHost != "wsl:Ubuntu" || len(st.AgentHosts) != 2 {
		t.Fatalf("host = %q (config %q), options %+v", st.AgentHost, c.cfg.AgentHost, st.AgentHosts)
	}
	if st.Agent != "claude" || !st.Agents[0].Available || st.Agents[0].Version != "9.9.9" {
		t.Fatalf("agents = %+v, chosen %q", st.Agents, st.Agent)
	}

	// An explicit choice sticks, even though the agents aren't on Windows.
	if err := c.UpdateSettings(SettingsPatch{AgentHost: ptr(host.LocalID)}); err != nil {
		t.Fatal(err)
	}
	c.refreshAgents(ctx)
	if st := c.State(); st.AgentHost != host.LocalID || st.Agents[0].Available {
		t.Errorf("after choosing Windows: host %q, agents %+v", st.AgentHost, st.Agents)
	}
	if _, _, _, _, err := c.pickAgent(ctx); err == nil || !strings.Contains(err.Error(), "on this machine") {
		t.Errorf("pick agent on Windows: %v", err)
	}

	for id, want := range map[string]string{"wsl:Debian": "click Detect again", "mars": "unknown place"} {
		if err := c.UpdateSettings(SettingsPatch{AgentHost: ptr(id)}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("choosing %q: %v", id, err)
		}
	}

	// A distro that has gone away says so when a run starts.
	c.mu.Lock()
	c.cfg.AgentHost = "wsl:Gone"
	c.mu.Unlock()
	c.refreshAgents(ctx)
	if st := c.State(); st.AgentHost != "wsl:Gone" || st.Agents[0].Available || !strings.Contains(st.Agents[0].Error, "no distribution") {
		t.Errorf("gone distro: host %q, agents %+v", st.AgentHost, st.Agents)
	}
	if _, _, _, _, err := c.pickAgent(ctx); err == nil || !strings.Contains(err.Error(), "choose where agents run in Settings") {
		t.Errorf("pick agent with a gone distro: %v", err)
	}
}

// A run on a WSL host clones and runs inside the distro, and says so.
func TestRunInWSL(t *testing.T) {
	t.Setenv("BRAID_FAKE_AGENT", "1")
	t.Setenv("BRAID_FAKE_DELAY_MS", "1")
	t.Setenv("BRAID_SECRETS", "file")
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, err := server.New(server.Config{DBPath: filepath.Join(t.TempDir(), "braid.db"), Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()
	setup, _ := srv.CreateSetupCode(ctx, time.Hour)

	c := newCore(t, log)
	defer c.Shutdown()
	wslHome := t.TempDir()
	var exe string
	c.hosts, exe = fakeHosts(t, wslHome)
	c.refreshAgents(ctx)
	if _, err := c.CreateTeam(ctx, CreateTeamInput{
		JoinInput: JoinInput{ServerURL: hs.URL, Code: setup, DisplayName: "Sam"},
		Roles:     api.AllRoles,
		Team:      api.TeamSettings{Name: "Acme", Repos: []api.RepoRef{{URL: makeRepo(t)}}, Output: api.OutputTarget{Type: api.OutputMarkdown}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.UpdateSettings(SettingsPatch{AgentHost: ptr("wsl:Ubuntu"), Agent: ptr("fake")}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "connected", func() bool { return c.State().Connection == "connected" })

	d := remote[api.ThreadDetail](t, c, "POST", "/v1/threads", api.CreateThreadRequest{Title: "Idea", Body: "An idea"})
	run, err := c.StartRun(ctx, StartRunInput{ThreadID: d.Thread.ID, Stage: api.StageSparring})
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, c, run.ID)
	out := remote[api.RunOutputResponse](t, c, "GET", "/v1/runs/"+run.ID+"/output", nil)
	if len(out.Items) == 0 || !strings.HasSuffix(out.Items[0].Text, "in WSL (Ubuntu)") {
		t.Errorf("the run should say where it ran: %+v", out.Items[:min(1, len(out.Items))])
	}
	st := c.State()
	if len(st.Repos) != 1 || st.Repos[0].State != "ready" || !strings.HasPrefix(st.Repos[0].Dir, filepath.Join(wslHome, ".local", "share", "braid", "repos")) {
		t.Fatalf("repos = %+v", st.Repos)
	}
	if b, _ := os.ReadFile(filepath.Join(filepath.Dir(exe), "distros")); !strings.Contains(string(b), "Ubuntu") {
		t.Error("git didn't run in the distro")
	}

	// A checkout typed as a WSL path is stored as Windows reaches it.
	c.mu.Lock()
	c.host = &host.WSL{Distro: "Ubuntu", Root: `\\wsl.localhost\Ubuntu`, Home: "/home/sam", Mount: "/mnt/"}
	c.mu.Unlock()
	name := st.Repos[0].Name
	if err := c.UpdateSettings(SettingsPatch{RepoOverrides: map[string]string{name: "/home/sam/code/app"}}); err != nil {
		t.Fatal(err)
	}
	if got := c.State().Repos[0].Override; got != `\\wsl.localhost\Ubuntu\home\sam\code\app` {
		t.Errorf("override stored as %q", got)
	}
}
