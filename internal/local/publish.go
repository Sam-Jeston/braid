package local

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/sam-jeston/braid/internal/api"
	"github.com/sam-jeston/braid/internal/export"
	"github.com/sam-jeston/braid/internal/workflow"
)

type PublishResult struct {
	Export api.Export `json:"export"`
	Folder string     `json:"folder,omitempty"`
}

func (c *Core) exportInput(ctx context.Context, threadID string) (export.Input, api.Team, *session, error) {
	s, err := c.session()
	if err != nil {
		return export.Input{}, api.Team{}, nil, err
	}
	team, err := s.freshTeam(ctx)
	if err != nil {
		return export.Input{}, team, s, err
	}
	d, err := s.client.Thread(ctx, threadID)
	if err != nil {
		return export.Input{}, team, s, err
	}
	in := export.Input{ThreadID: d.Thread.ID, ThreadTitle: d.Thread.Title, Docs: map[string]string{}}
	for _, a := range d.Artifacts {
		switch {
		case a.Stage == api.StagePlan && a.Path == "plan.json":
			if in.Plan, err = workflow.ParsePlan(a.Content); err != nil {
				return in, team, s, err
			}
		case a.Stage == api.StagePlan && a.Path == "plan.md":
			in.PlanDoc = a.Content
		case strings.HasSuffix(a.Path, ".md"):
			in.Docs[a.Path] = a.Content
		}
	}
	if in.Plan == nil {
		return in, team, s, errors.New("this thread has no plan yet")
	}
	return in, team, s, nil
}

// Publish exports the plan to the team's configured tracker using this
// member's local credentials, then records the export on the server.
func (c *Core) Publish(ctx context.Context, threadID string) (PublishResult, error) {
	in, team, s, err := c.exportInput(ctx, threadID)
	if err != nil {
		return PublishResult{}, err
	}
	var res PublishResult
	var items []api.ExportItem
	switch team.Output.Type {
	case api.OutputLinear:
		key, _ := c.secrets.Get(secretLinear)
		l := &export.Linear{APIKey: key, TeamKey: team.Output.Config["team_key"]}
		items, err = l.Export(ctx, in)
	case api.OutputJira:
		token, _ := c.secrets.Get(secretJira)
		c.mu.Lock()
		email := c.cfg.JiraEmail
		c.mu.Unlock()
		j := &export.Jira{
			Site: team.Output.Config["site"], Email: email, Token: token, ProjectKey: team.Output.Config["project_key"],
			EpicType: team.Output.Config["epic_type"], IssueType: team.Output.Config["issue_type"],
		}
		items, err = j.Export(ctx, in)
	default:
		c.mu.Lock()
		dir := c.exportDir()
		c.mu.Unlock()
		items, res.Folder, err = export.Markdown(dir, in)
	}
	if err != nil && len(items) == 0 {
		return res, err
	}
	target := team.Output.Type
	if target == "" {
		target = api.OutputMarkdown
	}
	exp, rerr := s.client.CreateExport(ctx, threadID, api.CreateExportRequest{Target: target, Items: items})
	res.Export = exp
	if err != nil {
		// Partial export: record what was created so nobody publishes twice.
		return res, fmt.Errorf("partially published: %w", err)
	}
	return res, rerr
}

// SaveMarkdown writes the plan locally without marking the thread published.
func (c *Core) SaveMarkdown(ctx context.Context, threadID string) (string, error) {
	in, _, _, err := c.exportInput(ctx, threadID)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	dir := c.exportDir()
	c.mu.Unlock()
	_, folder, err := export.Markdown(dir, in)
	return folder, err
}

// Open hands a URL or exported folder to the operating system.
func (c *Core) Open(target string) error {
	u, err := url.Parse(target)
	if err != nil {
		return err
	}
	switch u.Scheme {
	case "http", "https":
	case "file", "":
		c.mu.Lock()
		dir := c.exportDir()
		c.mu.Unlock()
		p := target
		if u.Scheme == "file" {
			p = u.Path
		}
		p = filepath.Clean(p)
		if p != dir && !strings.HasPrefix(p, dir+string(filepath.Separator)) {
			return errors.New("only exported files can be opened")
		}
		target = p
	default:
		return fmt.Errorf("refusing to open %q", u.Scheme)
	}
	cmd, err := OpenCommand(target)
	if err != nil {
		return err
	}
	return cmd.Start()
}

// OpenCommand picks the platform's "open this" command, including WSL,
// where Linux GUI openers are often missing but Windows' explorer works.
func OpenCommand(target string) (*exec.Cmd, error) {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", target), nil
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", target), nil
	}
	for _, opener := range []string{"xdg-open", "wslview", "gio"} {
		if p, err := exec.LookPath(opener); err == nil {
			if opener == "gio" {
				return exec.Command(p, "open", target), nil
			}
			return exec.Command(p, target), nil
		}
	}
	if explorer, err := exec.LookPath("explorer.exe"); err == nil {
		if !strings.HasPrefix(target, "http") {
			if out, err := exec.Command("wslpath", "-w", target).Output(); err == nil {
				target = strings.TrimSpace(string(out))
			}
		}
		return exec.Command(explorer, target), nil
	}
	return nil, fmt.Errorf("no program available to open %s", target)
}
