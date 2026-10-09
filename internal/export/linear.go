package export

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/sam-jeston/braid/internal/api"
)

// Linear creates a parent issue for the epic, one sub-issue per ticket, and
// "blocks" relations for dependencies.
type Linear struct {
	APIKey   string
	TeamKey  string
	Endpoint string // defaults to https://api.linear.app/graphql
}

type gqlResp[T any] struct {
	Data   T `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

func linearCall[T any](ctx context.Context, l *Linear, query string, vars map[string]any) (T, error) {
	var out gqlResp[T]
	endpoint := l.Endpoint
	if endpoint == "" {
		endpoint = "https://api.linear.app/graphql"
	}
	_, err := doJSON(ctx, "POST", endpoint, map[string]string{"Authorization": l.APIKey},
		map[string]any{"query": query, "variables": vars}, &out)
	if err != nil {
		return out.Data, err
	}
	if len(out.Errors) > 0 {
		var msgs []string
		for _, e := range out.Errors {
			msgs = append(msgs, e.Message)
		}
		return out.Data, errors.New("linear: " + strings.Join(msgs, "; "))
	}
	return out.Data, nil
}

type linearIssue struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	URL        string `json:"url"`
}

func (l *Linear) createIssue(ctx context.Context, input map[string]any) (linearIssue, error) {
	data, err := linearCall[struct {
		IssueCreate struct {
			Success bool        `json:"success"`
			Issue   linearIssue `json:"issue"`
		} `json:"issueCreate"`
	}](ctx, l, `mutation($input: IssueCreateInput!) { issueCreate(input: $input) { success issue { id identifier url } } }`,
		map[string]any{"input": input})
	if err != nil {
		return linearIssue{}, err
	}
	if !data.IssueCreate.Success {
		return linearIssue{}, errors.New("linear: issueCreate was not successful")
	}
	return data.IssueCreate.Issue, nil
}

func (l *Linear) Export(ctx context.Context, in Input) ([]api.ExportItem, error) {
	if l.APIKey == "" {
		return nil, errors.New("add your Linear API key in Settings → This device")
	}
	teams, err := linearCall[struct {
		Teams struct {
			Nodes []struct {
				ID  string `json:"id"`
				Key string `json:"key"`
			} `json:"nodes"`
		} `json:"teams"`
	}](ctx, l, `query($key: String!) { teams(filter: { key: { eq: $key } }) { nodes { id key } } }`,
		map[string]any{"key": l.TeamKey})
	if err != nil {
		return nil, err
	}
	if len(teams.Teams.Nodes) == 0 {
		return nil, fmt.Errorf("linear: no team with key %q is visible to this API key", l.TeamKey)
	}
	teamID := teams.Teams.Nodes[0].ID

	epicDesc := strings.TrimSpace(in.Plan.Summary + "\n\n" + in.Plan.Epic.Description)
	epic, err := l.createIssue(ctx, map[string]any{"teamId": teamID, "title": in.Plan.Epic.Title, "description": epicDesc})
	if err != nil {
		return nil, err
	}
	items := []api.ExportItem{{Ref: "epic", Title: in.Plan.Epic.Title, Key: epic.Identifier, URL: epic.URL}}
	ids := map[string]string{}
	for _, t := range in.Plan.Tickets {
		iss, err := l.createIssue(ctx, map[string]any{"teamId": teamID, "title": t.Title, "description": t.Body(), "parentId": epic.ID})
		if err != nil {
			return items, fmt.Errorf("created %d of %d tickets, then: %w", len(items)-1, len(in.Plan.Tickets), err)
		}
		ids[t.Ref] = iss.ID
		items = append(items, api.ExportItem{Ref: t.Ref, Title: t.Title, Key: iss.Identifier, URL: iss.URL})
	}
	for _, t := range in.Plan.Tickets {
		for _, dep := range t.DependsOn {
			if ids[dep] == "" || ids[t.Ref] == "" {
				continue
			}
			// Best effort: a missing relation shouldn't fail the whole export.
			_, _ = linearCall[map[string]any](ctx, l,
				`mutation($input: IssueRelationCreateInput!) { issueRelationCreate(input: $input) { success } }`,
				map[string]any{"input": map[string]any{"issueId": ids[dep], "relatedIssueId": ids[t.Ref], "type": "blocks"}})
		}
	}
	return items, nil
}
