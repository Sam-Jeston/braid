package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sam-jeston/braid/internal/api"
)

func TestProtocolGate(t *testing.T) {
	s, err := New(Config{
		DBPath: filepath.Join(t.TempDir(), "t.db"), Version: "v9.1.0", Logger: slog.New(slog.DiscardHandler),
		MinClientProtocol: api.Protocol + 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(func() { hs.Close(); s.Close() })

	get := func(path string, proto string) (int, string) {
		req, _ := http.NewRequest("GET", hs.URL+path, nil)
		if proto != "" {
			req.Header.Set(api.ProtocolHeader, proto)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	// Any app can read /v1/info to find out what the server speaks.
	status, body := get("/v1/info", "")
	var info api.ServerInfo
	if status != 200 || json.Unmarshal([]byte(body), &info) != nil {
		t.Fatalf("info: %d %s", status, body)
	}
	if info.Protocol != api.Protocol || info.MinClientProtocol != api.Protocol+1 || info.Version != "v9.1.0" {
		t.Errorf("info = %+v", info)
	}
	if status, _ := get("/healthz", ""); status != 200 {
		t.Errorf("healthz: %d", status)
	}

	// Older apps, including ones that predate the header, are told to update.
	for _, proto := range []string{"", "junk", "0", strconv.Itoa(api.Protocol)} {
		status, body := get("/v1/team", proto)
		if status != http.StatusUpgradeRequired || !strings.Contains(body, api.CodeClientOutdated) || !strings.Contains(body, "Update Braid") || !strings.Contains(body, "v9.1.0") {
			t.Errorf("protocol %q: %d %s", proto, status, body)
		}
	}
	// A new enough app gets through to the normal checks.
	if status, _ := get("/v1/team", strconv.Itoa(api.Protocol+1)); status != 401 {
		t.Errorf("current app: %d", status)
	}
}

func TestDefaultServerAcceptsSupportedApps(t *testing.T) {
	_, hs := newTestServer(t)
	req, _ := http.NewRequest("GET", hs.URL+"/v1/team", nil)
	req.Header.Set(api.ProtocolHeader, strconv.Itoa(api.MinClientProtocol))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("oldest supported app: %d", resp.StatusCode)
	}
}
