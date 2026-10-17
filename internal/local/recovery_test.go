package local

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sam-jeston/braid/internal/api"
	"github.com/sam-jeston/braid/internal/server"
)

// TestRecovery signs a team's only admin back in with recovery codes: first
// on a new machine, then on the original one after its device key is lost.
func TestRecovery(t *testing.T) {
	t.Setenv("BRAID_SECRETS", "file")
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, err := server.New(server.Config{DBPath: filepath.Join(t.TempDir(), "braid.db"), Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()
	setup, _ := srv.CreateSetupCode(ctx, time.Hour)

	home := t.TempDir()
	laptop, err := New(Options{Home: home, Version: "test", Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { laptop.Shutdown() }()
	if _, err := laptop.CreateTeam(ctx, CreateTeamInput{
		JoinInput: JoinInput{ServerURL: hs.URL, Code: setup, DisplayName: "Priya"},
		Roles:     api.AllRoles,
		Team:      api.TeamSettings{Name: "Acme", Output: api.OutputTarget{Type: "markdown"}},
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "laptop connected", func() bool { return laptop.State().Connection == "connected" })
	me := laptop.State().Me.ID

	// A new machine signs in as Priya, and the laptop is signed out.
	rc, err := srv.CreateRecoveryCode(ctx, "", "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	desktop := newCore(t, log)
	defer desktop.Shutdown()
	if _, err := desktop.Join(ctx, JoinInput{ServerURL: hs.URL, Code: rc.Code}); err != nil {
		t.Fatalf("recover on desktop: %v", err)
	}
	waitFor(t, "desktop connected", func() bool { return desktop.State().Connection == "connected" })
	if st := desktop.State(); st.Me.ID != me || !st.Me.IsAdmin || st.Me.DisplayName != "Priya" {
		t.Errorf("desktop signed in as %+v", st.Me)
	}
	waitFor(t, "laptop revoked", func() bool { return laptop.State().Connection == "revoked" })

	// The laptop loses its key, so it keeps the profile but can't connect.
	prof := laptop.State().Profiles[0]
	laptop.mu.Lock()
	laptop.cfg.Profiles[0].RepoOverrides = map[string]string{"web": t.TempDir()}
	override := laptop.cfg.Profiles[0].RepoOverrides["web"]
	err = laptop.saveConfig()
	laptop.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := laptop.secrets.Delete(keyName(prof.ID)); err != nil {
		t.Fatal(err)
	}
	laptop.Shutdown()
	laptop, err = New(Options{Home: home, Version: "test", Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	if st := laptop.State(); st.Active != nil || len(st.Profiles) != 1 {
		t.Fatalf("after losing the key: active %+v, %d profiles", st.Active, len(st.Profiles))
	}

	// Recovering there replaces the old profile, keeping its local settings.
	rc, err = srv.CreateRecoveryCode(ctx, "Acme", "Priya", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := laptop.Join(ctx, JoinInput{ServerURL: hs.URL, Code: rc.Code}); err != nil {
		t.Fatalf("recover on laptop: %v", err)
	}
	waitFor(t, "laptop connected again", func() bool { return laptop.State().Connection == "connected" })
	st := laptop.State()
	if len(st.Profiles) != 1 || st.Profiles[0].ID == prof.ID || st.Me.ID != me {
		t.Fatalf("profiles = %+v, me = %+v", st.Profiles, st.Me)
	}
	if got := st.Profiles[0].RepoOverrides["web"]; got != override {
		t.Errorf("repo override = %q, want %q", got, override)
	}
	waitFor(t, "desktop revoked", func() bool { return desktop.State().Connection == "revoked" })
}
