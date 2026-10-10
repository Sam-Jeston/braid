package export

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/sam-jeston/braid/internal/api"
)

func TestOutcome(t *testing.T) {
	item := func(ref, state string) api.ExportItem { return api.ExportItem{Ref: ref, Key: ref, State: state} }
	cases := []struct {
		name  string
		items []api.ExportItem
		want  api.Delivery
	}{
		{"nothing known yet", []api.ExportItem{item("epic", ""), item("T1", ""), item("T2", "")}, api.DeliveryPlanned},
		{"work in progress", []api.ExportItem{item("epic", "in_progress"), item("T1", "done"), item("T2", "in_progress")}, api.DeliveryPlanned},
		{"epic closed", []api.ExportItem{item("epic", "done"), item("T1", "done"), item("T2", "todo")}, api.DeliveryBuilt},
		{"epic cancelled", []api.ExportItem{item("epic", "cancelled"), item("T1", "done")}, api.DeliveryCancelled},
		{"every ticket closed, some done", []api.ExportItem{item("epic", "todo"), item("T1", "done"), item("T2", "cancelled")}, api.DeliveryBuilt},
		{"every ticket cancelled", []api.ExportItem{item("epic", "todo"), item("T1", "cancelled"), item("T2", "cancelled")}, api.DeliveryCancelled},
		{"a deleted ticket doesn't block", []api.ExportItem{item("epic", "in_progress"), item("T1", "done"), item("T2", "")}, api.DeliveryBuilt},
		{"no tickets visible", []api.ExportItem{item("epic", "in_progress")}, api.DeliveryPlanned},
	}
	for _, c := range cases {
		if got := Outcome(c.items); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func TestLinearStates(t *testing.T) {
	type query struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	var queries []query
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "lin_api_test" {
			w.WriteHeader(401)
			return
		}
		var q query
		json.NewDecoder(r.Body).Decode(&q)
		queries = append(queries, q)
		switch q.Variables["team"] {
		case "ENG": // ENG-3 was deleted, so it isn't returned
			w.Write([]byte(`{"data":{"issues":{"nodes":[
				{"identifier":"ENG-1","state":{"type":"completed"}},
				{"identifier":"ENG-2","state":{"type":"started"}},
				{"identifier":"ENG-4","state":{"type":"canceled"}}]}}}`))
		case "WEB":
			w.Write([]byte(`{"data":{"issues":{"nodes":[{"identifier":"WEB-10","state":{"type":"backlog"}}]}}}`))
		}
	}))
	defer srv.Close()
	l := &Linear{APIKey: "lin_api_test", Endpoint: srv.URL}
	got, err := l.States(context.Background(), []string{"ENG-1", "ENG-2", "ENG-3", "ENG-4", "WEB-10", "not a key"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"ENG-1": "done", "ENG-2": "in_progress", "ENG-4": "cancelled", "WEB-10": "todo"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("states = %v", got)
	}
	if len(queries) != 2 || !strings.Contains(queries[0].Query, "includeArchived: true") {
		t.Fatalf("queries = %+v", queries)
	}
	if nums := queries[0].Variables["numbers"].([]any); len(nums) != 4 || nums[0].(float64) != 1 {
		t.Errorf("numbers = %v", nums)
	}

	if _, err := (&Linear{APIKey: "wrong", Endpoint: srv.URL}).States(context.Background(), []string{"ENG-1"}); err == nil {
		t.Error("a rejected API key should fail the check")
	}
	if _, err := (&Linear{}).States(context.Background(), []string{"ENG-1"}); err == nil || !strings.Contains(err.Error(), "Settings") {
		t.Errorf("no key: %v", err)
	}
}

func TestJiraStates(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "me@x.com" || p != "tok" || r.URL.Path != "/rest/api/3/issue/bulkfetch" {
			w.WriteHeader(401)
			return
		}
		var req struct {
			Keys   []string `json:"issueIdsOrKeys"`
			Fields []string `json:"fields"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		asked = req.Keys
		if !reflect.DeepEqual(req.Fields, []string{"status", "resolution"}) {
			t.Errorf("fields = %v", req.Fields)
		}
		// ENG-5 was deleted; ENG-6 moved and comes back under its new key.
		w.Write([]byte(`{"issues":[
			{"key":"ENG-1","fields":{"status":{"name":"Done","statusCategory":{"key":"done"}},"resolution":{"name":"Done"}}},
			{"key":"ENG-2","fields":{"status":{"name":"In Review","statusCategory":{"key":"indeterminate"}},"resolution":null}},
			{"key":"ENG-3","fields":{"status":{"name":"Closed","statusCategory":{"key":"done"}},"resolution":{"name":"Won't Do"}}},
			{"key":"ENG-4","fields":{"status":{"name":"To Do","statusCategory":{"key":"new"}},"resolution":null}},
			{"key":"OPS-9","fields":{"status":{"name":"Done","statusCategory":{"key":"done"}},"resolution":null}}
		],"issueErrors":[{"id":"ENG-5"}]}`))
	}))
	defer srv.Close()
	j := &Jira{Site: srv.URL, Email: "me@x.com", Token: "tok"}
	got, err := j.States(context.Background(), []string{"ENG-1", "ENG-2", "ENG-3", "ENG-4", "ENG-5", "ENG-6"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"ENG-1": "done", "ENG-2": "in_progress", "ENG-3": "cancelled", "ENG-4": "todo"}
	if !reflect.DeepEqual(got, want) || len(asked) != 6 {
		t.Errorf("states = %v (asked %v)", got, asked)
	}
	if _, err := (&Jira{Site: srv.URL, Email: "me@x.com", Token: "bad"}).States(context.Background(), []string{"ENG-1"}); err == nil {
		t.Error("rejected credentials should fail the check")
	}
}

func TestJiraCancelledStatuses(t *testing.T) {
	for _, c := range []struct{ status, resolution, want string }{
		{"Done", "Done", "done"},
		{"Closed", "Fixed", "done"},
		{"Cancelled", "", "cancelled"},
		{"Closed", "Won't Do", "cancelled"},
		{"Closed", "Duplicate", "cancelled"},
		{"Rejected", "", "cancelled"},
	} {
		if got := jiraState("done", c.status, c.resolution); got != c.want {
			t.Errorf("%s/%s: %s", c.status, c.resolution, got)
		}
	}
}
