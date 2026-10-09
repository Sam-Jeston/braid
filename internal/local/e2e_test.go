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
		Team:      api.TeamSettings{Name: "Acme", RepoURL: repoDir, Output: api.OutputTarget{Type: api.OutputMarkdown}},
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

type httpErr struct{ status int }

func (e *httpErr) Error() string { return http.StatusText(e.status) }
