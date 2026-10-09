package server

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/sam-jeston/braid/internal/api"
	"github.com/sam-jeston/braid/internal/authn"
)

// fixture is a server with a controllable clock and helpers to add members.
type fixture struct {
	t  *testing.T
	s  *Server
	hs *httptest.Server

	mu  sync.Mutex
	now time.Time
}

type member struct {
	api.Member
	DeviceID string
	Token    string
	key      ed25519.PrivateKey
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, now: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)}
	s, err := New(Config{
		DBPath: filepath.Join(t.TempDir(), "t.db"),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:    func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now },
	})
	if err != nil {
		t.Fatal(err)
	}
	f.s = s
	f.hs = httptest.NewServer(s.Handler())
	t.Cleanup(func() { f.hs.Close(); s.Close() })
	return f
}

func (f *fixture) advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.mu.Unlock()
}

// call makes a request and decodes a 2xx response into out (if non-nil).
func (f *fixture) call(m member, method, path string, in, out any) int {
	f.t.Helper()
	status, b := call(f.t, f.hs, method, path, m.Token, in)
	if out != nil && status < 300 {
		if err := json.Unmarshal(b, out); err != nil {
			f.t.Fatalf("%s %s: decode %s: %v", method, path, b, err)
		}
	}
	return status
}

// must is call that fails the test unless the status matches.
func (f *fixture) must(want int, m member, method, path string, in, out any) {
	f.t.Helper()
	status, b := call(f.t, f.hs, method, path, m.Token, in)
	if status != want {
		f.t.Fatalf("%s %s: got %d, want %d: %s", method, path, status, want, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			f.t.Fatalf("%s %s: decode: %v", method, path, err)
		}
	}
}

func (f *fixture) login(m *member) {
	f.t.Helper()
	status, tok := login(f.t, f.s, f.hs, m.DeviceID, m.key)
	if status != 200 {
		f.t.Fatalf("login %s: %d", m.DisplayName, status)
	}
	m.Token = tok
}

// createTeam makes a new team whose first member is an admin with the given roles.
func (f *fixture) createTeam(name string, roles ...api.Role) member {
	f.t.Helper()
	code, err := f.s.CreateSetupCode(context.Background(), time.Hour)
	if err != nil {
		f.t.Fatal(err)
	}
	_, key, _ := authn.GenerateKey()
	pub := authn.EncodePublicKey(key.Public().(ed25519.PublicKey))
	var join api.JoinResponse
	f.must(201, member{}, "POST", "/v1/setup/teams", api.CreateTeamRequest{
		SetupCode: code,
		Team:      api.TeamSettings{Name: name, Output: api.OutputTarget{Type: "markdown"}},
		Member:    api.NewMember{DisplayName: name + " admin", Roles: roles},
		Device:    api.DeviceRegistration{PublicKey: pub, Signature: authn.Sign(key, authn.RegistrationMessage(f.s.ServerID(), code, pub))},
	}, &join)
	m := member{Member: join.Member, DeviceID: join.DeviceID, key: key}
	f.login(&m)
	return m
}

func (f *fixture) invite(admin member, req api.CreateInviteRequest) string {
	f.t.Helper()
	var inv api.CreateInviteResponse
	f.must(201, admin, "POST", "/v1/invites", req, &inv)
	return inv.Code
}

func (f *fixture) redeem(code, name string) (member, int) {
	f.t.Helper()
	_, key, _ := authn.GenerateKey()
	pub := authn.EncodePublicKey(key.Public().(ed25519.PublicKey))
	var join api.JoinResponse
	status := f.call(member{}, "POST", "/v1/invites/redeem", api.RedeemInviteRequest{
		Code:   code,
		Member: api.NewMember{DisplayName: name},
		Device: api.DeviceRegistration{PublicKey: pub, Signature: authn.Sign(key, authn.RegistrationMessage(f.s.ServerID(), code, pub))},
	}, &join)
	m := member{Member: join.Member, DeviceID: join.DeviceID, key: key}
	if status == 201 {
		f.login(&m)
	}
	return m, status
}

func (f *fixture) join(admin member, name string, roles ...api.Role) member {
	f.t.Helper()
	m, status := f.redeem(f.invite(admin, api.CreateInviteRequest{Roles: roles}), name)
	if status != 201 {
		f.t.Fatalf("redeem: %d", status)
	}
	return m
}

func (f *fixture) thread(m member, title string) api.ThreadDetail {
	f.t.Helper()
	var d api.ThreadDetail
	f.must(201, m, "POST", "/v1/threads", api.CreateThreadRequest{Title: title, Body: "An idea worth sparring on."}, &d)
	return d
}

func (f *fixture) save(m member, threadID string, stage api.Stage, path, content string) {
	f.t.Helper()
	f.must(201, m, "POST", "/v1/threads/"+threadID+"/artifacts", api.SaveArtifactRequest{Stage: stage, Path: path, Content: content}, nil)
}

func TestMemberAdministration(t *testing.T) {
	f := newFixture(t)
	admin := f.createTeam("Acme", api.RoleProduct)
	dana := f.join(admin, "Dana", api.RoleDesign)

	// Only admins manage members and invites.
	if s := f.call(dana, "PATCH", "/v1/members/"+admin.ID, api.MemberPatch{Roles: []api.Role{api.RoleDesign}}, nil); s != 403 {
		t.Errorf("non-admin patched a member: %d", s)
	}
	if s := f.call(dana, "POST", "/v1/invites", api.CreateInviteRequest{Roles: []api.Role{api.RoleDesign}}, nil); s != 403 {
		t.Errorf("non-admin created an invite: %d", s)
	}
	if s := f.call(dana, "GET", "/v1/invites", nil, nil); s != 403 {
		t.Errorf("non-admin listed invites: %d", s)
	}
	if s := f.call(dana, "DELETE", "/v1/members/"+admin.ID, nil, nil); s != 403 {
		t.Errorf("non-admin removed a member: %d", s)
	}

	var m api.Member
	f.must(200, admin, "PATCH", "/v1/members/"+dana.ID, api.MemberPatch{Roles: []api.Role{api.RoleDesign, api.RoleEngineering}}, &m)
	if len(m.Roles) != 2 {
		t.Errorf("roles = %v", m.Roles)
	}
	if s := f.call(admin, "PATCH", "/v1/members/"+dana.ID, api.MemberPatch{Roles: []api.Role{"ceo"}}, nil); s != 400 {
		t.Errorf("unknown role accepted: %d", s)
	}

	// A team always keeps an admin.
	no := false
	if s := f.call(admin, "PATCH", "/v1/members/"+admin.ID, api.MemberPatch{IsAdmin: &no}, nil); s != 409 {
		t.Errorf("last admin demoted themselves: %d", s)
	}
	if s := f.call(admin, "DELETE", "/v1/members/"+admin.ID, nil, nil); s != 409 {
		t.Errorf("last admin left: %d", s)
	}
	yes := true
	f.must(200, admin, "PATCH", "/v1/members/"+dana.ID, api.MemberPatch{IsAdmin: &yes}, nil)
	f.must(200, admin, "PATCH", "/v1/members/"+admin.ID, api.MemberPatch{IsAdmin: &no}, nil)

	// Removing a member revokes their devices and sessions.
	f.must(204, dana, "DELETE", "/v1/members/"+admin.ID, nil, nil)
	if s := f.call(admin, "GET", "/v1/me", nil, nil); s != 401 {
		t.Errorf("removed member's token still works: %d", s)
	}
	if s, _ := login(t, f.s, f.hs, admin.DeviceID, admin.key); s != 401 {
		t.Errorf("removed member logged in again: %d", s)
	}

	// Members can see their own devices.
	var devices []api.Device
	f.must(200, dana, "GET", "/v1/devices", nil, &devices)
	if len(devices) != 1 || devices[0].ID != dana.DeviceID {
		t.Errorf("devices = %+v", devices)
	}
}

func TestInvites(t *testing.T) {
	f := newFixture(t)
	admin := f.createTeam("Acme", api.RoleProduct)

	// Single use by default.
	code := f.invite(admin, api.CreateInviteRequest{Roles: []api.Role{api.RoleEngineering}})
	eng, status := f.redeem(strings.ToLower(code), "Eli")
	if status != 201 || !api.HasRole(eng.Roles, api.RoleEngineering) || eng.IsAdmin {
		t.Fatalf("redeem: %d %+v", status, eng.Member)
	}
	if _, status := f.redeem(code, "Again"); status != 403 {
		t.Errorf("single-use invite redeemed twice: %d", status)
	}

	// Revoked invites stop working.
	code = f.invite(admin, api.CreateInviteRequest{Roles: []api.Role{api.RoleDesign}, MaxUses: 5})
	var invites []api.Invite
	f.must(200, admin, "GET", "/v1/invites", nil, &invites)
	var multi *api.Invite
	for i := range invites {
		if invites[i].Hint == code[len(code)-4:] {
			multi = &invites[i]
		} else if invites[i].Uses != 1 {
			t.Errorf("used invite shows %d uses", invites[i].Uses)
		}
	}
	if len(invites) != 2 || multi == nil || multi.MaxUses != 5 {
		t.Fatalf("invites = %+v", invites)
	}
	f.must(204, admin, "DELETE", "/v1/invites/"+multi.ID, nil, nil)
	if _, status := f.redeem(code, "Late"); status != 403 {
		t.Errorf("revoked invite redeemed: %d", status)
	}
	if s := f.call(admin, "DELETE", "/v1/invites/"+multi.ID, nil, nil); s != 404 {
		t.Errorf("revoking twice: %d", s)
	}

	// Invites expire.
	code = f.invite(admin, api.CreateInviteRequest{Roles: []api.Role{api.RoleDesign}, ExpiresInHours: 1})
	f.advance(2 * time.Hour)
	f.login(&admin)
	if _, status := f.redeem(code, "Slow"); status != 403 {
		t.Errorf("expired invite redeemed: %d", status)
	}
	if s := f.call(admin, "POST", "/v1/invites", api.CreateInviteRequest{}, nil); s != 400 {
		t.Errorf("invite without roles: %d", s)
	}
}

func TestTeamsAreIsolated(t *testing.T) {
	f := newFixture(t)
	a := f.createTeam("A", api.RoleProduct, api.RoleDesign, api.RoleEngineering)
	b := f.createTeam("B", api.RoleProduct, api.RoleDesign, api.RoleEngineering)
	th := f.thread(a, "Team A secret")
	var run api.Run
	f.must(201, a, "POST", "/v1/runs", api.CreateRunRequest{ThreadID: th.Thread.ID, Stage: api.StageSparring, Agent: "fake"}, &run)
	f.invite(a, api.CreateInviteRequest{Roles: []api.Role{api.RoleDesign}})
	var invites []api.Invite
	f.must(200, a, "GET", "/v1/invites", nil, &invites)

	for _, c := range []struct{ method, path string }{
		{"GET", "/v1/threads/" + th.Thread.ID},
		{"PATCH", "/v1/threads/" + th.Thread.ID},
		{"GET", "/v1/runs/" + run.ID},
		{"GET", "/v1/runs/" + run.ID + "/output"},
		{"POST", "/v1/runs/" + run.ID + "/cancel"},
		{"DELETE", "/v1/invites/" + invites[0].ID},
		{"PATCH", "/v1/members/" + a.ID},
		{"DELETE", "/v1/members/" + a.ID},
	} {
		var body any
		if c.method == "PATCH" {
			body = map[string]any{}
		}
		if s := f.call(b, c.method, c.path, body, nil); s != 404 {
			t.Errorf("team B %s %s: %d, want 404", c.method, c.path, s)
		}
	}
	var threads []api.Thread
	f.must(200, b, "GET", "/v1/threads", nil, &threads)
	if len(threads) != 0 {
		t.Errorf("team B sees team A's threads: %+v", threads)
	}
}

func TestStageRules(t *testing.T) {
	f := newFixture(t)
	priya := f.createTeam("Acme", api.RoleProduct)
	dana := f.join(priya, "Dana", api.RoleDesign, api.RoleEngineering)

	if s := f.call(priya, "POST", "/v1/threads", api.CreateThreadRequest{Title: " ", Body: "x"}, nil); s != 400 {
		t.Errorf("thread without title: %d", s)
	}
	if s := f.call(priya, "POST", "/v1/threads", api.CreateThreadRequest{Title: "x"}, nil); s != 400 {
		t.Errorf("thread without body: %d", s)
	}
	th := f.thread(priya, "Weekly digest").Thread.ID
	base := "/v1/threads/" + th

	// Messages only go to stages that have started.
	if s := f.call(dana, "POST", base+"/messages", api.CreateMessageRequest{Stage: api.StageDesign, Body: "early"}, nil); s != 409 {
		t.Errorf("message to a locked stage: %d", s)
	}
	if s := f.call(dana, "POST", base+"/messages", api.CreateMessageRequest{Stage: "nope", Body: "x"}, nil); s != 400 {
		t.Errorf("message to an unknown stage: %d", s)
	}
	if s := f.call(dana, "POST", base+"/messages", api.CreateMessageRequest{Stage: api.StageSparring, Body: "  "}, nil); s != 400 {
		t.Errorf("empty message: %d", s)
	}
	f.must(201, dana, "POST", base+"/messages", api.CreateMessageRequest{Stage: api.StageSparring, Body: "Who asked for this?"}, nil)

	// Documents are validated against the stage's allowed outputs.
	for _, bad := range []api.SaveArtifactRequest{
		{Stage: api.StageSparring, Path: "../brief.md", Content: "x"},
		{Stage: api.StageSparring, Path: "notes.txt", Content: "x"},
		{Stage: api.StageSparring, Path: "design.md", Content: "x"},
	} {
		if s := f.call(priya, "POST", base+"/artifacts", bad, nil); s != 400 {
			t.Errorf("saved %s in %s: %d", bad.Path, bad.Stage, s)
		}
	}
	if s := f.call(priya, "POST", base+"/artifacts", api.SaveArtifactRequest{Stage: api.StageDesign, Path: "design.md", Content: "x"}, nil); s != 409 {
		t.Errorf("edited a locked stage: %d", s)
	}

	// Approval needs the owner role and the stage's primary document.
	if s := f.call(dana, "POST", base+"/stages/sparring/approve", nil, nil); s != 403 {
		t.Errorf("designer approved sparring: %d", s)
	}
	if s := f.call(priya, "POST", base+"/stages/sparring/approve", nil, nil); s != 409 {
		t.Errorf("approved sparring without a brief: %d", s)
	}
	f.save(priya, th, api.StageSparring, "brief.md", "# Brief v1")
	f.save(priya, th, api.StageSparring, "brief.md", "# Brief v2")
	var history []api.Artifact
	f.must(200, priya, "GET", base+"/artifacts/history?stage=sparring&path=brief.md", nil, &history)
	if len(history) != 2 || history[0].Version != 2 || history[0].Content != "# Brief v2" {
		t.Errorf("history = %+v", history)
	}
	var thread api.Thread
	f.must(200, priya, "POST", base+"/stages/sparring/approve", nil, &thread)
	if thread.CurrentStage != api.StageDesign || thread.StageState(api.StageDesign).Status != api.StageOpen {
		t.Fatalf("design didn't open: %+v", thread.Stages)
	}
	if s := f.call(priya, "POST", base+"/stages/sparring/approve", nil, nil); s != 409 {
		t.Errorf("approved twice: %d", s)
	}

	// With mockups, design needs a selected variation, chosen by a designer.
	f.save(dana, th, api.StageDesign, "design.md", "# Design")
	f.save(dana, th, api.StageDesign, "mockups/a.html", "<h1>A</h1>")
	f.save(dana, th, api.StageDesign, "mockups/b.html", "<h1>B</h1>")
	if s := f.call(dana, "POST", base+"/stages/design/approve", nil, nil); s != 409 {
		t.Errorf("approved design without a selection: %d", s)
	}
	if s := f.call(priya, "POST", base+"/stages/design/selection", api.SelectionRequest{Path: "mockups/a.html"}, nil); s != 403 {
		t.Errorf("non-designer selected a mockup: %d", s)
	}
	if s := f.call(dana, "POST", base+"/stages/design/selection", api.SelectionRequest{Path: "mockups/zzz.html"}, nil); s != 400 {
		t.Errorf("selected a missing mockup: %d", s)
	}
	f.must(200, dana, "POST", base+"/stages/design/selection", api.SelectionRequest{Path: "mockups/a.html"}, nil)
	// Deleting the selected mockup clears the selection.
	f.save(dana, th, api.StageDesign, "mockups/a.html", "")
	var detail api.ThreadDetail
	f.must(200, dana, "GET", base, nil, &detail)
	if detail.Thread.StageState(api.StageDesign).Selection != "" {
		t.Error("selection survived the deletion of its mockup")
	}
	for _, a := range detail.Artifacts {
		if a.Path == "mockups/a.html" {
			t.Error("deleted mockup still listed")
		}
	}
	f.must(200, dana, "POST", base+"/stages/design/selection", api.SelectionRequest{Path: "mockups/b.html"}, nil)
	f.must(200, dana, "POST", base+"/stages/design/approve", nil, nil)

	// plan.json must be a valid plan.
	f.save(dana, th, api.StageTechnical, "technical.md", "# Tech")
	f.must(200, dana, "POST", base+"/stages/technical/approve", nil, nil)
	if s := f.call(dana, "POST", base+"/artifacts", api.SaveArtifactRequest{Stage: api.StagePlan, Path: "plan.json", Content: `{"tickets": []}`}, nil); s != 400 {
		t.Errorf("invalid plan accepted: %d", s)
	}
	if s := f.call(dana, "POST", base+"/exports", api.CreateExportRequest{Target: "markdown"}, nil); s != 409 {
		t.Errorf("published without a plan: %d", s)
	}

	// Archived threads are frozen until restored.
	archived := api.ThreadArchived
	f.must(200, priya, "PATCH", base, api.ThreadPatch{Status: &archived}, &thread)
	if s := f.call(priya, "POST", base+"/stages/sparring/reopen", nil, nil); s != 409 {
		t.Errorf("reopened a stage on an archived thread: %d", s)
	}
	if s := f.call(dana, "POST", "/v1/runs", api.CreateRunRequest{ThreadID: th, Stage: api.StagePlan, Agent: "fake"}, nil); s != 409 {
		t.Errorf("ran an agent on an archived thread: %d", s)
	}
	active := api.ThreadActive
	title := "Weekly digest emails"
	f.must(200, priya, "PATCH", base, api.ThreadPatch{Status: &active, Title: &title}, &thread)
	if thread.Status != api.ThreadActive || thread.Title != title {
		t.Errorf("restore: %+v", thread)
	}
	bogus := api.ThreadStatus("deleted")
	if s := f.call(priya, "PATCH", base, api.ThreadPatch{Status: &bogus}, nil); s != 400 {
		t.Errorf("unknown status accepted: %d", s)
	}
	var threads []api.Thread
	f.must(200, priya, "GET", "/v1/threads", nil, &threads)
	if len(threads) != 1 || len(threads[0].Stages) != 4 {
		t.Errorf("threads = %+v", threads)
	}
}

func TestRunLifecycle(t *testing.T) {
	f := newFixture(t)
	priya := f.createTeam("Acme", api.RoleProduct)
	dana := f.join(priya, "Dana", api.RoleDesign)
	th := f.thread(priya, "Idea").Thread.ID

	if s := f.call(priya, "POST", "/v1/runs", api.CreateRunRequest{ThreadID: th, Stage: api.StageDesign, Agent: "fake"}, nil); s != 409 {
		t.Errorf("run in a locked stage: %d", s)
	}
	if s := f.call(priya, "POST", "/v1/runs", api.CreateRunRequest{ThreadID: th, Stage: api.StageSparring}, nil); s != 400 {
		t.Errorf("run without an agent: %d", s)
	}
	var run api.Run
	f.must(201, priya, "POST", "/v1/runs", api.CreateRunRequest{ThreadID: th, Stage: api.StageSparring, Agent: "fake"}, &run)
	if s := f.call(dana, "POST", "/v1/runs", api.CreateRunRequest{ThreadID: th, Stage: api.StageSparring, Agent: "fake"}, nil); s != 409 {
		t.Errorf("second run on a busy stage: %d", s)
	}
	// A busy stage can't be edited or approved by hand.
	if s := f.call(priya, "POST", "/v1/threads/"+th+"/artifacts", api.SaveArtifactRequest{Stage: api.StageSparring, Path: "brief.md", Content: "x"}, nil); s != 409 {
		t.Errorf("edited a stage while an agent works on it: %d", s)
	}

	// Only the run's owner streams output and finishes it.
	if s := f.call(dana, "POST", "/v1/runs/"+run.ID+"/output", api.RunOutputBatch{Items: []api.RunOutput{{Kind: "text", Text: "hijack"}}}, nil); s != 403 {
		t.Errorf("teammate wrote to someone else's run: %d", s)
	}
	if s := f.call(dana, "POST", "/v1/runs/"+run.ID+"/finish", api.FinishRunRequest{Status: api.RunSucceeded}, nil); s != 403 {
		t.Errorf("teammate finished someone else's run: %d", s)
	}
	f.must(204, priya, "POST", "/v1/runs/"+run.ID+"/output", api.RunOutputBatch{Items: []api.RunOutput{{Kind: "text", Text: strings.Repeat("x", 70<<10)}}}, nil)
	var out api.RunOutputResponse
	f.must(200, dana, "GET", "/v1/runs/"+run.ID+"/output", nil, &out)
	if len(out.Items) != 1 || len(out.Items[0].Text) != 64<<10 {
		t.Errorf("oversized output wasn't clipped: %d items", len(out.Items))
	}

	// Anyone can ask the owner's machine to stop.
	f.must(202, dana, "POST", "/v1/runs/"+run.ID+"/cancel", nil, nil)
	var n int
	f.s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE run_id = ? AND type = ?`, run.ID, api.EvRunCancel).Scan(&n)
	if n != 1 {
		t.Errorf("cancel request events = %d", n)
	}

	// A machine that stops heartbeating loses the run and the stage lock.
	f.advance(3 * time.Minute)
	if err := f.s.reapRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.must(200, priya, "GET", "/v1/runs/"+run.ID, nil, &run)
	if run.Status != api.RunAbandoned {
		t.Fatalf("status = %s", run.Status)
	}
	var detail api.ThreadDetail
	f.must(200, priya, "GET", "/v1/threads/"+th, nil, &detail)
	if detail.Thread.StageState(api.StageSparring).ActiveRunID != "" {
		t.Error("abandoned run still holds the stage")
	}
	if s := f.call(priya, "POST", "/v1/runs/"+run.ID+"/output", api.RunOutputBatch{}, nil); s != 409 {
		t.Errorf("heartbeat on an abandoned run: %d", s)
	}
	if s := f.call(dana, "POST", "/v1/runs/"+run.ID+"/cancel", nil, nil); s != 409 {
		t.Errorf("cancelled a finished run: %d", s)
	}

	// Only one codebase scan at a time, and they're listed for the team.
	var scan api.Run
	f.must(201, priya, "POST", "/v1/runs", api.CreateRunRequest{Kind: api.RunKindScan, Agent: "fake"}, &scan)
	if s := f.call(dana, "POST", "/v1/runs", api.CreateRunRequest{Kind: api.RunKindScan, Agent: "fake"}, nil); s != 409 {
		t.Errorf("concurrent scan: %d", s)
	}
	f.must(200, priya, "POST", "/v1/runs/"+scan.ID+"/finish", api.FinishRunRequest{
		Status: api.RunSucceeded, CommitSHA: "web@abc api@def",
		Artifacts: []api.ArtifactUpload{{Path: "codebase-brief.md", Content: "# Codebase brief"}, {Path: "rules.md", Content: "- rule"}},
	}, nil)
	var scans []api.Run
	f.must(200, dana, "GET", "/v1/runs?kind=scan", nil, &scans)
	if len(scans) != 1 || scans[0].Status != api.RunSucceeded {
		t.Errorf("scans = %+v", scans)
	}
	var team api.Team
	f.must(200, dana, "GET", "/v1/team", nil, &team)
	if team.ScanCommit != "web@abc api@def" || team.CodebaseBrief != "# Codebase brief" || team.Rules != "- rule" {
		t.Errorf("scan didn't update the team: %+v", team)
	}
}

func TestTeamSettingsArePerRole(t *testing.T) {
	f := newFixture(t)
	admin := f.createTeam("Acme", api.RoleProduct)
	eng := f.join(admin, "Eli", api.RoleEngineering)
	name := "Hijacked"
	if s := f.call(eng, "PATCH", "/v1/team", api.TeamPatch{Name: &name}, nil); s != 403 {
		t.Errorf("non-admin renamed the team: %d", s)
	}
	repos := []api.RepoRef{{URL: "github.com/acme/web"}}
	if s := f.call(eng, "PATCH", "/v1/team", api.TeamPatch{Repos: &repos}, nil); s != 403 {
		t.Errorf("non-admin changed repositories: %d", s)
	}
	// Anyone curates the shared agent context.
	rules := "- Keep handlers thin"
	var team api.Team
	f.must(200, eng, "PATCH", "/v1/team", api.TeamPatch{Rules: &rules}, &team)
	if team.Rules != rules {
		t.Errorf("rules = %q", team.Rules)
	}
	bad := api.OutputTarget{Type: "linear"}
	if s := f.call(admin, "PATCH", "/v1/team", api.TeamPatch{Output: &bad}, nil); s != 400 {
		t.Errorf("linear output without a team key: %d", s)
	}
	f.must(200, admin, "PATCH", "/v1/team", api.TeamPatch{Repos: &repos}, &team)
	if len(team.Repos) != 1 || team.Repos[0].Name != "web" {
		t.Errorf("repos = %+v", team.Repos)
	}
}

func TestWebsocketReplayAndPresence(t *testing.T) {
	f := newFixture(t)
	priya := f.createTeam("Acme", api.RoleProduct)
	dana := f.join(priya, "Dana", api.RoleDesign)
	th := f.thread(priya, "Idea").Thread.ID
	f.must(201, dana, "POST", "/v1/threads/"+th+"/messages", api.CreateMessageRequest{Stage: api.StageSparring, Body: "hello"}, nil)

	dial := func(m member, query string) *websocket.Conn {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(f.hs.URL, "http")+"/v1/ws"+query, &websocket.DialOptions{
			HTTPHeader: http.Header{"Authorization": []string{"Bearer " + m.Token}},
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.CloseNow() })
		return c
	}
	// next returns the next event, skipping presence updates, which arrive whenever anyone connects.
	next := func(c *websocket.Conn) api.Event {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for {
			_, b, err := c.Read(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var ev api.Event
			json.Unmarshal(b, &ev)
			if ev.Type != api.EvPresence {
				return ev
			}
		}
	}

	// Replay from the start brings back every event in order, then hello.
	c := dial(priya, "?since=0")
	var types []string
	var last int64
	for {
		ev := next(c)
		if ev.Type == api.EvHello {
			if ev.Seq != last {
				t.Errorf("hello seq %d, last replayed %d", ev.Seq, last)
			}
			break
		}
		if ev.Seq <= last {
			t.Fatalf("out of order: %d after %d", ev.Seq, last)
		}
		last = ev.Seq
		types = append(types, ev.Type)
	}
	joined := strings.Join(types, ",")
	if !strings.Contains(joined, api.EvMemberJoined) || !strings.Contains(joined, api.EvThreadCreated) || !strings.HasSuffix(joined, api.EvMessageCreated) {
		t.Errorf("replayed %s", joined)
	}

	// Caught-up clients get only the hello, and a bad cursor forces a resync.
	if ev := next(dial(priya, "?since="+itoa(last))); ev.Type != api.EvHello {
		t.Errorf("caught-up client got %s first", ev.Type)
	}
	if ev := next(dial(priya, "?since=-1")); ev.Type != api.EvResync {
		t.Errorf("bad cursor got %s", ev.Type)
	}

	// Live events and presence fan out to teammates.
	d := dial(dana, "")
	if ev := next(d); ev.Type != api.EvHello {
		t.Fatalf("first event %s", ev.Type)
	}
	f.must(201, priya, "POST", "/v1/threads/"+th+"/messages", api.CreateMessageRequest{Stage: api.StageSparring, Body: "live"}, nil)
	if ev := next(d); ev.Type != api.EvMessageCreated {
		t.Errorf("live event = %s", ev.Type)
	}
	msg, _ := json.Marshal(api.ClientMessage{Type: "presence", ThreadID: th})
	if err := c.Write(context.Background(), websocket.MessageText, msg); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		_, b, err := d.Read(ctx)
		if err != nil {
			t.Fatal("never saw priya's presence")
		}
		var ev api.Event
		json.Unmarshal(b, &ev)
		if ev.Type != api.EvPresence {
			continue
		}
		var p []api.PresenceEntry
		json.Unmarshal(ev.Data, &p)
		found := false
		for _, e := range p {
			found = found || (e.MemberID == priya.ID && e.ThreadID == th)
		}
		if found {
			break
		}
	}

	// Removing a member drops their live connection.
	f.must(204, priya, "DELETE", "/v1/members/"+dana.ID, nil, nil)
	for {
		if _, _, err := d.Read(ctx); err != nil {
			if ctx.Err() != nil {
				t.Fatal("removed member's websocket stayed open")
			}
			break
		}
	}
	if s := f.call(member{}, "GET", "/v1/ws", nil, nil); s != 401 {
		t.Errorf("websocket without a token: %d", s)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }
