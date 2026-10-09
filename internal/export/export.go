// Package export publishes a finished plan to the team's tracker. Exports
// run on the member's machine with that member's own credentials, so the
// Braid server never holds third-party tokens.
package export

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sam-jeston/braid/internal/api"
	"github.com/sam-jeston/braid/internal/workflow"
)

type Input struct {
	ThreadID    string
	ThreadTitle string
	Plan        *workflow.Plan
	PlanDoc     string            // plan.md, if the agent wrote one
	Docs        map[string]string // approved stage documents by file name
}

var httpClient = &http.Client{Timeout: 30 * time.Second}

// Markdown writes the plan and its supporting documents into a new folder
// under dir and returns one item per ticket.
func Markdown(dir string, in Input) ([]api.ExportItem, string, error) {
	folder := filepath.Join(dir, time.Now().Format("2006-01-02")+"-"+Slug(in.ThreadTitle))
	if err := os.MkdirAll(filepath.Join(folder, "tickets"), 0o755); err != nil {
		return nil, "", err
	}
	write := func(name, content string) error {
		return os.WriteFile(filepath.Join(folder, name), []byte(content), 0o644)
	}
	readme := in.Plan.Markdown()
	if strings.TrimSpace(in.PlanDoc) != "" {
		readme = in.PlanDoc
	}
	if err := write("README.md", readme); err != nil {
		return nil, "", err
	}
	pj, _ := json.MarshalIndent(in.Plan, "", "  ")
	if err := write("plan.json", string(pj)+"\n"); err != nil {
		return nil, "", err
	}
	if len(in.Docs) > 0 {
		if err := os.MkdirAll(filepath.Join(folder, "context"), 0o755); err != nil {
			return nil, "", err
		}
		for name, content := range in.Docs {
			if err := write(filepath.Join("context", filepath.Base(name)), content); err != nil {
				return nil, "", err
			}
		}
	}
	var items []api.ExportItem
	for _, t := range in.Plan.Tickets {
		name := filepath.Join("tickets", fmt.Sprintf("%s-%s.md", t.Ref, Slug(t.Title)))
		if err := write(name, fmt.Sprintf("# %s · %s\n\n%s", t.Ref, t.Title, t.Body())); err != nil {
			return nil, "", err
		}
		items = append(items, api.ExportItem{Ref: t.Ref, Title: t.Title, Key: t.Ref, URL: "file://" + filepath.ToSlash(filepath.Join(folder, name))})
	}
	return items, folder, nil
}

func Slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
		if b.Len() >= 48 {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "plan"
	}
	return out
}

func doJSON(ctx context.Context, method, url string, headers map[string]string, in, out any) (int, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("%s %s: %s: %s", method, url, resp.Status, strings.TrimSpace(string(raw)))
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}
