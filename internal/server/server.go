// Package server implements braid-server: the self-hosted, team-shared state
// store. It never executes agents; it stores threads, documents and run
// transcripts and fans out realtime events to every connected teammate.
package server

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sam-jeston/braid/internal/api"
	"github.com/sam-jeston/braid/internal/authn"
)

type Config struct {
	DBPath  string
	Version string
	Logger  *slog.Logger
	// RunTimeout marks running agent runs as abandoned when their machine
	// stops sending heartbeats.
	RunTimeout time.Duration
	// TokenTTL is the lifetime of access tokens.
	TokenTTL time.Duration
	// TrustProxy uses X-Forwarded-For for rate limiting (behind a reverse proxy).
	TrustProxy bool
	// MinClientProtocol overrides api.MinClientProtocol (tests).
	MinClientProtocol int
	Now               func() time.Time
}

type Server struct {
	cfg        Config
	db         *sql.DB
	hub        *Hub
	serverID   string
	log        *slog.Logger
	challenges *challengeStore
	limiter    *rateLimiter
	mux        *http.ServeMux
}

func New(cfg Config) (*Server, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.RunTimeout == 0 {
		cfg.RunTimeout = 2 * time.Minute
	}
	if cfg.TokenTTL == 0 {
		cfg.TokenTTL = time.Hour
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	if cfg.Version == "" {
		cfg.Version = "dev"
	}
	if cfg.MinClientProtocol == 0 {
		cfg.MinClientProtocol = api.MinClientProtocol
	}
	db, err := openDB(cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	s := &Server{
		cfg:        cfg,
		db:         db,
		log:        cfg.Logger,
		challenges: newChallengeStore(),
		limiter:    newRateLimiter(30, time.Minute),
	}
	s.hub = newHub(s)
	if s.serverID, err = s.loadServerID(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	s.routes()
	return s, nil
}

func (s *Server) now() time.Time { return s.cfg.Now() }

func (s *Server) ServerID() string { return s.serverID }

func (s *Server) Close() error { return s.db.Close() }

func (s *Server) Handler() http.Handler { return s.logRequests(s.checkProtocol(s.mux)) }

// Run performs background maintenance until ctx is cancelled.
func (s *Server) Run(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.reapRuns(ctx); err != nil {
				s.log.Error("reap runs", "err", err)
			}
			s.challenges.gc()
			s.limiter.gc()
			_, _ = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, ms(s.now()))
		}
	}
}

func (s *Server) routes() {
	m := http.NewServeMux()
	// Public
	m.HandleFunc("GET /v1/info", s.h(s.handleInfo))
	m.HandleFunc("POST /v1/setup/teams", s.limited(s.h(s.handleCreateTeam)))
	m.HandleFunc("POST /v1/invites/redeem", s.limited(s.h(s.handleRedeemInvite)))
	m.HandleFunc("POST /v1/auth/challenge", s.limited(s.h(s.handleChallenge)))
	m.HandleFunc("POST /v1/auth/token", s.limited(s.h(s.handleToken)))
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })

	// Team & membership
	m.HandleFunc("GET /v1/me", s.authed(s.handleMe))
	m.HandleFunc("GET /v1/team", s.authed(s.handleGetTeam))
	m.HandleFunc("PATCH /v1/team", s.authed(s.handlePatchTeam))
	m.HandleFunc("GET /v1/members", s.authed(s.handleListMembers))
	m.HandleFunc("PATCH /v1/members/{id}", s.authed(s.handlePatchMember))
	m.HandleFunc("DELETE /v1/members/{id}", s.authed(s.handleRemoveMember))
	m.HandleFunc("GET /v1/invites", s.authed(s.handleListInvites))
	m.HandleFunc("POST /v1/invites", s.authed(s.handleCreateInvite))
	m.HandleFunc("DELETE /v1/invites/{id}", s.authed(s.handleRevokeInvite))
	m.HandleFunc("GET /v1/devices", s.authed(s.handleListDevices))
	m.HandleFunc("DELETE /v1/devices/{id}", s.authed(s.handleRevokeDevice))

	// Threads
	m.HandleFunc("GET /v1/threads", s.authed(s.handleListThreads))
	m.HandleFunc("POST /v1/threads", s.authed(s.handleCreateThread))
	m.HandleFunc("GET /v1/threads/{id}", s.authed(s.handleGetThread))
	m.HandleFunc("PATCH /v1/threads/{id}", s.authed(s.handlePatchThread))
	m.HandleFunc("POST /v1/threads/{id}/messages", s.authed(s.handleCreateMessage))
	m.HandleFunc("POST /v1/threads/{id}/stages/{stage}/approve", s.authed(s.handleApprove))
	m.HandleFunc("POST /v1/threads/{id}/stages/{stage}/reopen", s.authed(s.handleReopen))
	m.HandleFunc("POST /v1/threads/{id}/stages/{stage}/selection", s.authed(s.handleSelection))
	m.HandleFunc("POST /v1/threads/{id}/artifacts", s.authed(s.handleSaveArtifact))
	m.HandleFunc("GET /v1/threads/{id}/artifacts/history", s.authed(s.handleArtifactHistory))
	m.HandleFunc("POST /v1/threads/{id}/exports", s.authed(s.handleCreateExport))

	// Runs
	m.HandleFunc("GET /v1/runs", s.authed(s.handleListRuns))
	m.HandleFunc("POST /v1/runs", s.authed(s.handleCreateRun))
	m.HandleFunc("GET /v1/runs/{id}", s.authed(s.handleGetRun))
	m.HandleFunc("GET /v1/runs/{id}/output", s.authed(s.handleGetRunOutput))
	m.HandleFunc("POST /v1/runs/{id}/output", s.authed(s.handleRunOutput))
	m.HandleFunc("POST /v1/runs/{id}/finish", s.authed(s.handleFinishRun))
	m.HandleFunc("POST /v1/runs/{id}/cancel", s.authed(s.handleCancelRun))

	// Realtime
	m.HandleFunc("GET /v1/ws", s.authed(s.handleWS))
	s.mux = m
}

// --- request plumbing ---

type httpError struct {
	status int
	code   string
	msg    string
}

func (e *httpError) Error() string { return e.msg }

func errf(status int, code, format string, args ...any) error {
	return &httpError{status: status, code: code, msg: fmt.Sprintf(format, args...)}
}

var (
	errNotFound  = errf(http.StatusNotFound, "not_found", "not found")
	errForbidden = errf(http.StatusForbidden, "forbidden", "you don't have permission to do that")
)

func (s *Server) h(fn func(w http.ResponseWriter, r *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := fn(w, r); err != nil {
			s.writeErr(w, r, err)
		}
	}
}

func (s *Server) writeErr(w http.ResponseWriter, r *http.Request, err error) {
	var he *httpError
	switch {
	case errors.As(err, &he):
	case isNoRows(err):
		he = errNotFound.(*httpError)
	default:
		s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		he = &httpError{status: 500, code: "internal", msg: "internal server error"}
	}
	var body api.ErrorBody
	body.Error.Code, body.Error.Message = he.code, he.msg
	writeJSON(w, he.status, body)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func decode(w http.ResponseWriter, r *http.Request, v any, limit int64) error {
	if limit == 0 {
		limit = 1 << 20
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return errf(400, "bad_request", "request body required")
		}
		return errf(400, "bad_request", "invalid request body: %v", err)
	}
	return nil
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// Hijack is required for the websocket upgrade.
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("hijacking not supported")
	}
	r.status = http.StatusSwitchingProtocols
	return h.Hijack()
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		if r.URL.Path == "/v1/ws" || strings.HasSuffix(r.URL.Path, "/output") {
			return
		}
		s.log.Debug("http", "method", r.Method, "path", r.URL.Path, "status", rec.status, "dur", time.Since(start).Round(time.Millisecond))
	})
}

// checkProtocol turns away apps older than MinClientProtocol with a message
// telling the member to update. /v1/info stays open so any app can find out
// which protocol the server speaks.
func (s *Server) checkProtocol(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/info" || r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		// A missing or malformed header is an app from before versioning: protocol 0.
		proto, _ := strconv.Atoi(r.Header.Get(api.ProtocolHeader))
		if proto < s.cfg.MinClientProtocol {
			msg := "This version of Braid is too old for your team's server. Update Braid to reconnect."
			if s.cfg.Version != "dev" {
				msg = fmt.Sprintf("This version of Braid is too old for your team's server, which runs %s. Update Braid to reconnect.", s.cfg.Version)
			}
			s.writeErr(w, r, errf(http.StatusUpgradeRequired, api.CodeClientOutdated, "%s", msg))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// --- auth ---

type authCtx struct {
	member   api.Member
	deviceID string
}

func (a *authCtx) teamID() string { return a.member.TeamID }

func (s *Server) authed(fn func(w http.ResponseWriter, r *http.Request, a *authCtx) error) http.HandlerFunc {
	return s.h(func(w http.ResponseWriter, r *http.Request) error {
		tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || tok == "" {
			return errf(401, "unauthorized", "missing access token")
		}
		var deviceID, memberID string
		var expires int64
		err := s.db.QueryRowContext(r.Context(), `SELECT s.device_id, d.member_id, s.expires_at FROM sessions s
			JOIN devices d ON d.id = s.device_id
			JOIN members m ON m.id = d.member_id
			WHERE s.token_hash = ? AND d.revoked_at IS NULL AND m.removed_at IS NULL`, authn.HashSecret(tok)).Scan(&deviceID, &memberID, &expires)
		if err != nil || expires < ms(s.now()) {
			return errf(401, "unauthorized", "invalid or expired access token")
		}
		m, err := getMember(r.Context(), s.db, memberID)
		if err != nil {
			return err
		}
		return fn(w, r, &authCtx{member: m, deviceID: deviceID})
	})
}

func requireAdmin(a *authCtx) error {
	if !a.member.IsAdmin {
		return errf(403, "forbidden", "only team admins can do that")
	}
	return nil
}

// --- rate limiting for unauthenticated endpoints ---

type rateLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	hits   map[string][]time.Time
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{limit: limit, window: window, hits: map[string][]time.Time{}}
}

func (l *rateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	h := l.hits[key]
	i := 0
	for i < len(h) && now.Sub(h[i]) > l.window {
		i++
	}
	h = h[i:]
	if len(h) >= l.limit {
		l.hits[key] = h
		return false
	}
	l.hits[key] = append(h, now)
	return true
}

func (l *rateLimiter) gc() {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	for k, h := range l.hits {
		if len(h) == 0 || now.Sub(h[len(h)-1]) > l.window {
			delete(l.hits, k)
		}
	}
}

func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			return strings.TrimSpace(strings.Split(xff, ",")[0])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) limited(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.limiter.allow(s.clientIP(r)) {
			s.writeErr(w, r, errf(429, "rate_limited", "too many attempts; try again in a minute"))
			return
		}
		next(w, r)
	}
}

// --- challenge nonces ---

type challenge struct {
	deviceID string
	expires  time.Time
}

type challengeStore struct {
	mu sync.Mutex
	m  map[string]challenge
}

func newChallengeStore() *challengeStore { return &challengeStore{m: map[string]challenge{}} }

func (c *challengeStore) issue(deviceID string) (string, time.Time) {
	nonce := authn.RandomToken("", 32)
	exp := time.Now().Add(2 * time.Minute)
	c.mu.Lock()
	c.m[nonce] = challenge{deviceID: deviceID, expires: exp}
	c.mu.Unlock()
	return nonce, exp
}

func (c *challengeStore) consume(nonce, deviceID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch, ok := c.m[nonce]
	delete(c.m, nonce)
	return ok && ch.deviceID == deviceID && time.Now().Before(ch.expires)
}

func (c *challengeStore) gc() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, v := range c.m {
		if time.Now().After(v.expires) {
			delete(c.m, k)
		}
	}
}
