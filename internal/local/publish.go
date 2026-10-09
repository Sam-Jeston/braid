package local

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

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
	return OpenTarget(target)
}

// openWait is how long OpenTarget waits for an opener to fail before
// assuming it worked.
var openWait = 3 * time.Second

// OpenTarget hands target to the operating system's opener. Openers report
// failure through their exit status (gio with no registered browser, for
// example), so it waits briefly for one and returns it as an error. Openers
// still running after openWait, like xdg-open starting a browser in the
// foreground, are left to finish on their own.
func OpenTarget(target string) error {
	cmd, checkExit, err := openCommand(target)
	if err != nil {
		return err
	}
	return runOpener(cmd, target, checkExit)
}

func runOpener(cmd *exec.Cmd, target string, checkExit bool) error {
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	// Don't let a browser that inherits stderr hold Wait open forever.
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("couldn't start %s to open %s: %w", filepath.Base(cmd.Path), target, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		// ErrWaitDelay means the opener succeeded but something it launched kept stderr.
		if err != nil && checkExit && !errors.Is(err, exec.ErrWaitDelay) {
			detail := strings.TrimSpace(stderr.String())
			if detail == "" {
				detail = err.Error()
			}
			return fmt.Errorf("%s couldn't open %s (%s): check that a default browser and file manager are set up",
				filepath.Base(cmd.Path), target, detail)
		}
		return nil
	case <-time.After(openWait):
		return nil
	}
}

// openCommand picks the platform's "open this" command. checkExit is false
// for openers whose exit status means nothing (explorer.exe returns 1 even
// when it succeeds).
func openCommand(target string) (cmd *exec.Cmd, checkExit bool, err error) {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", target), true, nil
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", target), true, nil
	}
	// Under WSL, Linux openers are usually missing or have no handlers
	// registered, so hand off to Windows first.
	openers := []string{"xdg-open", "gio"}
	if isWSL() {
		openers = []string{"wslview", "explorer.exe", "xdg-open", "gio"}
	}
	for _, opener := range openers {
		p, err := exec.LookPath(opener)
		if err != nil {
			continue
		}
		switch opener {
		case "gio":
			return exec.Command(p, "open", target), true, nil
		case "explorer.exe":
			if !strings.HasPrefix(target, "http") {
				if out, err := exec.Command("wslpath", "-w", target).Output(); err == nil {
					target = strings.TrimSpace(string(out))
				}
			}
			return exec.Command(p, target), false, nil
		}
		return exec.Command(p, target), true, nil
	}
	return nil, false, fmt.Errorf("no program available to open %s: install xdg-utils, or wslu under WSL", target)
}

func isWSL() bool {
	if os.Getenv("WSL_DISTRO_NAME") != "" {
		return true
	}
	_, err := os.Stat("/proc/sys/fs/binfmt_misc/WSLInterop")
	return err == nil
}
