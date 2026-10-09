package local

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/sam-jeston/braid/internal/client"
)

// API is the loopback HTTP/WebSocket interface between the UI and the core.
//
// Security model: it listens on 127.0.0.1 only, every request must carry a
// per-launch random token (a header, so browsers can't forge it cross-site),
// the Host header must be a loopback name (DNS-rebinding defence), and CORS
// is only granted to the Wails webview origins.
type API struct {
	core    *Core
	token   string
	static  fs.FS
	origins map[string]bool
	mux     *http.ServeMux
}

func (c *Core) NewAPI(token string, static fs.FS) *API {
	a := &API{
		core: c, token: token, static: static,
		origins: map[string]bool{
			"wails://wails": true, "wails://wails.localhost": true,
			"http://wails.localhost": true, "https://wails.localhost": true,
			// Some webviews report custom-scheme pages as an opaque origin.
			// The per-launch token, not CORS, is what guards this API.
			"null": true,
		},
	}
	m := http.NewServeMux()
	m.HandleFunc("GET /local/state", a.auth(a.state))
	m.HandleFunc("POST /local/probe", a.auth(a.probe))
	m.HandleFunc("POST /local/join", a.auth(a.join))
	m.HandleFunc("POST /local/create-team", a.auth(a.createTeam))
	m.HandleFunc("POST /local/profiles/{id}/activate", a.auth(a.activate))
	m.HandleFunc("DELETE /local/profiles/{id}", a.auth(a.leave))
	m.HandleFunc("PUT /local/settings", a.auth(a.settings))
	m.HandleFunc("PUT /local/credentials", a.auth(a.credentials))
	m.HandleFunc("POST /local/agents/refresh", a.auth(a.refreshAgents))
	m.HandleFunc("POST /local/runs", a.auth(a.startRun))
	m.HandleFunc("POST /local/scan", a.auth(a.startScan))
	m.HandleFunc("POST /local/runs/{id}/cancel", a.auth(a.cancelRun))
	m.HandleFunc("POST /local/repo/sync", a.auth(a.syncRepo))
	m.HandleFunc("POST /local/threads/{id}/publish", a.auth(a.publish))
	m.HandleFunc("POST /local/threads/{id}/save-markdown", a.auth(a.saveMarkdown))
	m.HandleFunc("POST /local/open", a.auth(a.open))
	m.HandleFunc("POST /local/presence", a.auth(a.presence))
	m.HandleFunc("GET /local/ws", a.ws)
	m.HandleFunc("/remote/", a.tokenOnly(a.proxy))
	if static != nil {
		m.Handle("/", a.staticHandler())
	}
	a.mux = m
	return a
}

func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		http.Error(w, "forbidden host", http.StatusForbidden)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && a.origins[origin] {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Vary", "Origin")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	a.mux.ServeHTTP(w, r)
}

func (a *API) validToken(t string) bool {
	return t != "" && subtle.ConstantTimeCompare([]byte(t), []byte(a.token)) == 1
}

func (a *API) tokenOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !a.validToken(tok) {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "missing or invalid local token")
			return
		}
		next(w, r)
	}
}

func (a *API) auth(fn func(w http.ResponseWriter, r *http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !a.validToken(tok) {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "missing or invalid local token")
			return
		}
		out, err := fn(w, r)
		if err != nil {
			status, code := http.StatusBadRequest, "error"
			var ce *client.Error
			if errors.As(err, &ce) {
				status, code = ce.Status, ce.Code
				if status == 0 {
					status = http.StatusBadGateway
				}
			}
			writeErr(w, status, code, err.Error())
			return
		}
		if out == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": msg}})
}

func body[T any](r *http.Request) (T, error) {
	var v T
	err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&v)
	if err != nil && !errors.Is(err, io.EOF) {
		return v, errors.New("invalid request body")
	}
	return v, nil
}

func ctxTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}

func (a *API) state(w http.ResponseWriter, r *http.Request) (any, error) { return a.core.State(), nil }

func (a *API) probe(w http.ResponseWriter, r *http.Request) (any, error) {
	in, err := body[struct {
		ServerURL     string `json:"server_url"`
		AllowInsecure bool   `json:"allow_insecure"`
	}](r)
	if err != nil {
		return nil, err
	}
	ctx, cancel := ctxTimeout(r, 15*time.Second)
	defer cancel()
	return a.core.Probe(ctx, in.ServerURL, in.AllowInsecure)
}

func (a *API) join(w http.ResponseWriter, r *http.Request) (any, error) {
	in, err := body[JoinInput](r)
	if err != nil {
		return nil, err
	}
	ctx, cancel := ctxTimeout(r, 30*time.Second)
	defer cancel()
	return a.core.Join(ctx, in)
}

func (a *API) createTeam(w http.ResponseWriter, r *http.Request) (any, error) {
	in, err := body[CreateTeamInput](r)
	if err != nil {
		return nil, err
	}
	ctx, cancel := ctxTimeout(r, 30*time.Second)
	defer cancel()
	return a.core.CreateTeam(ctx, in)
}

func (a *API) activate(w http.ResponseWriter, r *http.Request) (any, error) {
	return nil, a.core.Activate(r.PathValue("id"))
}

func (a *API) leave(w http.ResponseWriter, r *http.Request) (any, error) {
	ctx, cancel := ctxTimeout(r, 15*time.Second)
	defer cancel()
	return nil, a.core.Leave(ctx, r.PathValue("id"))
}

func (a *API) settings(w http.ResponseWriter, r *http.Request) (any, error) {
	in, err := body[SettingsPatch](r)
	if err != nil {
		return nil, err
	}
	if err := a.core.UpdateSettings(in); err != nil {
		return nil, err
	}
	return a.core.State(), nil
}

func (a *API) credentials(w http.ResponseWriter, r *http.Request) (any, error) {
	in, err := body[struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}](r)
	if err != nil {
		return nil, err
	}
	return nil, a.core.SetCredential(in.Key, in.Value)
}

func (a *API) refreshAgents(w http.ResponseWriter, r *http.Request) (any, error) {
	return a.core.RefreshAgents(r.Context()), nil
}

func (a *API) startRun(w http.ResponseWriter, r *http.Request) (any, error) {
	in, err := body[StartRunInput](r)
	if err != nil {
		return nil, err
	}
	ctx, cancel := ctxTimeout(r, 30*time.Second)
	defer cancel()
	return a.core.StartRun(ctx, in)
}

func (a *API) startScan(w http.ResponseWriter, r *http.Request) (any, error) {
	ctx, cancel := ctxTimeout(r, 30*time.Second)
	defer cancel()
	return a.core.StartScan(ctx)
}

func (a *API) cancelRun(w http.ResponseWriter, r *http.Request) (any, error) {
	ctx, cancel := ctxTimeout(r, 15*time.Second)
	defer cancel()
	return nil, a.core.CancelRun(ctx, r.PathValue("id"))
}

func (a *API) syncRepo(w http.ResponseWriter, r *http.Request) (any, error) {
	ctx, cancel := ctxTimeout(r, 20*time.Minute)
	defer cancel()
	return a.core.SyncRepo(ctx, true)
}

func (a *API) publish(w http.ResponseWriter, r *http.Request) (any, error) {
	ctx, cancel := ctxTimeout(r, 5*time.Minute)
	defer cancel()
	return a.core.Publish(ctx, r.PathValue("id"))
}

func (a *API) saveMarkdown(w http.ResponseWriter, r *http.Request) (any, error) {
	ctx, cancel := ctxTimeout(r, time.Minute)
	defer cancel()
	folder, err := a.core.SaveMarkdown(ctx, r.PathValue("id"))
	if err != nil {
		return nil, err
	}
	return map[string]string{"folder": folder}, nil
}

func (a *API) open(w http.ResponseWriter, r *http.Request) (any, error) {
	in, err := body[struct {
		Target string `json:"target"`
	}](r)
	if err != nil {
		return nil, err
	}
	return nil, a.core.Open(in.Target)
}

func (a *API) presence(w http.ResponseWriter, r *http.Request) (any, error) {
	in, err := body[struct {
		ThreadID string `json:"thread_id"`
	}](r)
	if err != nil {
		return nil, err
	}
	a.core.SetPresence(in.ThreadID)
	return nil, nil
}

// proxy forwards /remote/v1/... to the team server with this device's credentials.
func (a *API) proxy(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/remote")
	if !strings.HasPrefix(path, "/v1/") || strings.HasPrefix(path, "/v1/ws") || strings.HasPrefix(path, "/v1/auth/") ||
		strings.HasPrefix(path, "/v1/setup/") || strings.HasPrefix(path, "/v1/invites/redeem") {
		writeErr(w, http.StatusForbidden, "forbidden", "path not allowed")
		return
	}
	if r.URL.RawQuery != "" {
		path += "?" + r.URL.RawQuery
	}
	var payload []byte
	if r.Body != nil {
		payload, _ = io.ReadAll(io.LimitReader(r.Body, 40<<20))
	}
	if len(payload) == 0 {
		payload = nil
	}
	status, resp, err := a.core.Proxy(r.Context(), r.Method, path, payload)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "offline", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(resp)
}

// ws streams core and team events to the UI. The token travels as a query
// parameter because browsers can't set websocket headers.
func (a *API) ws(w http.ResponseWriter, r *http.Request) {
	if !a.validToken(r.URL.Query().Get("token")) {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "missing or invalid local token")
		return
	}
	opts := &websocket.AcceptOptions{}
	if origin := r.Header.Get("Origin"); origin != "" && a.origins[origin] {
		opts.InsecureSkipVerify = true // origin is one of our own webviews
	}
	c, err := websocket.Accept(w, r, opts)
	if err != nil {
		return
	}
	defer c.CloseNow()
	ctx := c.CloseRead(r.Context())
	ch, unsub := a.core.bus.Subscribe()
	defer unsub()
	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case b := <-ch:
			wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.Write(wctx, websocket.MessageText, b)
			cancel()
			if err != nil {
				return
			}
		case <-ping.C:
			pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.Ping(pctx)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

func (a *API) staticHandler() http.Handler {
	files := http.FileServer(http.FS(a.static))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" {
			if _, err := fs.Stat(a.static, p); err == nil {
				files.ServeHTTP(w, r)
				return
			}
		}
		// SPA fallback.
		b, err := fs.ReadFile(a.static, "index.html")
		if err != nil {
			http.Error(w, "UI not built: run `make frontend`", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(b)
	})
}
