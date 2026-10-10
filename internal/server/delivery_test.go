package server

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sam-jeston/braid/internal/api"
)

const testPlan = `{"summary":"S","epic":{"title":"Digest","description":"D"},"tickets":[
	{"ref":"T1","title":"API","type":"task","role":"engineering","description":"x","acceptance_criteria":["a"],"estimate":3,"labels":[],"depends_on":[]},
	{"ref":"T2","title":"UI","type":"task","role":"engineering","description":"y","acceptance_criteria":["b"],"estimate":2,"labels":[],"depends_on":["T1"]}]}`

// publish walks a new thread through every stage (m needs every role) and
// publishes its plan to target.
func (f *fixture) publish(m member, title, target string, items []api.ExportItem) api.Thread {
	f.t.Helper()
	th := f.thread(m, title).Thread.ID
	base := "/v1/threads/" + th
	f.save(m, th, api.StageSparring, "brief.md", "# Brief")
	f.must(200, m, "POST", base+"/stages/sparring/approve", nil, nil)
	f.save(m, th, api.StageDesign, "design.md", "# Design")
	f.must(200, m, "POST", base+"/stages/design/approve", nil, nil)
	f.save(m, th, api.StageTechnical, "technical.md", "# Tech")
	f.must(200, m, "POST", base+"/stages/technical/approve", nil, nil)
	f.save(m, th, api.StagePlan, "plan.json", testPlan)
	f.must(201, m, "POST", base+"/exports", api.CreateExportRequest{Target: target, Items: items}, nil)
	var d api.ThreadDetail
	f.must(200, m, "GET", base, nil, &d)
	return d.Thread
}

func linearItems() []api.ExportItem {
	return []api.ExportItem{
		{Ref: "epic", Title: "Digest", Key: "ENG-1", URL: "https://linear.app/acme/issue/ENG-1"},
		{Ref: "T1", Title: "API", Key: "ENG-2", URL: "https://linear.app/acme/issue/ENG-2"},
		{Ref: "T2", Title: "UI", Key: "ENG-3", URL: "https://linear.app/acme/issue/ENG-3"},
	}
}

func TestDelivery(t *testing.T) {
	f := newFixture(t)
	eng := f.createTeam("Acme", api.AllRoles...)
	pat := f.join(eng, "Pat", api.RoleProduct)
	other := f.createTeam("Globex", api.AllRoles...)

	th := f.publish(eng, "Digest", api.OutputLinear, linearItems())
	if th.Status != api.ThreadCompleted || th.Delivery != api.DeliveryPlanned || th.DeliveryAt == nil {
		t.Fatalf("published thread = %+v", th)
	}
	md := f.publish(eng, "Notes", api.OutputMarkdown, nil)
	base := "/v1/threads/" + th.ID
	planned := func(m member) []string {
		var exps []api.Export
		f.must(200, m, "GET", "/v1/exports/planned", nil, &exps)
		var ids []string
		for _, e := range exps {
			ids = append(ids, e.ThreadID)
		}
		return ids
	}
	if got := planned(pat); len(got) != 2 {
		t.Fatalf("planned = %v", got)
	}
	if got := planned(other); len(got) != 0 {
		t.Fatalf("another team sees our plans: %v", got)
	}

	// Any member's app can report what the tracker shows; unknown keys and states are ignored.
	var out api.Thread
	f.must(200, pat, "POST", base+"/delivery", api.DeliveryUpdate{Delivery: api.DeliveryPlanned, Tracker: api.OutputLinear, Items: []api.IssueState{
		{Key: "ENG-1", State: api.IssueInProgress}, {Key: "ENG-2", State: api.IssueDone}, {Key: "ENG-3", State: "bogus"}, {Key: "ENG-9", State: api.IssueDone},
	}}, &out)
	var d api.ThreadDetail
	f.must(200, pat, "GET", base, nil, &d)
	states := []string{}
	for _, it := range d.Exports[0].Items {
		states = append(states, it.State)
	}
	if strings.Join(states, ",") != "in_progress,done," || d.Thread.Delivery != api.DeliveryPlanned {
		t.Errorf("issue states = %v, delivery %s", states, d.Thread.Delivery)
	}

	for _, c := range []struct {
		name string
		m    member
		path string
		req  api.DeliveryUpdate
		want int
	}{
		{"unknown delivery", eng, base, api.DeliveryUpdate{Delivery: "shipped"}, 400},
		{"unknown tracker", eng, base, api.DeliveryUpdate{Delivery: api.DeliveryBuilt, Tracker: "asana"}, 400},
		{"wrong tracker", pat, base, api.DeliveryUpdate{Delivery: api.DeliveryBuilt, Tracker: api.OutputJira}, 409},
		{"non-engineer by hand", pat, base, api.DeliveryUpdate{Delivery: api.DeliveryBuilt}, 403},
		{"another team", other, base, api.DeliveryUpdate{Delivery: api.DeliveryBuilt}, 404},
		{"unpublished thread", eng, "/v1/threads/" + f.thread(eng, "Draft").Thread.ID, api.DeliveryUpdate{Delivery: api.DeliveryBuilt}, 409},
	} {
		if s := f.call(c.m, "POST", c.path+"/delivery", c.req, nil); s != c.want {
			t.Errorf("%s: %d, want %d", c.name, s, c.want)
		}
	}

	// The tracker shows the work done.
	f.must(200, pat, "POST", base+"/delivery", api.DeliveryUpdate{Delivery: api.DeliveryBuilt, Tracker: api.OutputLinear,
		Items: []api.IssueState{{Key: "ENG-1", State: api.IssueDone}}}, &out)
	if out.Delivery != api.DeliveryBuilt || out.DeliverySource != api.OutputLinear || out.DeliveryBy != pat.ID {
		t.Errorf("built = %+v", out)
	}
	if got := planned(eng); len(got) != 1 || got[0] != md.ID {
		t.Errorf("planned after build = %v", got)
	}
	// Once it isn't planned, the tracker can't move it again: an engineer's call stands.
	if s := f.call(pat, "POST", base+"/delivery", api.DeliveryUpdate{Delivery: api.DeliveryCancelled, Tracker: api.OutputLinear}, nil); s != 409 {
		t.Errorf("tracker overrode a closed plan: %d", s)
	}

	// Engineers can move it by hand, including back to planned.
	f.must(200, eng, "POST", base+"/delivery", api.DeliveryUpdate{Delivery: api.DeliveryPlanned}, &out)
	if out.Delivery != api.DeliveryPlanned || out.DeliverySource != api.DeliveryManual {
		t.Errorf("back to planned = %+v", out)
	}
	f.must(200, eng, "POST", "/v1/threads/"+md.ID+"/delivery", api.DeliveryUpdate{Delivery: api.DeliveryCancelled}, &out)
	if out.Delivery != api.DeliveryCancelled {
		t.Errorf("markdown plan cancelled by hand = %+v", out)
	}
	// Setting the same value again changes nothing.
	f.must(200, eng, "POST", "/v1/threads/"+md.ID+"/delivery", api.DeliveryUpdate{Delivery: api.DeliveryCancelled}, nil)

	f.must(200, eng, "GET", base, nil, &d)
	var lines []string
	for _, m := range d.Messages {
		if m.Kind == api.MessageSystem && m.Stage == api.StagePlan {
			lines = append(lines, m.Body)
		}
	}
	if got := strings.Join(lines, " | "); got != "published the plan to Linear | found the plan done in Linear | moved the plan back to planned" {
		t.Errorf("plan log: %s", got)
	}
	f.must(200, eng, "GET", "/v1/threads/"+md.ID, nil, &d)
	if last := d.Messages[len(d.Messages)-1]; last.Body != "marked the plan as cancelled" {
		t.Errorf("manual cancel logged as %q", last.Body)
	}

	// Archived plans aren't checked.
	archived := api.ThreadArchived
	f.must(200, eng, "PATCH", base, api.ThreadPatch{Status: &archived}, nil)
	if got := planned(eng); len(got) != 0 {
		t.Errorf("archived plan still checked: %v", got)
	}
}

func TestMigratePublishedThreadsArePlanned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	// A database from before delivery tracking.
	for _, q := range append(append([]string{}, migrations[:2]...),
		`PRAGMA user_version = 2`,
		`INSERT INTO teams (id, name, created_at) VALUES ('team_a', 'A', 0)`,
		`INSERT INTO threads (id, team_id, title, status, current_stage, created_by, created_at, updated_at) VALUES
			('thr_done', 'team_a', 'Done', 'completed', 'plan', 'm', 0, 5), ('thr_wip', 'team_a', 'WIP', 'active', 'design', 'm', 0, 5)`,
		`INSERT INTO exports (id, thread_id, target, member_id, items, created_at) VALUES ('exp_1', 'thr_done', 'markdown', 'm', '[]', 4)`,
	) {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	db.Close()
	if !strings.Contains(migrations[2], "delivery") {
		t.Fatal("this test expects the delivery migration to be the third")
	}

	s, err := New(Config{DBPath: path, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	done, err := getThread(context.Background(), s.db, "team_a", "thr_done")
	if err != nil {
		t.Fatal(err)
	}
	if done.Delivery != api.DeliveryPlanned || done.DeliveryAt == nil || done.DeliveryAt.UnixMilli() != 4 {
		t.Errorf("published thread = %+v", done)
	}
	wip, _ := getThread(context.Background(), s.db, "team_a", "thr_wip")
	if wip.Delivery != "" {
		t.Errorf("unpublished thread delivery = %q", wip.Delivery)
	}
}
