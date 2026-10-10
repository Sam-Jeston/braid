package local

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sam-jeston/braid/internal/api"
	"github.com/sam-jeston/braid/internal/export"
	"github.com/sam-jeston/braid/internal/server"
)

// fakeLinear creates issues ENG-1, ENG-2, … and reports the states set in it.
type fakeLinear struct {
	mu      sync.Mutex
	created int
	states  map[int]string // issue number → Linear state type
	lookups int
}

func (l *fakeLinear) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "lin_api_test" {
		w.WriteHeader(401)
		return
	}
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	l.mu.Lock()
	defer l.mu.Unlock()
	switch {
	case strings.Contains(req.Query, "teams("):
		fmt.Fprint(w, `{"data":{"teams":{"nodes":[{"id":"team-1","key":"ENG"}]}}}`)
	case strings.Contains(req.Query, "issueCreate"):
		l.created++
		fmt.Fprintf(w, `{"data":{"issueCreate":{"success":true,"issue":{"id":"id-%d","identifier":"ENG-%d","url":"https://linear.app/acme/issue/ENG-%d"}}}}`, l.created, l.created, l.created)
	case strings.Contains(req.Query, "issueRelationCreate"):
		fmt.Fprint(w, `{"data":{"issueRelationCreate":{"success":true}}}`)
	case strings.Contains(req.Query, "issues("):
		l.lookups++
		var nodes []string
		for _, n := range req.Variables["numbers"].([]any) {
			num := int(n.(float64))
			if st, ok := l.states[num]; ok {
				nodes = append(nodes, fmt.Sprintf(`{"identifier":"ENG-%d","state":{"type":%q}}`, num, st))
			}
		}
		fmt.Fprintf(w, `{"data":{"issues":{"nodes":[%s]}}}`, strings.Join(nodes, ","))
	}
}

func (l *fakeLinear) set(states map[int]string) {
	l.mu.Lock()
	l.states = states
	l.mu.Unlock()
}

// TestDeliveryTracking publishes a plan to Linear, then lets members' apps
// notice in Linear that the work was done.
func TestDeliveryTracking(t *testing.T) {
	t.Setenv("BRAID_SECRETS", "file")
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	linear := &fakeLinear{}
	ls := httptest.NewServer(linear)
	defer ls.Close()

	srv, err := server.New(server.Config{DBPath: filepath.Join(t.TempDir(), "d.db"), Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()
	code, _ := srv.CreateSetupCode(ctx, time.Hour)

	// Checks run when the test says so.
	fakeTracker := func(c *Core) {
		c.mu.Lock()
		c.linearEndpoint, c.deliveryTiming = ls.URL, deliveryTiming{first: time.Hour, every: time.Hour}
		c.mu.Unlock()
	}
	priya := newCore(t, log)
	defer priya.Shutdown()
	fakeTracker(priya)
	if _, err := priya.CreateTeam(ctx, CreateTeamInput{
		JoinInput: JoinInput{ServerURL: hs.URL, Code: code, DisplayName: "Priya"},
		Roles:     api.AllRoles,
		Team:      api.TeamSettings{Name: "Acme", Output: api.OutputTarget{Type: api.OutputLinear, Config: map[string]string{"team_key": "ENG"}}},
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "priya connected", func() bool { return priya.State().Connection == "connected" })
	if err := priya.SetCredential(secretLinear, "lin_api_test"); err != nil {
		t.Fatal(err)
	}

	// A plan, published to Linear as ENG-1 (epic), ENG-2 and ENG-3.
	th := remote[api.ThreadDetail](t, priya, "POST", "/v1/threads", api.CreateThreadRequest{Title: "Digest", Body: "Weekly digest"}).Thread.ID
	for _, step := range []struct {
		stage    api.Stage
		path, md string
	}{{api.StageSparring, "brief.md", "# Brief"}, {api.StageDesign, "design.md", "# Design"}, {api.StageTechnical, "technical.md", "# Tech"}} {
		remote[api.Artifact](t, priya, "POST", "/v1/threads/"+th+"/artifacts", api.SaveArtifactRequest{Stage: step.stage, Path: step.path, Content: step.md})
		remote[api.Thread](t, priya, "POST", "/v1/threads/"+th+"/stages/"+string(step.stage)+"/approve", nil)
	}
	remote[api.Artifact](t, priya, "POST", "/v1/threads/"+th+"/artifacts", api.SaveArtifactRequest{Stage: api.StagePlan, Path: "plan.json", Content: `{"summary":"S","epic":{"title":"Digest","description":"D"},"tickets":[
		{"ref":"T1","title":"API","type":"task","role":"engineering","description":"x","acceptance_criteria":["a"],"estimate":3,"labels":[],"depends_on":[]},
		{"ref":"T2","title":"UI","type":"task","role":"engineering","description":"y","acceptance_criteria":["b"],"estimate":2,"labels":[],"depends_on":[]}]}`})
	res, err := priya.Publish(ctx, th)
	if err != nil || len(res.Export.Items) != 3 {
		t.Fatalf("publish: %+v %v", res, err)
	}
	thread := func() api.ThreadDetail { return remote[api.ThreadDetail](t, priya, "GET", "/v1/threads/"+th, nil) }
	if d := thread(); d.Thread.Delivery != api.DeliveryPlanned {
		t.Fatalf("delivery after publishing = %q", d.Thread.Delivery)
	}

	// Work starts: issue states are recorded, but the plan stays planned.
	linear.set(map[int]string{1: "started", 2: "completed", 3: "unstarted"})
	check, err := priya.CheckDeliveries(ctx, th)
	if err != nil || check.Checked != 1 || check.Updated != 0 {
		t.Fatalf("first check: %+v %v", check, err)
	}
	d := thread()
	if d.Thread.Delivery != api.DeliveryPlanned || d.Exports[0].Items[1].State != api.IssueDone || d.Exports[0].Items[2].State != api.IssueTodo {
		t.Fatalf("after first check: %s %+v", d.Thread.Delivery, d.Exports[0].Items)
	}

	// A teammate without Linear credentials can't check, but sees the result.
	inv := remote[api.CreateInviteResponse](t, priya, "POST", "/v1/invites", api.CreateInviteRequest{Roles: []api.Role{api.RoleEngineering}})
	dev := newCore(t, log)
	defer dev.Shutdown()
	fakeTracker(dev)
	if _, err := dev.Join(ctx, JoinInput{ServerURL: hs.URL, Code: inv.Code, DisplayName: "Dev"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "dev connected", func() bool { return dev.State().Connection == "connected" })
	if check, err := dev.CheckDeliveries(ctx, ""); err != nil || check.Skipped != 1 || check.Checked != 0 {
		t.Errorf("check without credentials: %+v %v", check, err)
	}

	// Once the last ticket is done, Priya's app marks the plan built in the
	// background (here: on reconnecting, with a short first check).
	linear.set(map[int]string{1: "started", 2: "completed", 3: "completed"})
	priya.mu.Lock()
	priya.deliveryTiming.first = 10 * time.Millisecond
	s := priya.sess
	priya.mu.Unlock()
	s.mu.Lock()
	s.lastDeliveryCheck = time.Time{}
	conn := s.conn
	s.mu.Unlock()
	conn.CloseNow()
	waitFor(t, "plan marked built", func() bool { return thread().Thread.Delivery == api.DeliveryBuilt })
	d = thread()
	if d.Thread.DeliverySource != api.OutputLinear || d.Thread.DeliveryBy != priya.State().Me.ID {
		t.Errorf("built by %s via %s", d.Thread.DeliveryBy, d.Thread.DeliverySource)
	}
	// Dev's UI hears about it in real time.
	waitFor(t, "dev sees it", func() bool {
		var th api.Thread
		for _, x := range remote[[]api.Thread](t, dev, "GET", "/v1/threads", nil) {
			th = x
		}
		return th.Delivery == api.DeliveryBuilt
	})

	// Built plans aren't checked any more.
	linear.mu.Lock()
	before := linear.lookups
	linear.mu.Unlock()
	if check, err := priya.CheckDeliveries(ctx, ""); err != nil || check.Checked != 0 {
		t.Errorf("check after build: %+v %v", check, err)
	}
	linear.mu.Lock()
	after := linear.lookups
	linear.mu.Unlock()
	if after != before {
		t.Error("a built plan was looked up again")
	}

	// A rejected key is reported, not swallowed.
	remote[api.Thread](t, dev, "POST", "/v1/threads/"+th+"/delivery", api.DeliveryUpdate{Delivery: api.DeliveryPlanned})
	priya.SetCredential(secretLinear, "lin_api_revoked")
	if _, err := priya.CheckDeliveries(ctx, th); err == nil {
		t.Error("a rejected Linear key should fail the check")
	}
}

func TestJiraChecksOnlyTheTeamSite(t *testing.T) {
	t.Setenv("BRAID_SECRETS", "file")
	c := newCore(t, slog.New(slog.DiscardHandler))
	defer c.Shutdown()
	c.SetCredential(secretJira, "tok")
	c.UpdateSettings(SettingsPatch{JiraEmail: ptr("me@x.com")})
	team := func(site string) api.Team {
		return api.Team{Output: api.OutputTarget{Type: api.OutputJira, Config: map[string]string{"site": site}}}
	}
	exp := api.Export{Target: api.OutputJira, Items: []api.ExportItem{
		{Ref: "epic", Key: "ENG-1", URL: "https://acme.atlassian.net/browse/ENG-1"},
		{Ref: "T1", Key: "ENG-2", URL: "https://evil.example/browse/ENG-2"},
	}}
	tr, keys := c.issueTracker(team("https://acme.atlassian.net"), exp)
	if j, ok := tr.(*export.Jira); !ok || j.Site != "https://acme.atlassian.net" || strings.Join(keys, ",") != "ENG-1" {
		t.Errorf("tracker = %#v, keys %v", tr, keys)
	}
	if tr, _ := c.issueTracker(team("http://acme.atlassian.net"), exp); tr != nil {
		t.Error("credentials would go over plain HTTP")
	}
	if tr, _ := c.issueTracker(team("https://other.atlassian.net"), exp); tr != nil {
		t.Error("checked issues published to a different site")
	}
	if tr, _ := c.issueTracker(api.Team{Output: api.OutputTarget{Type: api.OutputLinear}}, exp); tr != nil {
		t.Error("checked Jira after the team moved to Linear")
	}
}
