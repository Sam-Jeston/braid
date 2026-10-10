package export

import (
	"context"
	"errors"
	"regexp"
	"strconv"

	"github.com/sam-jeston/braid/internal/api"
)

// Tracker reads back the state of issues a plan was published as, so Braid
// can tell when the work was built or cancelled.
type Tracker interface {
	// States returns the state (api.Issue*) of each issue key it can see.
	// Keys that no longer exist are left out rather than failing the call.
	States(ctx context.Context, keys []string) (map[string]string, error)
}

// Outcome decides what became of a published plan from its issues' states:
//
//   - built when the epic is done, or every ticket is closed and at least one was done;
//   - cancelled when the epic is cancelled, or every ticket was;
//   - otherwise still planned.
//
// Items without a state (deleted, or not visible to the member checking) are ignored.
func Outcome(items []api.ExportItem) api.Delivery {
	var epic string
	tickets, closed, done := 0, 0, 0
	for _, it := range items {
		if it.Ref == "epic" {
			epic = it.State
			continue
		}
		if it.State == "" {
			continue
		}
		tickets++
		switch it.State {
		case api.IssueDone:
			closed++
			done++
		case api.IssueCancelled:
			closed++
		}
	}
	switch {
	case epic == api.IssueDone:
		return api.DeliveryBuilt
	case epic == api.IssueCancelled:
		return api.DeliveryCancelled
	case tickets > 0 && closed == tickets && done > 0:
		return api.DeliveryBuilt
	case tickets > 0 && closed == tickets:
		return api.DeliveryCancelled
	}
	return api.DeliveryPlanned
}

// --- Linear ---

// States looks issues up by identifier (ENG-123): one request per team key,
// filtering on issue numbers. (issue(id:) can't be batched: its result is
// non-null, so one deleted issue would fail the whole query.)
func (l *Linear) States(ctx context.Context, keys []string) (map[string]string, error) {
	if l.APIKey == "" {
		return nil, errors.New("add your Linear API key in Settings → This device")
	}
	byTeam := map[string][]float64{}
	var teams []string
	for _, k := range keys {
		m := linearKey.FindStringSubmatch(k)
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[2])
		if byTeam[m[1]] == nil {
			teams = append(teams, m[1])
		}
		byTeam[m[1]] = append(byTeam[m[1]], float64(n))
	}
	out := map[string]string{}
	for _, team := range teams {
		nums := byTeam[team]
		for start := 0; start < len(nums); start += 100 {
			data, err := linearCall[struct {
				Issues struct {
					Nodes []struct {
						Identifier string `json:"identifier"`
						State      struct {
							Type string `json:"type"`
						} `json:"state"`
					} `json:"nodes"`
				} `json:"issues"`
			}](ctx, l, `query($team: String!, $numbers: [Float!]) {
				issues(filter: { team: { key: { eq: $team } }, number: { in: $numbers } }, first: 100, includeArchived: true) {
					nodes { identifier state { type } }
				}
			}`, map[string]any{"team": team, "numbers": nums[start:min(start+100, len(nums))]})
			if err != nil {
				return nil, err
			}
			for _, iss := range data.Issues.Nodes {
				out[iss.Identifier] = linearState(iss.State.Type)
			}
		}
	}
	return out, nil
}

var linearKey = regexp.MustCompile(`^([A-Z0-9]+)-([0-9]+)$`)

// linearState maps a Linear workflow state type (triage, backlog, unstarted,
// started, completed, canceled).
func linearState(typ string) string {
	switch typ {
	case "completed":
		return api.IssueDone
	case "canceled":
		return api.IssueCancelled
	case "started":
		return api.IssueInProgress
	}
	return api.IssueTodo
}

// --- Jira ---

// States uses the bulk fetch endpoint, up to 100 issues per request. Unlike
// a JQL search, it returns the issues it found even when some keys no longer
// exist.
func (j *Jira) States(ctx context.Context, keys []string) (map[string]string, error) {
	if j.Email == "" || j.Token == "" {
		return nil, errors.New("add your Jira email and API token in Settings → This device")
	}
	out := map[string]string{}
	for start := 0; start < len(keys); start += 100 {
		batch := keys[start:min(start+100, len(keys))]
		var resp struct {
			Issues []struct {
				Key    string `json:"key"`
				Fields struct {
					Status struct {
						Name           string `json:"name"`
						StatusCategory struct {
							Key string `json:"key"`
						} `json:"statusCategory"`
					} `json:"status"`
					Resolution *struct {
						Name string `json:"name"`
					} `json:"resolution"`
				} `json:"fields"`
			} `json:"issues"`
		}
		_, err := doJSON(ctx, "POST", j.Site+"/rest/api/3/issue/bulkfetch", j.headers(),
			map[string]any{"issueIdsOrKeys": batch, "fields": []string{"status", "resolution"}}, &resp)
		if err != nil {
			return nil, err
		}
		asked := map[string]bool{}
		for _, k := range batch {
			asked[k] = true
		}
		for _, iss := range resp.Issues {
			if !asked[iss.Key] {
				continue // moved to another key; we can't tell which one we asked for
			}
			resolution := ""
			if iss.Fields.Resolution != nil {
				resolution = iss.Fields.Resolution.Name
			}
			out[iss.Key] = jiraState(iss.Fields.Status.StatusCategory.Key, iss.Fields.Status.Name, resolution)
		}
	}
	return out, nil
}

// Jira has no "cancelled" status category: closed issues are all "done", and
// the status or resolution name says whether the work happened.
var jiraCancelled = regexp.MustCompile(`(?i)cancel|won'?t (do|fix)|reject|declin|duplicate|obsolete|abandon`)

func jiraState(category, status, resolution string) string {
	switch category {
	case "done":
		if jiraCancelled.MatchString(status) || jiraCancelled.MatchString(resolution) {
			return api.IssueCancelled
		}
		return api.IssueDone
	case "indeterminate":
		return api.IssueInProgress
	}
	return api.IssueTodo
}
