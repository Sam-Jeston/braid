package local

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/sam-jeston/braid/internal/api"
	"github.com/sam-jeston/braid/internal/server"
)

// TestPipeline drives the whole product loop with two teammates on separate
// "machines" (separate Braid homes) against one real braid-server:
// create team → scan → invite → thread → spar → design → technical → plan → publish.
func TestPipeline(t *testing.T) {
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
	setup, err := srv.CreateSetupCode(ctx, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	repoDir := makeRepo(t)

	// Priya (product + admin) creates the team.
	priya := newCore(t, log)
	defer priya.Shutdown()
	_, err = priya.CreateTeam(ctx, CreateTeamInput{
		JoinInput: JoinInput{ServerURL: hs.URL, Code: setup, DisplayName: "Priya", Email: "priya@example.com"},
		Roles:     []api.Role{api.RoleProduct},
		Team:      api.TeamSettings{Name: "Acme", Repos: []api.RepoRef{{URL: repoDir}}, Output: api.OutputTarget{Type: api.OutputMarkdown}},
	})
	if err != nil {
		t.Fatalf("create team: %v", err)
	}
	if err := priya.UpdateSettings(SettingsPatch{Agent: ptr("fake")}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "priya connected", func() bool { return priya.State().Connection == "connected" })

	// The setup code is single use.
	other := newCore(t, log)
	defer other.Shutdown()
	if _, err := other.CreateTeam(ctx, CreateTeamInput{JoinInput: JoinInput{ServerURL: hs.URL, Code: setup, DisplayName: "Mallory"}, Roles: []api.Role{api.RoleProduct}, Team: api.TeamSettings{Name: "X", Output: api.OutputTarget{Type: "markdown"}}}); err == nil {
		t.Fatal("setup code was accepted twice")
	}

	// Initial codebase scan updates the team brief.
	scan, err := priya.StartScan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, priya, scan.ID)
	team := remote[api.Team](t, priya, "GET", "/v1/team", nil)
	if !strings.Contains(team.CodebaseBrief, "Codebase brief") || team.ScanCommit == "" {
		t.Fatalf("scan didn't update team: %+v", team)
	}

	// Invite Dev (design + engineering).
	inv := remote[api.CreateInviteResponse](t, priya, "POST", "/v1/invites", api.CreateInviteRequest{Roles: []api.Role{api.RoleDesign, api.RoleEngineering}})
	dev := newCore(t, log)
	defer dev.Shutdown()
	if _, err := dev.Join(ctx, JoinInput{ServerURL: hs.URL, Code: strings.ToLower(inv.Code), DisplayName: "Dev"}); err != nil {
		t.Fatalf("join: %v", err)
	}
	dev.UpdateSettings(SettingsPatch{Agent: ptr("fake")})
	waitFor(t, "dev connected", func() bool { return dev.State().Connection == "connected" })

	// Dev's UI stream sees what Priya does.
	devEvents, unsub := dev.Bus().Subscribe()
	defer unsub()

	// Priya starts a thread and spars on her machine.
	detail := remote[api.ThreadDetail](t, priya, "POST", "/v1/threads", api.CreateThreadRequest{Title: "Weekly digest emails", Body: "Send managers a weekly digest of team activity."})
	th := detail.Thread.ID
	run, err := priya.StartRun(ctx, StartRunInput{ThreadID: th, Stage: api.StageSparring, TriggerMessageID: detail.Messages[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	// A second run on the same stage is rejected while the first is active.
	if _, err := dev.StartRun(ctx, StartRunInput{ThreadID: th, Stage: api.StageSparring}); err == nil {
		t.Log("note: first run finished before the second started")
	}
	waitRun(t, priya, run.ID)
	sawOutput := false
	timeout := time.After(5 * time.Second)
	for !sawOutput {
		select {
		case b := <-devEvents:
			var ev api.Event
			json.Unmarshal(b, &ev)
			if ev.Type == api.EvRunOutput && ev.RunID == run.ID {
				sawOutput = true
			}
		case <-timeout:
			t.Fatal("dev never saw priya's agent output in realtime")
		}
	}

	// Approvals are role-gated.
	if _, err := proxyErr(dev, "POST", "/v1/threads/"+th+"/stages/sparring/approve"); err == nil {
		t.Fatal("designer approved sparring")
	}
	remote[api.Thread](t, priya, "POST", "/v1/threads/"+th+"/stages/sparring/approve", nil)

	// Dev runs design on their machine, selects a variation, approves.
	run, err = dev.StartRun(ctx, StartRunInput{ThreadID: th, Stage: api.StageDesign})
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, dev, run.ID)
	if _, err := proxyErr(dev, "POST", "/v1/threads/"+th+"/stages/design/approve"); err == nil {
		t.Fatal("design approved without a selected variation")
	}
	remote[api.Thread](t, dev, "POST", "/v1/threads/"+th+"/stages/design/selection", api.SelectionRequest{Path: "mockups/compact-list.html"})
	remote[api.Thread](t, dev, "POST", "/v1/threads/"+th+"/stages/design/approve", nil)

	run, err = dev.StartRun(ctx, StartRunInput{ThreadID: th, Stage: api.StageTechnical})
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, dev, run.ID)
	thread := remote[api.Thread](t, dev, "POST", "/v1/threads/"+th+"/stages/technical/approve", nil)
	if thread.CurrentStage != api.StagePlan || thread.StageState(api.StagePlan).Status != api.StageOpen {
		t.Fatalf("plan stage not open: %+v", thread)
	}

	// Plan generation and publishing.
	run, err = dev.StartRun(ctx, StartRunInput{ThreadID: th, Stage: api.StagePlan})
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, dev, run.ID)
	res, err := priya.Publish(ctx, th)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if len(res.Export.Items) != 3 || res.Folder == "" {
		t.Fatalf("unexpected export: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(res.Folder, "README.md")); err != nil {
		t.Fatal(err)
	}
	// Anyone can also save the plan as Markdown on their own machine.
	folder, err := dev.SaveMarkdown(ctx, th)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(folder, "README.md")); err != nil || !strings.Contains(string(b), "#") {
		t.Fatalf("saved markdown: %v", err)
	}
	final := remote[api.ThreadDetail](t, dev, "GET", "/v1/threads/"+th, nil)
	if final.Thread.Status != api.ThreadCompleted {
		t.Fatalf("thread not completed: %s", final.Thread.Status)
	}
	paths := map[string]bool{}
	for _, a := range final.Artifacts {
		paths[string(a.Stage)+"/"+a.Path] = true
	}
	for _, want := range []string{"sparring/brief.md", "design/design.md", "design/mockups/compact-list.html", "technical/technical.md", "plan/plan.json"} {
		if !paths[want] {
			t.Errorf("missing artifact %s (have %v)", want, paths)
		}
	}
	agentReplies := 0
	for _, m := range final.Messages {
		if m.Kind == api.MessageAgent {
			agentReplies++
		}
	}
	if agentReplies != 4 {
		t.Errorf("expected 4 agent replies, got %d", agentReplies)
	}

	// Reopening sparring locks everything downstream again.
	if _, err := proxyErr(priya, "POST", "/v1/threads/"+th+"/stages/sparring/reopen"); err == nil {
		t.Fatal("reopened a completed thread")
	}
}

func TestReopenLocksDownstream(t *testing.T) {
	t.Setenv("BRAID_FAKE_AGENT", "1")
	t.Setenv("BRAID_FAKE_DELAY_MS", "1")
	t.Setenv("BRAID_SECRETS", "file")
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, _ := server.New(server.Config{DBPath: filepath.Join(t.TempDir(), "b.db"), Logger: log})
	defer srv.Close()
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()
	code, _ := srv.CreateSetupCode(ctx, time.Hour)
	c := newCore(t, log)
	defer c.Shutdown()
	if _, err := c.CreateTeam(ctx, CreateTeamInput{
		JoinInput: JoinInput{ServerURL: hs.URL, Code: code, DisplayName: "Sam"},
		Roles:     api.AllRoles,
		Team:      api.TeamSettings{Name: "Solo", Output: api.OutputTarget{Type: "markdown"}},
	}); err != nil {
		t.Fatal(err)
	}
	c.UpdateSettings(SettingsPatch{Agent: ptr("fake")})
	d := remote[api.ThreadDetail](t, c, "POST", "/v1/threads", api.CreateThreadRequest{Title: "Idea", Body: "An idea"})
	th := d.Thread.ID
	run, err := c.StartRun(ctx, StartRunInput{ThreadID: th, Stage: api.StageSparring})
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, c, run.ID)
	remote[api.Thread](t, c, "POST", "/v1/threads/"+th+"/stages/sparring/approve", nil)
	run, _ = c.StartRun(ctx, StartRunInput{ThreadID: th, Stage: api.StageDesign})
	waitRun(t, c, run.ID)
	thread := remote[api.Thread](t, c, "POST", "/v1/threads/"+th+"/stages/sparring/reopen", nil)
	if thread.CurrentStage != api.StageSparring || thread.StageState(api.StageDesign).Status != api.StageLocked {
		t.Fatalf("reopen didn't lock design: %+v", thread.Stages)
	}
	// The repo-less team still ran with the brief-only fallback.
	out := remote[api.RunOutputResponse](t, c, "GET", "/v1/runs/"+run.ID+"/output", nil)
	if len(out.Items) == 0 {
		t.Fatal("no run output recorded")
	}
}

func TestMultipleRepos(t *testing.T) {
	t.Setenv("BRAID_FAKE_AGENT", "1")
	t.Setenv("BRAID_FAKE_DELAY_MS", "1")
	t.Setenv("BRAID_SECRETS", "file")
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, _ := server.New(server.Config{DBPath: filepath.Join(t.TempDir(), "b.db"), Logger: log})
	defer srv.Close()
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()
	code, _ := srv.CreateSetupCode(ctx, time.Hour)
	web, backend, localWeb := makeRepo(t), makeRepo(t), makeRepo(t)

	c := newCore(t, log)
	defer c.Shutdown()
	if _, err := c.CreateTeam(ctx, CreateTeamInput{
		JoinInput: JoinInput{ServerURL: hs.URL, Code: code, DisplayName: "Sam"},
		Roles:     api.AllRoles,
		Team:      api.TeamSettings{Name: "Multi", Repos: []api.RepoRef{{Name: "web", URL: web}, {URL: backend, Branch: "main"}}, Output: api.OutputTarget{Type: "markdown"}},
	}); err != nil {
		t.Fatal(err)
	}
	c.UpdateSettings(SettingsPatch{Agent: ptr("fake")})
	waitFor(t, "connected", func() bool { return c.State().Connection == "connected" })
	team := remote[api.Team](t, c, "GET", "/v1/team", nil)
	second := filepath.Base(backend) // derived from the path
	if len(team.Repos) != 2 || team.Repos[0].Name != "web" || team.Repos[1].Name != second || team.Repos[1].Branch != "main" {
		t.Fatalf("repos = %+v", team.Repos)
	}

	// Branches that git could read as options never reach members' machines.
	bad := []api.RepoRef{{URL: web, Branch: "--upload-pack=touch /tmp/pwned"}}
	if _, err := proxyErrBody(c, "PATCH", "/v1/team", api.TeamPatch{Repos: &bad}); err == nil {
		t.Fatal("accepted a branch that looks like a git option")
	}

	// This member reads web from their own checkout.
	if err := c.UpdateSettings(SettingsPatch{RepoOverrides: map[string]string{"web": localWeb}}); err != nil {
		t.Fatal(err)
	}

	d := remote[api.ThreadDetail](t, c, "POST", "/v1/threads", api.CreateThreadRequest{Title: "Idea", Body: "An idea"})
	run, err := c.StartRun(ctx, StartRunInput{ThreadID: d.Thread.ID, Stage: api.StageSparring})
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, c, run.ID)
	run = remote[api.Run](t, c, "GET", "/v1/runs/"+run.ID, nil)
	if !regexp.MustCompile(`^web@[0-9a-f]{10} ` + regexp.QuoteMeta(second) + `@[0-9a-f]{10}$`).MatchString(run.CommitSHA) {
		t.Errorf("commit = %q, want both repositories' revisions", run.CommitSHA)
	}
	out := remote[api.RunOutputResponse](t, c, "GET", "/v1/runs/"+run.ID+"/output", nil)
	read := map[string]bool{}
	for _, it := range out.Items {
		if it.Kind == "tool" && it.Tool == "Read" {
			read[it.Detail] = true
		}
	}
	if !read["web/README.md"] || !read[second+"/src/app.ts"] {
		t.Errorf("agent didn't read both repositories, labelled by name: %v", read)
	}

	st := c.State()
	if len(st.Repos) != 2 || st.Repos[0].State != "override" || st.Repos[0].Override != localWeb || st.Repos[0].Dir != localWeb ||
		st.Repos[1].State != "ready" || st.Repos[1].Override != "" {
		t.Fatalf("repo states = %+v", st.Repos)
	}

	// A scan covers every repository.
	scan, err := c.StartScan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, c, scan.ID)
	if team = remote[api.Team](t, c, "GET", "/v1/team", nil); !strings.HasPrefix(team.ScanCommit, "web@") {
		t.Errorf("scan commit = %q", team.ScanCommit)
	}

	// Clearing the override goes back to the managed clone.
	c.UpdateSettings(SettingsPatch{RepoOverrides: map[string]string{"web": ""}})
	if st := c.State(); st.Repos[0].Override != "" {
		t.Fatalf("override not cleared: %+v", st.Repos[0])
	}
	if _, err := c.SyncRepos(ctx, true); err != nil {
		t.Fatal(err)
	}
	if st := c.State(); st.Repos[0].State != "ready" || st.Repos[0].Dir == localWeb {
		t.Fatalf("web should be a managed clone again: %+v", st.Repos[0])
	}
}

func TestCancelPresenceRevokeAndLeave(t *testing.T) {
	t.Setenv("BRAID_FAKE_AGENT", "1")
	t.Setenv("BRAID_FAKE_DELAY_MS", "400") // slow enough to stop mid-run
	t.Setenv("BRAID_SECRETS", "file")
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, _ := server.New(server.Config{DBPath: filepath.Join(t.TempDir(), "b.db"), Logger: log})
	defer srv.Close()
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()
	code, _ := srv.CreateSetupCode(ctx, time.Hour)

	priya := newCore(t, log)
	defer priya.Shutdown()
	if _, err := priya.CreateTeam(ctx, CreateTeamInput{
		JoinInput: JoinInput{ServerURL: hs.URL, Code: code, DisplayName: "Priya"},
		Roles:     api.AllRoles,
		Team:      api.TeamSettings{Name: "Acme", Output: api.OutputTarget{Type: "markdown"}},
	}); err != nil {
		t.Fatal(err)
	}
	priya.UpdateSettings(SettingsPatch{Agent: ptr("fake")})
	inv := remote[api.CreateInviteResponse](t, priya, "POST", "/v1/invites", api.CreateInviteRequest{Roles: []api.Role{api.RoleDesign}})
	dev := newCore(t, log)
	defer dev.Shutdown()
	if _, err := dev.Join(ctx, JoinInput{ServerURL: hs.URL, Code: inv.Code, DisplayName: "Dev"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "both connected", func() bool {
		return priya.State().Connection == "connected" && dev.State().Connection == "connected"
	})
	th := remote[api.ThreadDetail](t, priya, "POST", "/v1/threads", api.CreateThreadRequest{Title: "Idea", Body: "An idea"}).Thread.ID

	// Teammates see who is looking at a thread.
	events, unsub := dev.Bus().Subscribe()
	defer unsub()
	priya.SetPresence(th)
	me := priya.State().Me.ID
	deadline := time.After(5 * time.Second)
	for seen := false; !seen; {
		select {
		case b := <-events:
			var ev api.Event
			json.Unmarshal(b, &ev)
			var p []api.PresenceEntry
			if ev.Type == api.EvPresence && json.Unmarshal(ev.Data, &p) == nil {
				for _, e := range p {
					seen = seen || (e.MemberID == me && e.ThreadID == th)
				}
			}
		case <-deadline:
			t.Fatal("dev never saw priya's presence")
		}
	}

	runStatus := func(id string) api.RunStatus { return remote[api.Run](t, priya, "GET", "/v1/runs/"+id, nil).Status }

	// A teammate's stop request reaches the machine running the agent.
	run, err := priya.StartRun(ctx, StartRunInput{ThreadID: th, Stage: api.StageSparring})
	if err != nil {
		t.Fatal(err)
	}
	if err := dev.CancelRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "remote cancel", func() bool { return runStatus(run.ID) == api.RunCancelled })

	// And the owner can stop it directly.
	run, err = priya.StartRun(ctx, StartRunInput{ThreadID: th, Stage: api.StageSparring})
	if err != nil {
		t.Fatal(err)
	}
	if err := priya.CancelRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "local cancel", func() bool { return runStatus(run.ID) == api.RunCancelled })
	if d := remote[api.ThreadDetail](t, priya, "GET", "/v1/threads/"+th, nil); len(d.Artifacts) != 0 || d.Thread.StageState(api.StageSparring).ActiveRunID != "" {
		t.Errorf("cancelled runs left documents or a stage lock: %+v", d.Thread.Stages[0])
	}

	// Removing a member cuts off their device, and their app says so.
	remote[struct{}](t, priya, "DELETE", "/v1/members/"+dev.State().Me.ID, nil)
	waitFor(t, "dev revoked", func() bool { return dev.State().Connection == "revoked" })

	// Leaving forgets the team on this machine and revokes the device.
	prof := priya.State().Active
	if err := priya.Leave(ctx, prof.ID); err != nil {
		t.Fatal(err)
	}
	if st := priya.State(); st.Active != nil || len(st.Profiles) != 0 {
		t.Fatalf("still a member locally: %+v", st.Profiles)
	}
}

func TestLegacyRepoOverride(t *testing.T) {
	p := Profile{RepoOverride: "/code/app"}
	repos := []api.RepoRef{{Name: "app"}, {Name: "api"}}
	if p.overrideFor(repos, 0) != "/code/app" || p.overrideFor(repos, 1) != "" {
		t.Fatal("the old single-repository override should apply to the first repository only")
	}
	p.RepoOverrides = map[string]string{"app": "/elsewhere"}
	if p.overrideFor(repos, 0) != "/elsewhere" {
		t.Fatal("a per-repository override wins over the old setting")
	}
}

// --- helpers ---

func ptr[T any](v T) *T { return &v }

func newCore(t *testing.T, log *slog.Logger) *Core {
	t.Helper()
	c, err := New(Options{Home: t.TempDir(), Version: "test", Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	c.cfg.ExportDir = t.TempDir()
	return c
}

func makeRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "src"), 0o755)
	os.WriteFile(filepath.Join(dir, "src", "app.ts"), []byte("export const app = 1;\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Demo\n"), 0o644)
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"}, {"add", "."},
		{"-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-q", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func waitRun(t *testing.T, c *Core, runID string) {
	t.Helper()
	var run api.Run
	waitFor(t, "run "+runID, func() bool {
		run = remote[api.Run](t, c, "GET", "/v1/runs/"+runID, nil)
		return run.Status != api.RunRunning
	})
	if run.Status != api.RunSucceeded {
		t.Fatalf("run %s %s: %s", runID, run.Status, run.Error)
	}
}

func remote[T any](t *testing.T, c *Core, method, path string, in any) T {
	t.Helper()
	var out T
	var body []byte
	if in != nil {
		body, _ = json.Marshal(in)
	}
	status, resp, err := c.Proxy(context.Background(), method, path, body)
	if err != nil || status >= 300 {
		t.Fatalf("%s %s: %d %v %s", method, path, status, err, resp)
	}
	if len(resp) > 0 {
		if err := json.Unmarshal(resp, &out); err != nil {
			t.Fatalf("%s %s: decode: %v", method, path, err)
		}
	}
	return out
}

func proxyErr(c *Core, method, path string) ([]byte, error) {
	status, resp, err := c.Proxy(context.Background(), method, path, nil)
	if err != nil {
		return nil, err
	}
	if status >= 300 {
		return resp, &httpErr{status}
	}
	return resp, nil
}

func proxyErrBody(c *Core, method, path string, in any) ([]byte, error) {
	body, _ := json.Marshal(in)
	status, resp, err := c.Proxy(context.Background(), method, path, body)
	if err != nil {
		return nil, err
	}
	if status >= 300 {
		return resp, &httpErr{status}
	}
	return resp, nil
}

type httpErr struct{ status int }

func (e *httpErr) Error() string { return http.StatusText(e.status) }
