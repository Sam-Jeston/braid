package export

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/sam-jeston/braid/internal/api"
)

// Jira creates an epic plus child issues (via the parent field) in Jira Cloud,
// linking dependencies with "Blocks".
type Jira struct {
	Site       string // https://acme.atlassian.net
	Email      string
	Token      string
	ProjectKey string
	EpicType   string // default "Epic"
	IssueType  string // default "Task"
}

func (j *Jira) headers() map[string]string {
	auth := base64.StdEncoding.EncodeToString([]byte(j.Email + ":" + j.Token))
	return map[string]string{"Authorization": "Basic " + auth}
}

func (j *Jira) create(ctx context.Context, fields map[string]any) (string, error) {
	var out struct {
		Key string `json:"key"`
	}
	_, err := doJSON(ctx, "POST", j.Site+"/rest/api/3/issue", j.headers(), map[string]any{"fields": fields}, &out)
	return out.Key, err
}

func (j *Jira) Export(ctx context.Context, in Input) ([]api.ExportItem, error) {
	if j.Email == "" || j.Token == "" {
		return nil, errors.New("add your Jira email and API token in Settings → This device")
	}
	epicType, issueType := j.EpicType, j.IssueType
	if epicType == "" {
		epicType = "Epic"
	}
	if issueType == "" {
		issueType = "Task"
	}
	project := map[string]any{"key": j.ProjectKey}
	epicKey, err := j.create(ctx, map[string]any{
		"project": project, "summary": in.Plan.Epic.Title, "issuetype": map[string]any{"name": epicType},
		"description": ADF(strings.TrimSpace(in.Plan.Summary + "\n\n" + in.Plan.Epic.Description)),
	})
	if err != nil {
		return nil, err
	}
	items := []api.ExportItem{{Ref: "epic", Title: in.Plan.Epic.Title, Key: epicKey, URL: j.Site + "/browse/" + epicKey}}
	keys := map[string]string{}
	for _, t := range in.Plan.Tickets {
		fields := map[string]any{
			"project": project, "summary": t.Title, "issuetype": map[string]any{"name": issueType},
			"description": ADF(t.Body()), "parent": map[string]any{"key": epicKey},
		}
		if len(t.Labels) > 0 {
			fields["labels"] = jiraLabels(t.Labels)
		}
		key, err := j.create(ctx, fields)
		if err != nil && strings.Contains(err.Error(), "parent") {
			// Some project configurations don't allow parent links; create unlinked.
			delete(fields, "parent")
			key, err = j.create(ctx, fields)
		}
		if err != nil {
			return items, fmt.Errorf("created %d of %d tickets, then: %w", len(items)-1, len(in.Plan.Tickets), err)
		}
		keys[t.Ref] = key
		items = append(items, api.ExportItem{Ref: t.Ref, Title: t.Title, Key: key, URL: j.Site + "/browse/" + key})
	}
	for _, t := range in.Plan.Tickets {
		for _, dep := range t.DependsOn {
			if keys[dep] == "" || keys[t.Ref] == "" {
				continue
			}
			_, _ = doJSON(ctx, "POST", j.Site+"/rest/api/3/issueLink", j.headers(), map[string]any{
				"type":         map[string]any{"name": "Blocks"},
				"outwardIssue": map[string]any{"key": keys[dep]},
				"inwardIssue":  map[string]any{"key": keys[t.Ref]},
			}, nil)
		}
	}
	return items, nil
}

func jiraLabels(ls []string) []string {
	var out []string
	for _, l := range ls {
		l = strings.ReplaceAll(strings.TrimSpace(l), " ", "-")
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// --- Markdown → Atlassian Document Format (the subset agents produce) ---

type node = map[string]any

var (
	reHeading = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	reBullet  = regexp.MustCompile(`^\s*[-*+]\s+(?:\[[ xX]\]\s+)?(.*)$`)
	reOrdered = regexp.MustCompile(`^\s*\d+[.)]\s+(.*)$`)
	reRule    = regexp.MustCompile(`^\s*(?:-{3,}|\*{3,}|_{3,})\s*$`)
	reInline  = regexp.MustCompile("`([^`]+)`|\\*\\*([^*]+)\\*\\*|\\[([^\\]]+)\\]\\(([^)\\s]+)\\)")
)

func ADF(md string) node {
	var content []any
	lines := strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
	var para []string
	var list node
	var listType string
	flushPara := func() {
		if len(para) > 0 {
			content = append(content, node{"type": "paragraph", "content": inline(strings.Join(para, " "))})
			para = nil
		}
	}
	flushList := func() {
		if list != nil {
			content = append(content, list)
			list, listType = nil, ""
		}
	}
	addItem := func(typ, text string) {
		flushPara()
		if listType != typ {
			flushList()
			list, listType = node{"type": typ, "content": []any{}}, typ
		}
		list["content"] = append(list["content"].([]any), node{"type": "listItem", "content": []any{
			node{"type": "paragraph", "content": inline(text)},
		}})
	}
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		switch {
		case strings.HasPrefix(strings.TrimSpace(l), "```"):
			flushPara()
			flushList()
			lang := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "```"))
			var code []string
			for i++; i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "```"); i++ {
				code = append(code, lines[i])
			}
			cb := node{"type": "codeBlock", "content": []any{node{"type": "text", "text": strings.Join(code, "\n") + " "}}}
			if lang != "" {
				cb["attrs"] = node{"language": lang}
			}
			content = append(content, cb)
		case strings.TrimSpace(l) == "":
			flushPara()
			flushList()
		case reRule.MatchString(l):
			flushPara()
			flushList()
			content = append(content, node{"type": "rule"})
		case reHeading.MatchString(l):
			flushPara()
			flushList()
			m := reHeading.FindStringSubmatch(l)
			content = append(content, node{"type": "heading", "attrs": node{"level": len(m[1])}, "content": inline(m[2])})
		case reBullet.MatchString(l):
			addItem("bulletList", reBullet.FindStringSubmatch(l)[1])
		case reOrdered.MatchString(l):
			addItem("orderedList", reOrdered.FindStringSubmatch(l)[1])
		default:
			flushList()
			para = append(para, strings.TrimSpace(l))
		}
	}
	flushPara()
	flushList()
	if len(content) == 0 {
		content = []any{node{"type": "paragraph", "content": []any{}}}
	}
	return node{"type": "doc", "version": 1, "content": content}
}

func inline(s string) []any {
	var out []any
	text := func(t string, marks ...node) {
		if t == "" {
			return
		}
		n := node{"type": "text", "text": t}
		if len(marks) > 0 {
			ms := make([]any, len(marks))
			for i, m := range marks {
				ms[i] = m
			}
			n["marks"] = ms
		}
		out = append(out, n)
	}
	last := 0
	for _, m := range reInline.FindAllStringSubmatchIndex(s, -1) {
		text(s[last:m[0]])
		switch {
		case m[2] >= 0:
			text(s[m[2]:m[3]], node{"type": "code"})
		case m[4] >= 0:
			text(s[m[4]:m[5]], node{"type": "strong"})
		case m[6] >= 0:
			text(s[m[6]:m[7]], node{"type": "link", "attrs": node{"href": s[m[8]:m[9]]}})
		}
		last = m[1]
	}
	text(s[last:])
	if out == nil {
		out = []any{}
	}
	return out
}
