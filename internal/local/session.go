package local

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/sam-jeston/braid/internal/api"
	"github.com/sam-jeston/braid/internal/client"
)

// session is the live connection to one team: it keeps team/member caches
// fresh and relays the server's event stream to the UI, reconnecting (and
// replaying missed events) whenever the network drops.
type session struct {
	c       *Core
	profile Profile // guarded by c.mu
	client  *client.Client
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}

	mu        sync.Mutex
	me        *api.Member
	team      *api.Team
	members   []api.Member
	status    string
	statusErr string
	lastSeq   int64
	conn      *websocket.Conn
	presence  string
}

func newSession(c *Core, p Profile, cl *client.Client) *session {
	ctx, cancel := context.WithCancel(context.Background())
	return &session{c: c, profile: p, client: cl, ctx: ctx, cancel: cancel, done: make(chan struct{}), status: "connecting", lastSeq: -1}
}

func (s *session) start() { go s.loop() }

func (s *session) stop() {
	s.cancel()
	select {
	case <-s.done:
	case <-time.After(3 * time.Second):
	}
}

func (s *session) profileSnapshot() Profile {
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	return s.profile
}

func (s *session) setStatus(status, errMsg string) {
	s.mu.Lock()
	changed := s.status != status || s.statusErr != errMsg
	s.status, s.statusErr = status, errMsg
	s.mu.Unlock()
	if changed {
		s.c.bus.PublishLocal("local.connection", map[string]string{"status": status, "error": errMsg})
	}
}

func isRevoked(err error) bool {
	var e *client.Error
	return errors.As(err, &e) && e.Status == 401 && (e.Code == "device_revoked" || e.Code == "unknown_device")
}

func (s *session) sleep(d time.Duration) bool {
	select {
	case <-s.ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func (s *session) loop() {
	defer close(s.done)
	backoff := time.Second
	for s.ctx.Err() == nil {
		s.setStatus("connecting", "")
		if err := s.refresh(s.ctx); err != nil {
			if s.ctx.Err() != nil {
				return
			}
			if isRevoked(err) {
				s.setStatus("revoked", "This device's access to the team was revoked. Re-join with a new invite.")
				if !s.sleep(time.Minute) {
					return
				}
				continue
			}
			s.setStatus("offline", err.Error())
			if !s.sleep(backoff) {
				return
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		s.mu.Lock()
		since := s.lastSeq
		s.mu.Unlock()
		conn, err := s.client.Dial(s.ctx, since)
		if err != nil {
			if s.ctx.Err() != nil {
				return
			}
			s.setStatus("offline", err.Error())
			if !s.sleep(backoff) {
				return
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		s.mu.Lock()
		s.conn = conn
		presence := s.presence
		s.mu.Unlock()
		s.setStatus("connected", "")
		backoff = time.Second
		if presence != "" {
			s.sendPresence(presence)
		}
		err = s.read(conn)
		s.mu.Lock()
		s.conn = nil
		s.mu.Unlock()
		conn.CloseNow()
		if s.ctx.Err() != nil {
			return
		}
		msg := "connection lost"
		if err != nil {
			msg = err.Error()
		}
		s.setStatus("offline", msg)
		if !s.sleep(backoff) {
			return
		}
	}
}

func (s *session) refresh(ctx context.Context) error {
	me, err := s.client.Me(ctx)
	if err != nil {
		return err
	}
	members, err := s.client.Members(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.me, s.team, s.members = &me.Member, &me.Team, members
	s.mu.Unlock()
	// Keep the cached team name in the profile list current.
	s.c.mu.Lock()
	if p := s.c.cfg.profile(s.profile.ID); p != nil && p.TeamName != me.Team.Name {
		p.TeamName = me.Team.Name
		s.profile.TeamName = me.Team.Name
		_ = s.c.saveConfig()
	}
	s.c.mu.Unlock()
	s.c.bus.PublishLocal("local.state", nil)
	return nil
}

func (s *session) freshTeam(ctx context.Context) (api.Team, error) {
	var t api.Team
	if err := s.client.Do(ctx, "GET", "/v1/team", nil, &t); err != nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.team != nil {
			return *s.team, nil
		}
		return t, err
	}
	s.mu.Lock()
	s.team = &t
	s.mu.Unlock()
	return t, nil
}

func (s *session) read(conn *websocket.Conn) error {
	for {
		_, data, err := conn.Read(s.ctx)
		if err != nil {
			return err
		}
		var ev api.Event
		if json.Unmarshal(data, &ev) != nil {
			continue
		}
		switch ev.Type {
		case api.EvHello:
			s.mu.Lock()
			if ev.Seq > s.lastSeq {
				s.lastSeq = ev.Seq
			}
			s.mu.Unlock()
			continue
		case api.EvResync:
			s.mu.Lock()
			s.lastSeq = ev.Seq
			s.mu.Unlock()
			s.c.bus.PublishLocal("local.resync", nil)
			continue
		}
		if ev.Seq > 0 {
			s.mu.Lock()
			dup := ev.Seq <= s.lastSeq
			if !dup {
				s.lastSeq = ev.Seq
			}
			s.mu.Unlock()
			if dup {
				continue
			}
		}
		s.apply(ev)
		s.c.bus.PublishRaw(data)
	}
}

// apply keeps caches in sync and reacts to events aimed at this machine.
func (s *session) apply(ev api.Event) {
	switch ev.Type {
	case api.EvTeamUpdated:
		var t api.Team
		if json.Unmarshal(ev.Data, &t) == nil {
			s.mu.Lock()
			s.team = &t
			s.mu.Unlock()
		}
	case api.EvMemberJoined, api.EvMemberUpdated, api.EvMemberRemoved:
		var m api.Member
		if json.Unmarshal(ev.Data, &m) != nil {
			return
		}
		s.mu.Lock()
		found := false
		for i := range s.members {
			if s.members[i].ID == m.ID {
				s.members[i], found = m, true
			}
		}
		if !found {
			s.members = append(s.members, m)
		}
		if s.me != nil && s.me.ID == m.ID {
			s.me = &m
		}
		s.mu.Unlock()
	case api.EvRunCancel:
		var cr api.CancelRequested
		if json.Unmarshal(ev.Data, &cr) == nil {
			s.c.cancelLocalRun(cr.RunID)
		}
	}
}

func (s *session) setPresence(threadID string) {
	s.mu.Lock()
	s.presence = threadID
	s.mu.Unlock()
	s.sendPresence(threadID)
}

func (s *session) sendPresence(threadID string) {
	s.mu.Lock()
	conn := s.conn
	s.mu.Unlock()
	if conn == nil {
		return
	}
	b, _ := json.Marshal(api.ClientMessage{Type: "presence", ThreadID: threadID})
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	_ = conn.Write(ctx, websocket.MessageText, b)
}

func (c *Core) SetPresence(threadID string) {
	if s, err := c.session(); err == nil {
		s.setPresence(threadID)
	}
}

func (s *session) memberMap() map[string]api.Member {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := make(map[string]api.Member, len(s.members))
	for _, x := range s.members {
		m[x.ID] = x
	}
	return m
}
