package local

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sam-jeston/braid/internal/api"
	"github.com/sam-jeston/braid/internal/client"
	"github.com/sam-jeston/braid/internal/server"
)

// TestIncompatibleServer upgrades a team's server past what this app speaks,
// then rolls it back: the app asks for an update, then reconnects by itself.
func TestIncompatibleServer(t *testing.T) {
	t.Setenv("BRAID_SECRETS", "file")
	old := incompatibleRetry
	incompatibleRetry = 50 * time.Millisecond
	defer func() { incompatibleRetry = old }()
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	db := filepath.Join(t.TempDir(), "v.db")

	var handler atomic.Value
	serve := func(minClient int) *server.Server {
		srv, err := server.New(server.Config{DBPath: db, Version: "v9.1.0", Logger: log, MinClientProtocol: minClient})
		if err != nil {
			t.Fatal(err)
		}
		handler.Store(srv.Handler())
		return srv
	}
	srv := serve(0)
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.Load().(http.Handler).ServeHTTP(w, r)
	}))
	defer hs.Close()
	code, _ := srv.CreateSetupCode(ctx, time.Hour)

	home := t.TempDir()
	c, err := New(Options{Home: home, Version: "test", Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTeam(ctx, CreateTeamInput{
		JoinInput: JoinInput{ServerURL: hs.URL, Code: code, DisplayName: "Priya"},
		Roles:     api.AllRoles,
		Team:      api.TeamSettings{Name: "Acme", Output: api.OutputTarget{Type: "markdown"}},
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "connected", func() bool { return c.State().Connection == "connected" })
	if v := c.State().ServerVersion; v != "v9.1.0" {
		t.Errorf("server version = %q", v)
	}
	c.Shutdown()

	// The server is upgraded to one that no longer supports this app.
	srv.Close()
	srv = serve(api.Protocol + 1)
	c, err = New(Options{Home: home, Version: "test", Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { c.Shutdown() }()
	waitFor(t, "incompatible", func() bool { return c.State().Connection == "incompatible" })
	st := c.State()
	if st.Update == nil || !st.Update.App || st.Update.ServerVersion != "v9.1.0" || st.Update.DownloadURL != ReleasesURL+"/tag/v9.1.0" {
		t.Errorf("update = %+v", st.Update)
	}
	if !strings.Contains(st.ConnectionError, "Update Braid") {
		t.Errorf("message: %s", st.ConnectionError)
	}
	// Requests the UI makes anyway are turned away by the server.
	if _, _, err := c.Proxy(ctx, "GET", "/v1/team", nil); err == nil || !strings.Contains(err.Error(), "Update Braid") {
		t.Errorf("proxied request: %v", err)
	} else if _, ok := client.AsIncompatible(err); !ok {
		t.Errorf("proxied request error isn't recognized: %v", err)
	}
	// So is joining another team on that server.
	var ie *client.IncompatibleError
	if _, err := c.Probe(ctx, hs.URL, false); !errors.As(err, &ie) || !ie.UpdateApp {
		t.Errorf("probe: %v", err)
	}

	// Rolling the server back reconnects without restarting the app.
	srv.Close()
	srv = serve(0)
	defer srv.Close()
	waitFor(t, "reconnected", func() bool { return c.State().Connection == "connected" })
	if c.State().Update != nil {
		t.Error("update notice should clear once connected")
	}
}

func TestUpdateDownloadURL(t *testing.T) {
	for v, want := range map[string]string{
		"v1.2.3":            ReleasesURL + "/tag/v1.2.3",
		"v1.3.0-rc.1":       ReleasesURL + "/tag/v1.3.0-rc.1",
		"v1.2.3-4-gabc1234": ReleasesURL + "/latest", // a build between releases (git describe)
		"dev":               ReleasesURL + "/latest",
		"73768fe":           ReleasesURL + "/latest",
		"v1.2.3/../../x":    ReleasesURL + "/latest",
	} {
		if got := updateNeeded(&client.IncompatibleError{UpdateApp: true, ServerVersion: v}).DownloadURL; got != want {
			t.Errorf("%s: %s", v, got)
		}
	}
	if u := updateNeeded(&client.IncompatibleError{ServerVersion: "v1.0.0"}); u.App || u.DownloadURL != "" {
		t.Errorf("server too old: %+v", u)
	}
}
