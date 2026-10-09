package local

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sam-jeston/braid/internal/agents"
	"github.com/sam-jeston/braid/internal/api"
	"github.com/sam-jeston/braid/internal/client"
	"github.com/sam-jeston/braid/internal/workflow"
)

type localRun struct {
	run        api.Run
	cancel     context.CancelFunc
	syncedRepo bool
}

type StartRunInput struct {
	ThreadID         string    `json:"thread_id"`
	Stage            api.Stage `json:"stage"`
	TriggerMessageID string    `json:"trigger_message_id"`
}

func (c *Core) pickAgent(ctx context.Context) (agents.Agent, string, string, error) {
	c.mu.Lock()
	id, model := c.cfg.Agent, c.cfg.AgentModel
	c.mu.Unlock()
	if id == "" {
		return nil, "", "", errors.New("no coding agent found on this machine: install Claude Code or Codex, then pick it in Settings")
	}
	a, ok := agents.Get(id)
	if !ok {
		return nil, "", "", fmt.Errorf("unknown agent %q; pick one in Settings", id)
	}
	v, err := a.Detect(ctx)
	if err != nil {
		return nil, "", "", fmt.Errorf("%s isn't available on this machine: %v", a.Name(), err)
	}
	return a, v, model, nil
}

// StartRun claims the stage on the server (so teammates see the agent start
// immediately) and executes the agent in the background on this machine.
func (c *Core) StartRun(ctx context.Context, in StartRunInput) (api.Run, error) {
	s, err := c.session()
	if err != nil {
		return api.Run{}, err
	}
	agent, ver, model, err := c.pickAgent(ctx)
	if err != nil {
		return api.Run{}, err
	}
	run, err := s.client.CreateRun(ctx, api.CreateRunRequest{
		Kind: api.RunKindStage, ThreadID: in.ThreadID, Stage: in.Stage, Agent: agent.ID(), AgentVersion: ver, TriggerMessageID: in.TriggerMessageID,
	})
	if err != nil {
		return run, err
	}
	c.launch(s, run, agent, model)
	return run, nil
}

// StartScan runs the codebase scout, which writes the team's codebase brief and rules.
func (c *Core) StartScan(ctx context.Context) (api.Run, error) {
	s, err := c.session()
	if err != nil {
		return api.Run{}, err
	}
	agent, ver, model, err := c.pickAgent(ctx)
	if err != nil {
		return api.Run{}, err
	}
	run, err := s.client.CreateRun(ctx, api.CreateRunRequest{Kind: api.RunKindScan, Agent: agent.ID(), AgentVersion: ver})
	if err != nil {
		return run, err
	}
	c.launch(s, run, agent, model)
	return run, nil
}

// maxRunDuration bounds a single agent run (override with BRAID_MAX_RUN_MINUTES).
func maxRunDuration() time.Duration {
	if m, err := strconv.Atoi(os.Getenv("BRAID_MAX_RUN_MINUTES")); err == nil && m > 0 {
		return time.Duration(m) * time.Minute
	}
	return 45 * time.Minute
}

func (c *Core) launch(s *session, run api.Run, agent agents.Agent, model string) {
	ctx, cancel := context.WithTimeout(context.Background(), maxRunDuration())
	lr := &localRun{run: run, cancel: cancel}
	c.mu.Lock()
	c.runs[run.ID] = lr
	c.mu.Unlock()
	c.wg.Add(1)
	c.bus.PublishLocal("local.runs", nil)
	go func() {
		defer c.wg.Done()
		defer cancel()
		c.execute(ctx, s, lr, agent, model)
		c.mu.Lock()
		delete(c.runs, run.ID)
		c.mu.Unlock()
		c.bus.PublishLocal("local.runs", nil)
	}()
}

// cancelLocalRun stops a run executing on this machine. Returns false if the
// run isn't local.
func (c *Core) cancelLocalRun(runID string) bool {
	c.mu.Lock()
	r := c.runs[runID]
	c.mu.Unlock()
	if r == nil {
		return false
	}
	r.cancel()
	return true
}

// CancelRun stops a local run, or asks the owning teammate's machine to stop it.
func (c *Core) CancelRun(ctx context.Context, runID string) error {
	if c.cancelLocalRun(runID) {
		return nil
	}
	s, err := c.session()
	if err != nil {
		return err
	}
	return s.client.Do(ctx, "POST", "/v1/runs/"+runID+"/cancel", nil, nil)
}

func (c *Core) execute(ctx context.Context, s *session, lr *localRun, agent agents.Agent, model string) {
	run := lr.run
	out := newPump(s.client, run.ID, c.log, lr.cancel)
	finish := api.FinishRunRequest{Status: api.RunFailed}
	runDir := filepath.Join(c.home, "runs", run.ID)
	defer func() {
		out.close()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) && finish.Status != api.RunSucceeded {
			finish.Status, finish.Error, finish.Artifacts = api.RunFailed, fmt.Sprintf("timed out after %s", maxRunDuration()), nil
		} else if ctx.Err() != nil && finish.Status != api.RunSucceeded {
			finish.Status, finish.Error, finish.Artifacts = api.RunCancelled, "cancelled", nil
		}
		c.finish(s.client, run.ID, finish)
		if os.Getenv("BRAID_KEEP_RUNS") != "1" {
			os.RemoveAll(runDir)
		}
	}()
	fail := func(err error) {
		finish.Error = err.Error()
		out.emit(api.RunOutput{Kind: "error", Text: err.Error()})
	}

	out.emit(api.RunOutput{Kind: "status", Text: fmt.Sprintf("Running %s %s on %s", agent.Name(), versionLabel(agent, model), c.deviceName())})

	// 1. Codebase checkout.
	team, err := s.freshTeam(ctx)
	if err != nil {
		fail(err)
		return
	}
	repoInfo := c.prepareRepo(ctx, s, lr, team, out)
	finish.CommitSHA = repoInfo.Revision()
	if ctx.Err() != nil {
		return
	}
	if run.Kind == api.RunKindScan && !repoInfo.Available() {
		fail(fmt.Errorf("there's no codebase to scan on this machine: %s", repoInfo.Unavailable()))
		return
	}

	// 2. Context from the shared thread state.
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		fail(err)
		return
	}
	spec := agents.Spec{Dir: runDir, Model: model, Stage: string(run.Stage)}
	for _, co := range repoInfo.Checkouts {
		spec.Repos = append(spec.Repos, agents.Repo{Name: co.Name, Dir: co.Dir})
	}
	var allowed func(string) bool
	initial := map[string]string{}
	if run.Kind == api.RunKindScan {
		spec.Stage = "scan"
		spec.System, spec.Prompt = workflow.ScanPrompt(team, repoInfo)
		allowed = workflow.ValidScanPath
	} else {
		detail, err := s.client.Thread(ctx, run.ThreadID)
		if err != nil {
			fail(fmt.Errorf("load thread: %w", err))
			return
		}
		members, err := s.client.Members(ctx)
		if err != nil {
			fail(fmt.Errorf("load members: %w", err))
			return
		}
		in := buildPromptInput(team, detail, members, run, repoInfo)
		spec.System, spec.Prompt = workflow.StagePrompt(in)
		if initial, err = materialize(runDir, detail, run.Stage); err != nil {
			fail(err)
			return
		}
		stage := run.Stage
		allowed = func(p string) bool { return workflow.ValidArtifactPath(stage, p) }
	}

	// 3. Run the agent.
	out.emit(api.RunOutput{Kind: "status", Text: "Agent working…"})
	res, err := agent.Run(ctx, spec, out.emit)
	finish.Reply, finish.CostUSD = res.Reply, res.CostUSD
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		fail(err)
	}

	// 4. Collect the documents it wrote.
	uploads, warnings := collect(runDir, allowed, initial)
	for _, w := range warnings {
		out.emit(api.RunOutput{Kind: "error", Text: w})
	}
	if run.Stage == api.StagePlan && err == nil {
		uploads, err = c.checkPlan(ctx, agent, spec, uploads, initial, allowed, out)
		if err != nil {
			fail(err)
		}
	}
	finish.Artifacts = uploads
	if err == nil {
		finish.Status = api.RunSucceeded
		if len(uploads) > 0 {
			names := make([]string, len(uploads))
			for i, u := range uploads {
				names[i] = u.Path
			}
			out.emit(api.RunOutput{Kind: "status", Text: "Updated " + strings.Join(names, ", ")})
		}
	}
}

func versionLabel(a agents.Agent, model string) string {
	if model != "" {
		return "(" + model + ")"
	}
	return ""
}

func (c *Core) prepareRepo(ctx context.Context, s *session, lr *localRun, team api.Team, out *pump) workflow.RepoInfo {
	var info workflow.RepoInfo
	if len(team.Repos) == 0 {
		info.Note = "the team hasn't configured a repository"
		out.emit(api.RunOutput{Kind: "status", Text: "No repository configured; using the shared codebase brief"})
		return info
	}
	if len(team.Repos) == 1 {
		out.emit(api.RunOutput{Kind: "status", Text: "Syncing codebase…"})
	} else {
		out.emit(api.RunOutput{Kind: "status", Text: fmt.Sprintf("Syncing %d repositories…", len(team.Repos))})
	}
	// Another run on this machine may be reading the checkouts: don't move
	// them under it, only fetch what's missing.
	busy := c.activeRunsFor(team.ID) > 0
	statuses := c.syncRepos(ctx, team, s.profileSnapshot(), false, busy)
	c.mu.Lock()
	lr.syncedRepo = true
	c.mu.Unlock()
	for i, st := range statuses {
		name := team.Repos[i].Name
		label := "Codebase"
		if len(team.Repos) > 1 {
			label = name
		}
		if !st.Available() {
			msg := firstLine(st.Error)
			info.Missing = append(info.Missing, fmt.Sprintf("%s (%s)", name, msg))
			out.emit(api.RunOutput{Kind: "error", Text: fmt.Sprintf("%s unavailable (%s); using the shared codebase brief", label, msg)})
			continue
		}
		info.Checkouts = append(info.Checkouts, workflow.Checkout{Name: name, URL: st.URL, Dir: st.Dir, Commit: st.Commit})
		ref := st.Branch
		if st.State == "override" {
			ref = "local checkout"
		}
		out.emit(api.RunOutput{Kind: "status", Text: fmt.Sprintf("%s at %s (%s)", label, shortSHA(st.Commit), ref)})
	}
	return info
}

func buildPromptInput(team api.Team, d api.ThreadDetail, members []api.Member, run api.Run, repoInfo workflow.RepoInfo) workflow.PromptInput {
	mm := map[string]api.Member{}
	for _, m := range members {
		mm[m.ID] = m
	}
	in := workflow.PromptInput{
		Team: team, Thread: d.Thread, Stage: run.Stage, Messages: d.Messages, Artifacts: d.Artifacts,
		Runs: d.Runs, Members: mm, Runner: mm[run.MemberID], Repo: repoInfo, Export: team.Output.Type,
	}
	for i := range d.Messages {
		if d.Messages[i].ID == run.TriggerMessageID {
			in.Trigger = &d.Messages[i]
		}
	}
	return in
}

// materialize writes the stage's current documents (editable) and the
// approved upstream documents (reference) into the run directory, returning
// the initial content of the editable ones.
func materialize(dir string, d api.ThreadDetail, stage api.Stage) (map[string]string, error) {
	initial := map[string]string{}
	idx := workflow.Index(stage)
	for _, a := range d.Artifacts {
		var dst string
		switch ai := workflow.Index(a.Stage); {
		case a.Stage == stage:
			dst = filepath.Join(dir, filepath.FromSlash(a.Path))
			initial[a.Path] = a.Content
		case ai < idx:
			st := d.Thread.StageState(a.Stage)
			if st == nil || st.Status != api.StageApproved {
				continue
			}
			dst = filepath.Join(dir, "upstream", string(a.Stage), filepath.FromSlash(a.Path))
		default:
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(dst, []byte(a.Content), 0o644); err != nil {
			return nil, err
		}
	}
	return initial, nil
}

// collect finds stage documents the agent created or changed. Deleted
// mockups become tombstones (empty content).
func collect(dir string, allowed func(string) bool, initial map[string]string) ([]api.ArtifactUpload, []string) {
	var uploads []api.ArtifactUpload
	var warnings []string
	seen := map[string]bool{}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." && (rel == "upstream" || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !allowed(rel) {
			return nil
		}
		seen[rel] = true
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.Size() > workflow.MaxArtifactBytes {
			warnings = append(warnings, fmt.Sprintf("%s is larger than 1 MB and was not saved", rel))
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		if prev, ok := initial[rel]; !ok || prev != string(b) {
			uploads = append(uploads, api.ArtifactUpload{Path: rel, Content: string(b)})
		}
		return nil
	})
	for p := range initial {
		if !seen[p] && workflow.IsMockup(p) {
			uploads = append(uploads, api.ArtifactUpload{Path: p, Content: ""})
		}
	}
	sort.Slice(uploads, func(i, j int) bool { return uploads[i].Path < uploads[j].Path })
	if len(uploads) > workflow.MaxArtifactsPer {
		warnings = append(warnings, fmt.Sprintf("only the first %d documents were saved", workflow.MaxArtifactsPer))
		uploads = uploads[:workflow.MaxArtifactsPer]
	}
	return uploads, warnings
}

// checkPlan validates plan.json and gives the agent one chance to repair it.
func (c *Core) checkPlan(ctx context.Context, agent agents.Agent, spec agents.Spec, uploads []api.ArtifactUpload, initial map[string]string, allowed func(string) bool, out *pump) ([]api.ArtifactUpload, error) {
	content, ok := "", false
	for _, u := range uploads {
		if u.Path == "plan.json" {
			content, ok = u.Content, true
		}
	}
	if !ok {
		content, ok = initial["plan.json"]
	}
	if !ok {
		return uploads, errors.New("the agent didn't write plan.json")
	}
	_, perr := workflow.ParsePlan(content)
	if perr == nil {
		return uploads, nil
	}
	out.emit(api.RunOutput{Kind: "status", Text: "plan.json didn't validate (" + perr.Error() + "); asking the agent to fix it"})
	spec.Prompt = "The file plan.json in your working directory failed validation:\n\n" + perr.Error() +
		"\n\nFix plan.json so it is valid JSON matching the schema in your instructions. Change nothing else. Reply with one sentence."
	if _, err := agent.Run(ctx, spec, out.emit); err != nil {
		return dropPath(uploads, "plan.json"), fmt.Errorf("repairing plan.json: %w", err)
	}
	uploads, _ = collect(spec.Dir, allowed, initial)
	for _, u := range uploads {
		if u.Path == "plan.json" {
			if _, err := workflow.ParsePlan(u.Content); err != nil {
				return dropPath(uploads, "plan.json"), err
			}
			return uploads, nil
		}
	}
	return uploads, errors.New("plan.json is still invalid: " + perr.Error())
}

func dropPath(us []api.ArtifactUpload, p string) []api.ArtifactUpload {
	var out []api.ArtifactUpload
	for _, u := range us {
		if u.Path != p {
			out = append(out, u)
		}
	}
	return out
}

// finish reports the outcome, retrying through brief network blips.
func (c *Core) finish(cl *client.Client, runID string, req api.FinishRunRequest) {
	for attempt := 0; attempt < 6; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		_, err := cl.FinishRun(ctx, runID, req)
		cancel()
		if err == nil {
			return
		}
		var ce *client.Error
		if errors.As(err, &ce) && ce.Status < 500 && ce.Status != 429 {
			if ce.Status == 400 && len(req.Artifacts) > 0 {
				// The server rejected a document; report the run without them.
				req.Status, req.Error, req.Artifacts = api.RunFailed, "server rejected the agent's documents: "+ce.Message, nil
				continue
			}
			c.log.Warn("finish run rejected", "run", runID, "err", err)
			return
		}
		c.log.Warn("finish run failed; retrying", "run", runID, "err", err)
		time.Sleep(time.Duration(1<<attempt) * time.Second)
	}
}

func shortSHA(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// pump batches agent output and streams it to the server; it doubles as the
// run's heartbeat.
type pump struct {
	cl     *client.Client
	runID  string
	log    interface{ Warn(string, ...any) }
	onGone func()

	mu       sync.Mutex
	buf      []api.RunOutput
	lastSent time.Time
	stop     chan struct{}
	stopped  chan struct{}
}

func newPump(cl *client.Client, runID string, log interface{ Warn(string, ...any) }, onGone func()) *pump {
	p := &pump{cl: cl, runID: runID, log: log, onGone: onGone, stop: make(chan struct{}), stopped: make(chan struct{}), lastSent: time.Now()}
	go p.loop()
	return p
}

func (p *pump) emit(o api.RunOutput) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if n := len(p.buf); n > 0 && (o.Kind == "text" || o.Kind == "thinking") && p.buf[n-1].Kind == o.Kind {
		p.buf[n-1].Text += o.Text
		return
	}
	p.buf = append(p.buf, o)
	if len(p.buf) > 5000 {
		p.buf = p.buf[len(p.buf)-5000:]
	}
}

func (p *pump) loop() {
	defer close(p.stopped)
	t := time.NewTicker(300 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-t.C:
			p.mu.Lock()
			due := len(p.buf) > 0 || time.Since(p.lastSent) > 15*time.Second
			p.mu.Unlock()
			if due {
				p.flush()
			}
		}
	}
}

func (p *pump) flush() {
	p.mu.Lock()
	items := p.buf
	p.buf = nil
	p.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err := p.cl.RunOutput(ctx, p.runID, items)
	if err == nil {
		p.mu.Lock()
		p.lastSent = time.Now()
		p.mu.Unlock()
		return
	}
	if client.IsStatus(err, 409) {
		// The server already closed this run (e.g. marked abandoned while we
		// were offline); stop working on it.
		p.onGone()
		return
	}
	p.log.Warn("stream run output", "run", p.runID, "err", err)
	p.mu.Lock()
	p.buf = append(items, p.buf...)
	p.mu.Unlock()
}

func (p *pump) close() {
	close(p.stop)
	<-p.stopped
	p.mu.Lock()
	pending := len(p.buf)
	p.mu.Unlock()
	if pending > 0 {
		p.flush()
	}
}
