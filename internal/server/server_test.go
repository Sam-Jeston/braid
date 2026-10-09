package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sam-jeston/braid/internal/api"
	"github.com/sam-jeston/braid/internal/authn"
)

func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	s, err := New(Config{DBPath: filepath.Join(t.TempDir(), "t.db"), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(func() { hs.Close(); s.Close() })
	return s, hs
}

func call(t *testing.T, hs *httptest.Server, method, path, token string, in any) (int, []byte) {
	t.Helper()
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, hs.URL+path, body)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func login(t *testing.T, s *Server, hs *httptest.Server, deviceID string, key []byte) (int, string) {
	t.Helper()
	_, b := call(t, hs, "POST", "/v1/auth/challenge", "", api.ChallengeRequest{DeviceID: deviceID})
	var ch api.ChallengeResponse
	json.Unmarshal(b, &ch)
	sig := authn.Sign(key, authn.TokenMessage(s.ServerID(), deviceID, ch.Nonce))
	status, b := call(t, hs, "POST", "/v1/auth/token", "", api.TokenRequest{DeviceID: deviceID, Nonce: ch.Nonce, Signature: sig})
	var tok api.TokenResponse
	json.Unmarshal(b, &tok)
	return status, tok.Token
}

func TestDeviceAuth(t *testing.T) {
	s, hs := newTestServer(t)
	code, _ := s.CreateSetupCode(context.Background(), time.Hour)
	_, key, _ := authn.GenerateKey()
	pub := authn.EncodePublicKey(key.Public().(ed25519.PublicKey))

	// A registration signed for a different code is rejected.
	bad := api.DeviceRegistration{PublicKey: pub, Signature: authn.Sign(key, authn.RegistrationMessage(s.ServerID(), "SETUP-WRONG", pub))}
	status, _ := call(t, hs, "POST", "/v1/setup/teams", "", api.CreateTeamRequest{
		SetupCode: code, Team: api.TeamSettings{Name: "T", Output: api.OutputTarget{Type: "markdown"}},
		Member: api.NewMember{DisplayName: "A", Roles: []api.Role{api.RoleProduct}}, Device: bad,
	})
	if status != 400 {
		t.Fatalf("bad signature: got %d", status)
	}

	reg := api.DeviceRegistration{PublicKey: pub, Name: "laptop", Signature: authn.Sign(key, authn.RegistrationMessage(s.ServerID(), code, pub))}
	status, b := call(t, hs, "POST", "/v1/setup/teams", "", api.CreateTeamRequest{
		SetupCode: code, Team: api.TeamSettings{Name: "T", Output: api.OutputTarget{Type: "markdown"}},
		Member: api.NewMember{DisplayName: "A", Roles: []api.Role{api.RoleProduct}}, Device: reg,
	})
	if status != 201 {
		t.Fatalf("create team: %d %s", status, b)
	}
	var join api.JoinResponse
	json.Unmarshal(b, &join)

	// Wrong key can't log in as the device.
	_, other, _ := authn.GenerateKey()
	if status, _ := login(t, s, hs, join.DeviceID, other); status != 401 {
		t.Fatalf("login with wrong key: %d", status)
	}
	status, tok := login(t, s, hs, join.DeviceID, key)
	if status != 200 || tok == "" {
		t.Fatalf("login: %d", status)
	}
	if status, _ := call(t, hs, "GET", "/v1/me", tok, nil); status != 200 {
		t.Fatalf("me: %d", status)
	}
	if status, _ := call(t, hs, "GET", "/v1/me", "brd_forged", nil); status != 401 {
		t.Fatalf("forged token: %d", status)
	}

	// Revoking the device kills its sessions and prevents new ones.
	if status, _ := call(t, hs, "DELETE", "/v1/devices/"+join.DeviceID, tok, nil); status != 204 {
		t.Fatalf("revoke: %d", status)
	}
	if status, _ := call(t, hs, "GET", "/v1/me", tok, nil); status != 401 {
		t.Fatalf("revoked token still works: %d", status)
	}
	if status, _ := login(t, s, hs, join.DeviceID, key); status != 401 {
		t.Fatalf("revoked device logged in: %d", status)
	}
}

func TestBootstrapCodeOnlyOnce(t *testing.T) {
	s, _ := newTestServer(t)
	ctx := context.Background()
	c1, _ := s.BootstrapCode(ctx)
	c2, _ := s.BootstrapCode(ctx)
	if c1 == "" || c2 != "" {
		t.Fatalf("expected one bootstrap code, got %q then %q", c1, c2)
	}
}

func TestCodeNormalization(t *testing.T) {
	code := authn.NewCode("BRD", 3)
	if authn.HashCode(code) != authn.HashCode(" "+toLowerWithSpaces(code)+" ") {
		t.Fatal("codes should match regardless of case and separators")
	}
}

func toLowerWithSpaces(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c == '-' {
			b[i] = ' '
		} else if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}
