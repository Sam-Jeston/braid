package export

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sam-jeston/braid/internal/workflow"
)

func samplePlan(t *testing.T) *workflow.Plan {
	p, err := workflow.ParsePlan(`{"summary":"S","epic":{"title":"Digest","description":"D"},"tickets":[
		{"ref":"T1","title":"API","type":"task","role":"engineering","description":"Do **it**","acceptance_criteria":["a"],"estimate":3,"labels":["backend"],"depends_on":[]},
		{"ref":"T2","title":"UI","type":"story","role":"engineering","description":"Build","acceptance_criteria":["b"],"estimate":2,"labels":[],"depends_on":["T1"]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPlanValidation(t *testing.T) {
	bad := []string{
		`{"epic":{"title":""},"tickets":[{"ref":"T1","title":"x","estimate":1}]}`,
		`{"epic":{"title":"E"},"tickets":[]}`,
		`{"epic":{"title":"E"},"tickets":[{"ref":"T1","title":"x","estimate":4}]}`,
		`{"epic":{"title":"E"},"tickets":[{"ref":"T1","title":"x","estimate":1,"depends_on":["T9"]}]}`,
		`{"epic":{"title":"E"},"tickets":[{"ref":"T1","title":"x","estimate":1},{"ref":"T1","title":"y","estimate":1}]}`,
		`not json`,
	}
	for _, b := range bad {
		if _, err := workflow.ParsePlan(b); err == nil {
			t.Errorf("expected error for %s", b)
		}
	}
}

func TestLinearExport(t *testing.T) {
	var created []map[string]any
	relations := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "lin_api_test" {
			w.WriteHeader(401)
			return
		}
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		switch {
		case strings.Contains(req.Query, "teams("):
			w.Write([]byte(`{"data":{"teams":{"nodes":[{"id":"team-1","key":"ENG"}]}}}`))
		case strings.Contains(req.Query, "issueCreate"):
			in := req.Variables["input"].(map[string]any)
			created = append(created, in)
			n := len(created)
			w.Write([]byte(`{"data":{"issueCreate":{"success":true,"issue":{"id":"id-` + string(rune('0'+n)) + `","identifier":"ENG-` + string(rune('0'+n)) + `","url":"https://linear.app/x/ENG-` + string(rune('0'+n)) + `"}}}}`))
		case strings.Contains(req.Query, "issueRelationCreate"):
			relations++
			w.Write([]byte(`{"data":{"issueRelationCreate":{"success":true}}}`))
		}
	}))
	defer srv.Close()
	l := &Linear{APIKey: "lin_api_test", TeamKey: "ENG", Endpoint: srv.URL}
	items, err := l.Export(context.Background(), Input{ThreadTitle: "Digest", Plan: samplePlan(t)})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || items[1].Key != "ENG-2" {
		t.Fatalf("items = %+v", items)
	}
	if created[1]["parentId"] != "id-1" || !strings.Contains(created[1]["description"].(string), "Acceptance criteria") {
		t.Fatalf("child issue input = %+v", created[1])
	}
	if relations != 1 {
		t.Fatalf("relations = %d", relations)
	}
}

func TestJiraExport(t *testing.T) {
	var bodies []map[string]any
	links := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "me@x.com" || p != "tok" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/rest/api/3/issue":
			var b map[string]any
			json.NewDecoder(r.Body).Decode(&b)
			bodies = append(bodies, b)
			w.WriteHeader(201)
			w.Write([]byte(`{"key":"ENG-` + string(rune('0'+len(bodies))) + `"}`))
		case "/rest/api/3/issueLink":
			links++
			w.WriteHeader(201)
		}
	}))
	defer srv.Close()
	j := &Jira{Site: srv.URL, Email: "me@x.com", Token: "tok", ProjectKey: "ENG"}
	items, err := j.Export(context.Background(), Input{Plan: samplePlan(t)})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || items[0].URL != srv.URL+"/browse/ENG-1" {
		t.Fatalf("items = %+v", items)
	}
	child := bodies[1]["fields"].(map[string]any)
	if child["parent"].(map[string]any)["key"] != "ENG-1" {
		t.Fatalf("child not parented: %+v", child)
	}
	if links != 1 {
		t.Fatalf("links = %d", links)
	}
}

func TestADF(t *testing.T) {
	doc := ADF("# Title\n\nSome **bold** and `code` with [link](https://x.y).\n\n- one\n- [ ] two\n\n1. first\n\n```go\nfmt.Println()\n```\n\n---")
	b, _ := json.Marshal(doc)
	s := string(b)
	for _, want := range []string{`"type":"heading"`, `"type":"strong"`, `"type":"code"`, `"href":"https://x.y"`, `"type":"bulletList"`, `"type":"orderedList"`, `"type":"codeBlock"`, `"language":"go"`, `"type":"rule"`} {
		if !strings.Contains(s, want) {
			t.Errorf("ADF missing %s: %s", want, s)
		}
	}
}
