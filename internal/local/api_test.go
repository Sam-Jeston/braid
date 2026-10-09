package local

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"
)

const testToken = "local-secret"

func newTestAPI(t *testing.T) (*Core, *httptest.Server) {
	t.Helper()
	t.Setenv("BRAID_SECRETS", "file")
	c := newCore(t, slog.New(slog.DiscardHandler))
	t.Cleanup(c.Shutdown)
	ui := fstest.MapFS{
		"index.html": {Data: []byte("<!doctype html><title>Braid</title>")},
		"app.js":     {Data: []byte("console.log(1)")},
	}
	hs := httptest.NewServer(c.NewAPI(testToken, ui))
	t.Cleanup(hs.Close)
	return c, hs
}

func do(t *testing.T, hs *httptest.Server, method, path, token string, body string, header ...string) (*http.Response, string) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, hs.URL+path, r)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(header); i += 2 {
		if header[i] == "Host" {
			req.Host = header[i+1]
		} else {
			req.Header.Set(header[i], header[i+1])
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestAPIRequiresToken(t *testing.T) {
	_, hs := newTestAPI(t)
	for _, tok := range []string{"", "wrong", testToken + "x"} {
		if resp, _ := do(t, hs, "GET", "/local/state", tok, ""); resp.StatusCode != 401 {
			t.Errorf("token %q: %d", tok, resp.StatusCode)
		}
		if resp, _ := do(t, hs, "GET", "/remote/v1/team", tok, ""); resp.StatusCode != 401 {
			t.Errorf("proxy with token %q: %d", tok, resp.StatusCode)
		}
	}
	resp, body := do(t, hs, "GET", "/local/state", testToken, "")
	var st State
	if resp.StatusCode != 200 || json.Unmarshal([]byte(body), &st) != nil || st.Version != "test" {
		t.Fatalf("state: %d %s", resp.StatusCode, body)
	}
}

func TestAPIRejectsForeignHosts(t *testing.T) {
	// DNS rebinding: a page on evil.example resolving to 127.0.0.1.
	_, hs := newTestAPI(t)
	for _, host := range []string{"evil.example", "evil.example:80", "127.0.0.1.nip.io"} {
		if resp, _ := do(t, hs, "GET", "/", "", "", "Host", host); resp.StatusCode != 403 {
			t.Errorf("host %s: %d", host, resp.StatusCode)
		}
	}
	if resp, _ := do(t, hs, "GET", "/local/state", testToken, "", "Host", "localhost:1234"); resp.StatusCode != 200 {
		t.Errorf("localhost: %d", resp.StatusCode)
	}
}

func TestAPICORS(t *testing.T) {
	_, hs := newTestAPI(t)
	resp, _ := do(t, hs, "OPTIONS", "/local/state", "", "", "Origin", "wails://wails")
	if resp.StatusCode != 204 || resp.Header.Get("Access-Control-Allow-Origin") != "wails://wails" {
		t.Errorf("webview preflight: %d %v", resp.StatusCode, resp.Header)
	}
	resp, _ = do(t, hs, "GET", "/local/state", testToken, "", "Origin", "https://evil.example")
	if resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Error("CORS granted to a foreign origin")
	}
}

func TestAPIProxyDenylist(t *testing.T) {
	_, hs := newTestAPI(t)
	// The UI must never drive device auth or registration itself.
	for _, p := range []string{"/remote/v1/auth/token", "/remote/v1/auth/challenge", "/remote/v1/ws", "/remote/v1/setup/teams",
		"/remote/v1/invites/redeem", "/remote/v2/team", "/remote/healthz", "/remote/v1/threads/../auth/token"} {
		if resp, body := do(t, hs, "POST", p, testToken, "{}"); resp.StatusCode != 403 {
			t.Errorf("%s: %d %s", p, resp.StatusCode, body)
		}
	}
	// Allowed paths reach the core, which isn't connected here.
	if resp, body := do(t, hs, "GET", "/remote/v1/team", testToken, ""); resp.StatusCode != 502 || !strings.Contains(body, "not connected") {
		t.Errorf("allowed path: %d %s", resp.StatusCode, body)
	}
}

func TestAPIValidatesInput(t *testing.T) {
	_, hs := newTestAPI(t)
	if resp, body := do(t, hs, "PUT", "/local/settings", testToken, "{not json"); resp.StatusCode != 400 || !strings.Contains(body, "invalid request body") {
		t.Errorf("bad json: %d %s", resp.StatusCode, body)
	}
	if resp, _ := do(t, hs, "PUT", "/local/settings", testToken, `{"agent":"skynet"}`); resp.StatusCode != 400 {
		t.Errorf("unknown agent: %d", resp.StatusCode)
	}
	if resp, _ := do(t, hs, "PUT", "/local/credentials", testToken, `{"key":"aws-secret","value":"x"}`); resp.StatusCode != 400 {
		t.Errorf("unknown credential: %d", resp.StatusCode)
	}
	if resp, _ := do(t, hs, "POST", "/local/open", testToken, `{"target":"/etc/passwd"}`); resp.StatusCode != 400 {
		t.Errorf("opened an arbitrary file: %d", resp.StatusCode)
	}
	if resp, _ := do(t, hs, "POST", "/local/runs", testToken, `{"thread_id":"thr_x","stage":"sparring"}`); resp.StatusCode < 400 {
		t.Errorf("started a run without a team: %d", resp.StatusCode)
	}
}

func TestAPICredentialsStayLocal(t *testing.T) {
	c, hs := newTestAPI(t)
	if resp, _ := do(t, hs, "PUT", "/local/credentials", testToken, `{"key":"linear-api-key","value":"lin_api_secret"}`); resp.StatusCode != 204 {
		t.Fatalf("store: %d", resp.StatusCode)
	}
	_, body := do(t, hs, "GET", "/local/state", testToken, "")
	if strings.Contains(body, "lin_api_secret") || !strings.Contains(body, `"linear-api-key":true`) {
		t.Errorf("state should say a key is stored without revealing it: %s", body)
	}
	if v, err := c.secrets.Get("linear-api-key"); err != nil || v != "lin_api_secret" {
		t.Errorf("secret = %q, %v", v, err)
	}
	do(t, hs, "PUT", "/local/credentials", testToken, `{"key":"linear-api-key","value":""}`)
	if _, err := c.secrets.Get("linear-api-key"); err == nil {
		t.Error("clearing a credential should delete it")
	}
}

func TestAPIServesUI(t *testing.T) {
	_, hs := newTestAPI(t)
	for _, p := range []string{"/", "/threads/thr_123"} { // SPA routes fall back to index.html
		if resp, body := do(t, hs, "GET", p, "", ""); resp.StatusCode != 200 || !strings.Contains(body, "<title>Braid</title>") {
			t.Errorf("%s: %d %s", p, resp.StatusCode, body)
		}
	}
	if resp, body := do(t, hs, "GET", "/app.js", "", ""); resp.StatusCode != 200 || body != "console.log(1)" {
		t.Errorf("asset: %d %s", resp.StatusCode, body)
	}
	if resp, _ := do(t, hs, "POST", "/", "", ""); resp.StatusCode != 405 {
		t.Errorf("POST /: %d", resp.StatusCode)
	}
}

func TestAPIWebsocket(t *testing.T) {
	c, hs := newTestAPI(t)
	url := "ws" + strings.TrimPrefix(hs.URL, "http") + "/local/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, resp, err := websocket.Dial(ctx, url+"?token=wrong", nil); err == nil || resp.StatusCode != 401 {
		t.Errorf("websocket with a bad token: %v", err)
	}
	// A page on another origin can't read the event stream, even with the token.
	if _, resp, err := websocket.Dial(ctx, url+"?token="+testToken, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": []string{"https://evil.example"}}}); err == nil || resp.StatusCode != 403 {
		t.Errorf("cross-origin websocket: %v", err)
	}

	ws, _, err := websocket.Dial(ctx, url+"?token="+testToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	time.Sleep(50 * time.Millisecond) // let the subscription register
	c.bus.PublishLocal("local.state", nil)
	_, b, err := ws.Read(ctx)
	if err != nil || !strings.Contains(string(b), `"type":"local.state"`) {
		t.Fatalf("event: %s %v", b, err)
	}
}
