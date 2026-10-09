package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/sam-jeston/braid/internal/api"
)

const maxReplay = 5000

type outMsg struct {
	seq  int64
	data []byte
}

type subscriber struct {
	teamID   string
	memberID string
	threadID string // presence: thread currently being viewed
	send     chan outMsg
	done     chan struct{}
	once     sync.Once
}

func (s *subscriber) close() { s.once.Do(func() { close(s.done) }) }

// Hub fans out committed events to every websocket connected for a team.
type Hub struct {
	s    *Server
	mu   sync.Mutex
	subs map[string]map[*subscriber]struct{}
}

func newHub(s *Server) *Hub { return &Hub{s: s, subs: map[string]map[*subscriber]struct{}{}} }

func (h *Hub) add(sub *subscriber) {
	h.mu.Lock()
	if h.subs[sub.teamID] == nil {
		h.subs[sub.teamID] = map[*subscriber]struct{}{}
	}
	h.subs[sub.teamID][sub] = struct{}{}
	h.mu.Unlock()
	h.broadcastPresence(sub.teamID)
}

func (h *Hub) remove(sub *subscriber) {
	h.mu.Lock()
	delete(h.subs[sub.teamID], sub)
	h.mu.Unlock()
	sub.close()
	h.broadcastPresence(sub.teamID)
}

func (h *Hub) publish(teamID string, ev api.Event) {
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for sub := range h.subs[teamID] {
		select {
		case sub.send <- outMsg{seq: ev.Seq, data: b}:
		default:
			// Slow consumer: drop the connection; the client reconnects and
			// replays from its last seq.
			sub.close()
		}
	}
}

func (h *Hub) disconnectMember(memberID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, subs := range h.subs {
		for sub := range subs {
			if sub.memberID == memberID {
				sub.close()
			}
		}
	}
}

func (h *Hub) setPresence(sub *subscriber, threadID string) {
	h.mu.Lock()
	changed := sub.threadID != threadID
	sub.threadID = threadID
	h.mu.Unlock()
	if changed {
		h.broadcastPresence(sub.teamID)
	}
}

func (h *Hub) presenceEvent(teamID string) []byte {
	seen := map[api.PresenceEntry]bool{}
	entries := []api.PresenceEntry{}
	for sub := range h.subs[teamID] {
		e := api.PresenceEntry{MemberID: sub.memberID, ThreadID: sub.threadID}
		if !seen[e] {
			seen[e] = true
			entries = append(entries, e)
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].MemberID != entries[j].MemberID {
			return entries[i].MemberID < entries[j].MemberID
		}
		return entries[i].ThreadID < entries[j].ThreadID
	})
	data, _ := json.Marshal(entries)
	b, _ := json.Marshal(api.Event{Type: api.EvPresence, Data: data, CreatedAt: time.Now().UTC()})
	return b
}

func (h *Hub) broadcastPresence(teamID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	b := h.presenceEvent(teamID)
	for sub := range h.subs[teamID] {
		select {
		case sub.send <- outMsg{data: b}:
		default:
		}
	}
}

// handleWS streams team events. With ?since=N the server first replays every
// event after N, so a reconnecting client misses nothing.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return nil // Accept has already written the response
	}
	defer c.CloseNow()
	c.SetReadLimit(64 << 10)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	sub := &subscriber{teamID: a.teamID(), memberID: a.member.ID, send: make(chan outMsg, 1024), done: make(chan struct{})}
	s.hub.add(sub)
	defer s.hub.remove(sub)

	go func() {
		defer cancel()
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			var msg api.ClientMessage
			if json.Unmarshal(data, &msg) == nil && msg.Type == "presence" {
				s.hub.setPresence(sub, msg.ThreadID)
			}
		}
	}()

	write := func(b []byte) error {
		wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return c.Write(wctx, websocket.MessageText, b)
	}

	var head int64
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM events WHERE team_id = ?`, a.teamID()).Scan(&head); err != nil {
		return nil
	}
	if sinceStr := r.URL.Query().Get("since"); sinceStr != "" {
		since, _ := strconv.ParseInt(sinceStr, 10, 64)
		if head-since > maxReplay || since < 0 {
			b, _ := json.Marshal(api.Event{Type: api.EvResync, Seq: head, CreatedAt: s.now()})
			if write(b) != nil {
				return nil
			}
		} else if since < head {
			if err := s.replay(ctx, a.teamID(), since, head, write); err != nil {
				return nil
			}
		}
	}
	hello, _ := json.Marshal(api.Event{Type: api.EvHello, Seq: head, CreatedAt: s.now()})
	if write(hello) != nil {
		return nil
	}

	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-sub.done:
			c.Close(websocket.StatusPolicyViolation, "connection reset by server")
			return nil
		case m := <-sub.send:
			if m.seq != 0 && m.seq <= head {
				continue // already replayed
			}
			if write(m.data) != nil {
				return nil
			}
		case <-ping.C:
			pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.Ping(pctx)
			cancel()
			if err != nil {
				return nil
			}
		}
	}
}

func (s *Server) replay(ctx context.Context, teamID string, since, head int64, write func([]byte) error) error {
	rows, err := s.db.QueryContext(ctx, `SELECT seq, thread_id, run_id, type, actor_id, data, created_at FROM events
		WHERE team_id = ? AND seq > ? AND seq <= ? ORDER BY seq`, teamID, since, head)
	if err != nil {
		return err
	}
	var batch [][]byte
	for rows.Next() {
		var ev api.Event
		var thread, run, actor sql.NullString
		var data string
		var created int64
		if err := rows.Scan(&ev.Seq, &thread, &run, &ev.Type, &actor, &data, &created); err != nil {
			rows.Close()
			return err
		}
		ev.ThreadID, ev.RunID, ev.ActorID = thread.String, run.String, actor.String
		ev.Data = json.RawMessage(data)
		ev.CreatedAt = fromMs(created)
		b, _ := json.Marshal(ev)
		batch = append(batch, b)
	}
	rows.Close()
	// Rows are fully read before writing so the single DB connection is free.
	for _, b := range batch {
		if err := write(b); err != nil {
			return err
		}
	}
	return nil
}
