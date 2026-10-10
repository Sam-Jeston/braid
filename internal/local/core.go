// Package local is the Braid desktop core. It owns the device identity,
// keeps a realtime connection to the team's braid-server, manages the local
// codebase checkout, and executes agents on this machine, streaming their
// work to the team. The UI talks to it over a token-protected loopback API.
package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sam-jeston/braid/internal/agents"
	"github.com/sam-jeston/braid/internal/api"
	"github.com/sam-jeston/braid/internal/authn"
	"github.com/sam-jeston/braid/internal/client"
	"github.com/sam-jeston/braid/internal/repo"
)

type Options struct {
	Home    string // defaults to $BRAID_HOME or <user config dir>/braid
	Version string
	Logger  *slog.Logger
}

type Core struct {
	opts     Options
	log      *slog.Logger
	home     string
	cfgPath  string
	secrets  Secrets
	bus      *Bus
	repos    *repo.Manager
	hostname string

	mu             sync.Mutex
	cfg            *Config
	sess           *session
	runs           map[string]*localRun
	agentInfo      []agents.Info
	deliveryTiming deliveryTiming
	linearEndpoint string // empty: the real Linear API (tests use a fake)
	wg             sync.WaitGroup
}

func DefaultHome() string {
	if h := os.Getenv("BRAID_HOME"); h != "" {
		return h
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "braid")
}

func New(opts Options) (*Core, error) {
	if opts.Home == "" {
		opts.Home = DefaultHome()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if err := os.MkdirAll(opts.Home, 0o700); err != nil {
		return nil, err
	}
	cfgPath := filepath.Join(opts.Home, "config.json")
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", cfgPath, err)
	}
	host, _ := os.Hostname()
	c := &Core{
		opts: opts, log: opts.Logger, home: opts.Home, cfgPath: cfgPath, cfg: cfg,
		bus: NewBus(), runs: map[string]*localRun{}, hostname: host, deliveryTiming: defaultDeliveryTiming,
	}
	c.secrets = openSecrets(opts.Home, cfg.SecretBackend)
	if cfg.SecretBackend == "" {
		cfg.SecretBackend = c.secrets.Backend()
		_ = c.saveConfig()
	}
	c.repos = repo.NewManager(filepath.Join(opts.Home, "repos"), func(teamID string, st repo.Status) {
		c.bus.PublishLocal("local.repo", st)
	})
	c.refreshAgents(context.Background())
	if cfg.Active != "" {
		if err := c.activate(cfg.Active); err != nil {
			c.log.Warn("activate profile", "err", err)
		}
	}
	return c, nil
}

func (c *Core) Bus() *Bus { return c.bus }

func (c *Core) Home() string { return c.home }

func (c *Core) saveConfig() error { return c.cfg.save(c.cfgPath) }

func (c *Core) refreshAgents(ctx context.Context) []agents.Info {
	info := agents.Detect(ctx)
	c.mu.Lock()
	c.agentInfo = info
	if c.cfg.Agent == "" {
		for _, a := range info {
			if a.Available {
				c.cfg.Agent = a.ID
				_ = c.saveConfig()
				break
			}
		}
	}
	c.mu.Unlock()
	return info
}

// Shutdown cancels local runs (reporting them as cancelled) and disconnects.
func (c *Core) Shutdown() {
	c.mu.Lock()
	for _, r := range c.runs {
		r.cancel()
	}
	s := c.sess
	c.mu.Unlock()
	done := make(chan struct{})
	go func() { c.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
	}
	if s != nil {
		s.stop()
	}
}

// --- state for the UI ---

type State struct {
	Version         string             `json:"version"`
	Hostname        string             `json:"hostname"`
	Home            string             `json:"home"`
	Profiles        []Profile          `json:"profiles"`
	Active          *Profile           `json:"active"`
	Me              *api.Member        `json:"me"`
	Team            *api.Team          `json:"team"`
	Members         []api.Member       `json:"members"`
	Connection      string             `json:"connection"` // connecting | connected | offline | revoked | incompatible
	ConnectionError string             `json:"connection_error,omitempty"`
	Update          *UpdateNeeded      `json:"update,omitempty"` // set while Connection is "incompatible"
	ServerVersion   string             `json:"server_version,omitempty"`
	Agents          []agents.Info      `json:"agents"`
	Agent           string             `json:"agent"`
	AgentModel      string             `json:"agent_model"`
	Repos           []RepoState        `json:"repos"`
	SecretBackend   string             `json:"secret_backend"`
	Credentials     map[string]bool    `json:"credentials"`
	JiraEmail       string             `json:"jira_email"`
	ExportDir       string             `json:"export_dir"`
	LocalRuns       []string           `json:"local_runs"`
	Stages          []stageDescription `json:"stages"`
	Extra           map[string]string  `json:"extra,omitempty"`
}

// RepoState is this machine's checkout of one team repository.
type RepoState struct {
	repo.Status
	Override string `json:"override,omitempty"` // local checkout this member chose instead of a managed clone
}

type stageDescription struct {
	ID      api.Stage `json:"id"`
	Title   string    `json:"title"`
	Owner   api.Role  `json:"owner"`
	Primary string    `json:"primary"`
}

const (
	secretLinear = "linear-api-key"
	secretJira   = "jira-api-token"
)

func (c *Core) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := State{
		Version: c.opts.Version, Hostname: c.hostname, Home: c.home,
		Profiles: append([]Profile{}, c.cfg.Profiles...), Agents: c.agentInfo,
		Agent: c.cfg.Agent, AgentModel: c.cfg.AgentModel, SecretBackend: c.secrets.Backend(),
		JiraEmail: c.cfg.JiraEmail, ExportDir: c.exportDir(), Connection: "offline",
		Credentials: map[string]bool{}, LocalRuns: []string{}, Members: []api.Member{},
		Repos: []RepoState{}, Stages: stageDescriptions(),
	}
	for _, k := range []string{secretLinear, secretJira} {
		_, err := c.secrets.Get(k)
		st.Credentials[k] = err == nil
	}
	for id := range c.runs {
		st.LocalRuns = append(st.LocalRuns, id)
	}
	sort.Strings(st.LocalRuns)
	if s := c.sess; s != nil {
		p := s.profile
		st.Active = &p
		s.mu.Lock()
		st.Me, st.Team = s.me, s.team
		st.Members = append(st.Members, s.members...)
		st.Connection, st.ConnectionError, st.Update, st.ServerVersion = s.status, s.statusErr, s.update, s.serverVersion
		var repos []api.RepoRef
		if s.team != nil {
			repos = s.team.Repos
		}
		s.mu.Unlock()
		st.Repos = c.repoStates(p, repos)
	}
	return st
}

func (c *Core) exportDir() string {
	if c.cfg.ExportDir != "" {
		return c.cfg.ExportDir
	}
	home, _ := os.UserHomeDir()
	if fi, err := os.Stat(filepath.Join(home, "Documents")); err == nil && fi.IsDir() {
		return filepath.Join(home, "Documents", "Braid")
	}
	return filepath.Join(home, "braid-exports")
}

// --- onboarding ---

type ProbeResult struct {
	URL  string         `json:"url"`
	Info api.ServerInfo `json:"info"`
}

func (c *Core) Probe(ctx context.Context, rawURL string, allowInsecure bool) (ProbeResult, error) {
	u, err := client.NormalizeServerURL(rawURL, allowInsecure)
	if err != nil {
		return ProbeResult{}, err
	}
	info, err := client.Info(ctx, u)
	if err == nil {
		err = client.CheckCompatible(info)
	}
	return ProbeResult{URL: u, Info: info}, err
}

type JoinInput struct {
	ServerURL     string `json:"server_url"`
	AllowInsecure bool   `json:"allow_insecure"`
	Code          string `json:"code"`
	DisplayName   string `json:"display_name"`
	Email         string `json:"email"`
}

type CreateTeamInput struct {
	JoinInput
	Roles []api.Role       `json:"roles"`
	Team  api.TeamSettings `json:"team"`
}

func (c *Core) Join(ctx context.Context, in JoinInput) (Profile, error) {
	pr, err := c.Probe(ctx, in.ServerURL, in.AllowInsecure)
	if err != nil {
		return Profile{}, err
	}
	_, key, err := authn.GenerateKey()
	if err != nil {
		return Profile{}, err
	}
	resp, err := client.RedeemInvite(ctx, pr.URL, api.RedeemInviteRequest{
		Code:   in.Code,
		Member: api.NewMember{DisplayName: in.DisplayName, Email: in.Email},
		Device: client.Registration(key, pr.Info.ServerID, in.Code, c.deviceName()),
	})
	if err != nil {
		return Profile{}, err
	}
	return c.addProfile(pr, resp, authn.EncodePrivateKey(key))
}

func (c *Core) CreateTeam(ctx context.Context, in CreateTeamInput) (Profile, error) {
	pr, err := c.Probe(ctx, in.ServerURL, in.AllowInsecure)
	if err != nil {
		return Profile{}, err
	}
	_, key, err := authn.GenerateKey()
	if err != nil {
		return Profile{}, err
	}
	for i := range in.Team.Repos {
		in.Team.Repos[i].URL = repo.NormalizeURL(in.Team.Repos[i].URL)
	}
	resp, err := client.CreateTeam(ctx, pr.URL, api.CreateTeamRequest{
		SetupCode: in.Code,
		Team:      in.Team,
		Member:    api.NewMember{DisplayName: in.DisplayName, Email: in.Email, Roles: in.Roles},
		Device:    client.Registration(key, pr.Info.ServerID, in.Code, c.deviceName()),
	})
	if err != nil {
		return Profile{}, err
	}
	return c.addProfile(pr, resp, authn.EncodePrivateKey(key))
}

func (c *Core) deviceName() string {
	if c.hostname != "" {
		return c.hostname
	}
	return "Braid desktop"
}

func (c *Core) addProfile(pr ProbeResult, resp api.JoinResponse, key string) (Profile, error) {
	p := Profile{
		ID: authn.NewID("prof"), ServerURL: pr.URL, ServerID: pr.Info.ServerID, TeamID: resp.Team.ID, TeamName: resp.Team.Name,
		MemberID: resp.Member.ID, DeviceID: resp.DeviceID, DisplayName: resp.Member.DisplayName,
	}
	if err := c.secrets.Set(keyName(p.ID), key); err != nil {
		return p, fmt.Errorf("store device key: %w", err)
	}
	c.mu.Lock()
	c.cfg.Profiles = append(c.cfg.Profiles, p)
	err := c.saveConfig()
	c.mu.Unlock()
	if err != nil {
		return p, err
	}
	return p, c.Activate(p.ID)
}

func keyName(profileID string) string { return "device-key:" + profileID }

func (c *Core) Activate(profileID string) error {
	if err := c.activate(profileID); err != nil {
		return err
	}
	c.mu.Lock()
	c.cfg.Active = profileID
	err := c.saveConfig()
	c.mu.Unlock()
	c.bus.PublishLocal("local.state", nil)
	return err
}

func (c *Core) activate(profileID string) error {
	c.mu.Lock()
	p := c.cfg.profile(profileID)
	if p == nil {
		c.mu.Unlock()
		return errors.New("unknown profile")
	}
	prof := *p
	old := c.sess
	c.sess = nil
	c.mu.Unlock()
	if old != nil {
		old.stop()
	}
	keyStr, err := c.secrets.Get(keyName(prof.ID))
	if err != nil {
		return fmt.Errorf("device key for %s is missing (%v); re-join the team", prof.TeamName, err)
	}
	key, err := authn.DecodePrivateKey(keyStr)
	if err != nil {
		return err
	}
	s := newSession(c, prof, client.New(prof.ServerURL, prof.ServerID, prof.DeviceID, key))
	c.mu.Lock()
	c.sess = s
	c.mu.Unlock()
	s.start()
	return nil
}

// Leave removes this device from the team on the server (best effort) and
// forgets the profile locally.
func (c *Core) Leave(ctx context.Context, profileID string) error {
	c.mu.Lock()
	p := c.cfg.profile(profileID)
	if p == nil {
		c.mu.Unlock()
		return errors.New("unknown profile")
	}
	prof := *p
	s := c.sess
	c.mu.Unlock()
	if s != nil && s.profile.ID == profileID {
		_ = s.client.Do(ctx, "DELETE", "/v1/devices/"+prof.DeviceID, nil, nil)
		s.stop()
		c.mu.Lock()
		c.sess = nil
		c.mu.Unlock()
	}
	_ = c.secrets.Delete(keyName(profileID))
	c.mu.Lock()
	var keep []Profile
	for _, x := range c.cfg.Profiles {
		if x.ID != profileID {
			keep = append(keep, x)
		}
	}
	c.cfg.Profiles = keep
	if c.cfg.Active == profileID {
		c.cfg.Active = ""
		if len(keep) > 0 {
			c.cfg.Active = keep[0].ID
		}
	}
	next := c.cfg.Active
	err := c.saveConfig()
	c.mu.Unlock()
	if next != "" {
		_ = c.activate(next)
	}
	c.bus.PublishLocal("local.state", nil)
	return err
}

// --- settings ---

type SettingsPatch struct {
	Agent      *string `json:"agent,omitempty"`
	AgentModel *string `json:"agent_model,omitempty"`
	// RepoOverrides sets (or, with an empty path, clears) local checkouts by repository name.
	RepoOverrides map[string]string `json:"repo_overrides,omitempty"`
	JiraEmail     *string           `json:"jira_email,omitempty"`
	ExportDir     *string           `json:"export_dir,omitempty"`
}

func (c *Core) UpdateSettings(p SettingsPatch) error {
	c.mu.Lock()
	if p.Agent != nil {
		if _, ok := agents.Get(*p.Agent); !ok {
			c.mu.Unlock()
			return fmt.Errorf("unknown agent %q", *p.Agent)
		}
		c.cfg.Agent = *p.Agent
	}
	if p.AgentModel != nil {
		c.cfg.AgentModel = strings.TrimSpace(*p.AgentModel)
	}
	if p.JiraEmail != nil {
		c.cfg.JiraEmail = strings.TrimSpace(*p.JiraEmail)
	}
	if p.ExportDir != nil {
		c.cfg.ExportDir = strings.TrimSpace(*p.ExportDir)
	}
	if p.RepoOverrides != nil && c.sess != nil {
		if prof := c.cfg.profile(c.sess.profile.ID); prof != nil {
			// Profiles are copied by value elsewhere, so build a new map rather than editing the shared one.
			next := map[string]string{}
			for name, dir := range prof.RepoOverrides {
				next[name] = dir
			}
			if prof.RepoOverride != "" {
				// Carry the old single-repository setting over to the first repository.
				c.sess.mu.Lock()
				if t := c.sess.team; t != nil && len(t.Repos) > 0 && next[t.Repos[0].Name] == "" {
					next[t.Repos[0].Name] = prof.RepoOverride
				}
				c.sess.mu.Unlock()
			}
			for name, dir := range p.RepoOverrides {
				if dir = strings.TrimSpace(dir); dir == "" {
					delete(next, name)
					continue
				}
				if abs, err := filepath.Abs(dir); err == nil {
					dir = abs
				}
				next[name] = dir
			}
			prof.RepoOverrides, prof.RepoOverride = next, ""
			c.sess.profile.RepoOverrides, c.sess.profile.RepoOverride = next, ""
		}
	}
	err := c.saveConfig()
	c.mu.Unlock()
	c.bus.PublishLocal("local.state", nil)
	return err
}

func (c *Core) SetCredential(key, value string) error {
	switch key {
	case secretLinear, secretJira:
	default:
		return fmt.Errorf("unknown credential %q", key)
	}
	var err error
	if strings.TrimSpace(value) == "" {
		err = c.secrets.Delete(key)
	} else {
		err = c.secrets.Set(key, strings.TrimSpace(value))
	}
	c.bus.PublishLocal("local.state", nil)
	return err
}

func (c *Core) RefreshAgents(ctx context.Context) []agents.Info {
	info := c.refreshAgents(ctx)
	c.bus.PublishLocal("local.state", nil)
	return info
}

func (c *Core) session() (*session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sess == nil {
		return nil, errors.New("not connected to a team")
	}
	return c.sess, nil
}

// SyncRepos refreshes this machine's checkouts of the active team's repositories.
func (c *Core) SyncRepos(ctx context.Context, force bool) ([]RepoState, error) {
	s, err := c.session()
	if err != nil {
		return nil, err
	}
	team, err := s.freshTeam(ctx)
	if err != nil {
		return nil, err
	}
	prof := s.profileSnapshot()
	if !force && c.activeRunsFor(team.ID) > 0 {
		// Never move a checkout under a running agent.
		return c.repoStates(prof, team.Repos), nil
	}
	var errs []error
	for i, st := range c.syncRepos(ctx, team, prof, force, false) {
		if !st.Available() {
			errs = append(errs, fmt.Errorf("%s: %s", team.Repos[i].Name, st.Error))
		}
	}
	return c.repoStates(prof, team.Repos), errors.Join(errs...)
}

// syncRepos syncs the team's repositories concurrently and returns their
// statuses in team order. With onlyMissing, checkouts that are already
// usable are left alone.
func (c *Core) syncRepos(ctx context.Context, team api.Team, prof Profile, force, onlyMissing bool) []repo.Status {
	out := make([]repo.Status, len(team.Repos))
	var wg sync.WaitGroup
	for i, r := range team.Repos {
		if out[i] = c.repos.Status(team.ID, r.Name); onlyMissing && out[i].Available() {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			st, err := c.repos.Sync(ctx, team.ID, r, prof.overrideFor(team.Repos, i), force)
			if err != nil && st.Error == "" {
				st.State, st.Error = "error", err.Error()
			}
			out[i] = st
		}()
	}
	wg.Wait()
	return out
}

func (c *Core) repoStates(p Profile, repos []api.RepoRef) []RepoState {
	out := []RepoState{}
	for i, r := range repos {
		st := c.repos.Status(p.TeamID, r.Name)
		if st.URL == "" {
			st.URL, st.Branch = r.URL, r.Branch
		}
		out = append(out, RepoState{Status: st, Override: p.overrideFor(repos, i)})
	}
	return out
}

func (c *Core) activeRunsFor(teamID string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, r := range c.runs {
		if r.run.TeamID == teamID && r.syncedRepo {
			n++
		}
	}
	return n
}

// Proxy forwards a UI request to the team server with this device's auth.
func (c *Core) Proxy(ctx context.Context, method, path string, body []byte) (int, []byte, error) {
	s, err := c.session()
	if err != nil {
		return 0, nil, err
	}
	resp, err := s.client.Raw(ctx, method, path, body, "application/json")
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := readAll(resp.Body, 64<<20)
	return resp.StatusCode, b, err
}

func mustMarshal(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
