// Package client is the desktop core's client for braid-server. It handles
// device-key authentication transparently: callers just make requests.
package client

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/sam-jeston/braid/internal/api"
	"github.com/sam-jeston/braid/internal/authn"
)

type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("server returned %d", e.Status)
}

func IsStatus(err error, status int) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == status
}

var httpClient = &http.Client{Timeout: 60 * time.Second}

var protocol = strconv.Itoa(api.Protocol)

// NormalizeServerURL validates a user-entered server URL. Plain HTTP is only
// accepted for loopback addresses unless allowInsecure is set.
func NormalizeServerURL(raw string, allowInsecure bool) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("enter your team's Braid server URL")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("%q is not a valid URL", raw)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", fmt.Errorf("unsupported URL scheme %q", u.Scheme)
	}
	if u.Scheme == "http" && !allowInsecure && !isLoopback(u.Hostname()) {
		return "", errors.New("the server must use HTTPS (plain HTTP is only allowed for localhost)")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery, u.Fragment = "", ""
	return u.String(), nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func decodeResp(resp *http.Response, out any) error {
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		var eb api.ErrorBody
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if json.Unmarshal(body, &eb) == nil && eb.Error.Message != "" {
			return &Error{Status: resp.StatusCode, Code: eb.Error.Code, Message: eb.Error.Message}
		}
		return &Error{Status: resp.StatusCode, Message: fmt.Sprintf("server returned %s", resp.Status)}
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func publicCall(ctx context.Context, method, url string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(api.ProtocolHeader, protocol)
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("can't reach the Braid server: %w", err)
	}
	return decodeResp(resp, out)
}

// Info checks that baseURL is a Braid server and returns its identity.
func Info(ctx context.Context, baseURL string) (api.ServerInfo, error) {
	var info api.ServerInfo
	if err := publicCall(ctx, "GET", baseURL+"/v1/info", nil, &info); err != nil {
		return info, err
	}
	if info.Product != "braid" || info.ServerID == "" {
		return info, errors.New("that URL doesn't look like a Braid server")
	}
	return info, nil
}

// IncompatibleError means this app and the server don't share a protocol
// version, so one of them has to be updated.
type IncompatibleError struct {
	UpdateApp     bool   // this app is too old for the server; otherwise the server is too old for the app
	ServerVersion string // the server's release, e.g. "v0.4.0" ("dev" for local builds)
}

func (e *IncompatibleError) Error() string {
	version := ""
	if e.ServerVersion != "" && e.ServerVersion != "dev" {
		version = " (" + e.ServerVersion + ")"
	}
	if e.UpdateApp {
		return "This version of Braid is too old for your team's server" + version + ". Update Braid to reconnect."
	}
	return "Your team's server" + version + " is too old for this version of Braid. Ask whoever runs it to update braid-server."
}

// CheckCompatible reports whether this app can talk to the server described
// by info.
func CheckCompatible(info api.ServerInfo) error {
	return checkCompatible(info, api.Protocol, api.MinServerProtocol)
}

func checkCompatible(info api.ServerInfo, proto, minServer int) error {
	switch {
	case proto < info.MinClientProtocol:
		return &IncompatibleError{UpdateApp: true, ServerVersion: info.Version}
	case info.Protocol < minServer:
		return &IncompatibleError{ServerVersion: info.Version}
	}
	return nil
}

// AsIncompatible recognizes an incompatibility found by CheckCompatible, or
// reported by the server when it turns this app away.
func AsIncompatible(err error) (*IncompatibleError, bool) {
	var ie *IncompatibleError
	if errors.As(err, &ie) {
		return ie, true
	}
	var e *Error
	if errors.As(err, &e) && e.Code == api.CodeClientOutdated {
		return &IncompatibleError{UpdateApp: true}, true
	}
	return nil, false
}

// Registration signs a device registration for the given code.
func Registration(key ed25519.PrivateKey, serverID, code, deviceName string) api.DeviceRegistration {
	pub := authn.EncodePublicKey(key.Public().(ed25519.PublicKey))
	return api.DeviceRegistration{
		PublicKey: pub,
		Name:      deviceName,
		Signature: authn.Sign(key, authn.RegistrationMessage(serverID, code, pub)),
	}
}

func CreateTeam(ctx context.Context, baseURL string, req api.CreateTeamRequest) (api.JoinResponse, error) {
	var out api.JoinResponse
	return out, publicCall(ctx, "POST", baseURL+"/v1/setup/teams", req, &out)
}

func RedeemInvite(ctx context.Context, baseURL string, req api.RedeemInviteRequest) (api.JoinResponse, error) {
	var out api.JoinResponse
	return out, publicCall(ctx, "POST", baseURL+"/v1/invites/redeem", req, &out)
}

// Client is an authenticated connection to one team on one server.
type Client struct {
	BaseURL  string
	ServerID string
	DeviceID string
	key      ed25519.PrivateKey

	mu       sync.Mutex
	token    string
	tokenExp time.Time
}

func New(baseURL, serverID, deviceID string, key ed25519.PrivateKey) *Client {
	return &Client{BaseURL: baseURL, ServerID: serverID, DeviceID: deviceID, key: key}
}

// Token returns a valid access token, signing a fresh challenge when needed.
func (c *Client) Token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Until(c.tokenExp) > time.Minute {
		return c.token, nil
	}
	var ch api.ChallengeResponse
	if err := publicCall(ctx, "POST", c.BaseURL+"/v1/auth/challenge", api.ChallengeRequest{DeviceID: c.DeviceID}, &ch); err != nil {
		return "", err
	}
	var tok api.TokenResponse
	err := publicCall(ctx, "POST", c.BaseURL+"/v1/auth/token", api.TokenRequest{
		DeviceID:  c.DeviceID,
		Nonce:     ch.Nonce,
		Signature: authn.Sign(c.key, authn.TokenMessage(c.ServerID, c.DeviceID, ch.Nonce)),
	}, &tok)
	if err != nil {
		return "", err
	}
	c.token, c.tokenExp = tok.Token, tok.ExpiresAt
	return c.token, nil
}

func (c *Client) invalidate() {
	c.mu.Lock()
	c.token = ""
	c.mu.Unlock()
}

// Raw performs an authenticated request, retrying once with a fresh token on 401.
func (c *Client) Raw(ctx context.Context, method, path string, body []byte, contentType string) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		tok, err := c.Token(ctx)
		if err != nil {
			return nil, err
		}
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rd)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set(api.ProtocolHeader, protocol)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("can't reach the Braid server: %w", err)
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			resp.Body.Close()
			c.invalidate()
			continue
		}
		return resp, nil
	}
}

func (c *Client) Do(ctx context.Context, method, path string, in, out any) error {
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return err
		}
	}
	resp, err := c.Raw(ctx, method, path, body, "application/json")
	if err != nil {
		return err
	}
	return decodeResp(resp, out)
}

// Dial opens the realtime event stream. since < 0 starts from "now".
func (c *Client) Dial(ctx context.Context, since int64) (*websocket.Conn, error) {
	tok, err := c.Token(ctx)
	if err != nil {
		return nil, err
	}
	u := strings.Replace(c.BaseURL, "http", "ws", 1) + "/v1/ws"
	if since >= 0 {
		u += fmt.Sprintf("?since=%d", since)
	}
	conn, resp, err := websocket.Dial(ctx, u, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + tok}, api.ProtocolHeader: {protocol}},
	})
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			c.invalidate()
		}
		if resp != nil && resp.StatusCode == http.StatusUpgradeRequired {
			return nil, &IncompatibleError{UpdateApp: true}
		}
		return nil, err
	}
	conn.SetReadLimit(32 << 20)
	return conn, nil
}

// --- typed helpers ---

func (c *Client) Me(ctx context.Context) (api.Me, error) {
	var out api.Me
	return out, c.Do(ctx, "GET", "/v1/me", nil, &out)
}

func (c *Client) Members(ctx context.Context) ([]api.Member, error) {
	var out []api.Member
	return out, c.Do(ctx, "GET", "/v1/members", nil, &out)
}

func (c *Client) Thread(ctx context.Context, id string) (api.ThreadDetail, error) {
	var out api.ThreadDetail
	return out, c.Do(ctx, "GET", "/v1/threads/"+url.PathEscape(id), nil, &out)
}

func (c *Client) CreateThread(ctx context.Context, req api.CreateThreadRequest) (api.ThreadDetail, error) {
	var out api.ThreadDetail
	return out, c.Do(ctx, "POST", "/v1/threads", req, &out)
}

func (c *Client) PostMessage(ctx context.Context, threadID string, req api.CreateMessageRequest) (api.Message, error) {
	var out api.Message
	return out, c.Do(ctx, "POST", "/v1/threads/"+url.PathEscape(threadID)+"/messages", req, &out)
}

func (c *Client) Approve(ctx context.Context, threadID string, stage api.Stage) (api.Thread, error) {
	var out api.Thread
	return out, c.Do(ctx, "POST", "/v1/threads/"+url.PathEscape(threadID)+"/stages/"+string(stage)+"/approve", nil, &out)
}

func (c *Client) CreateRun(ctx context.Context, req api.CreateRunRequest) (api.Run, error) {
	var out api.Run
	return out, c.Do(ctx, "POST", "/v1/runs", req, &out)
}

func (c *Client) RunOutput(ctx context.Context, runID string, items []api.RunOutput) error {
	return c.Do(ctx, "POST", "/v1/runs/"+url.PathEscape(runID)+"/output", api.RunOutputBatch{Items: items}, nil)
}

func (c *Client) FinishRun(ctx context.Context, runID string, req api.FinishRunRequest) (api.Run, error) {
	var out api.Run
	return out, c.Do(ctx, "POST", "/v1/runs/"+url.PathEscape(runID)+"/finish", req, &out)
}

func (c *Client) CreateExport(ctx context.Context, threadID string, req api.CreateExportRequest) (api.Export, error) {
	var out api.Export
	return out, c.Do(ctx, "POST", "/v1/threads/"+url.PathEscape(threadID)+"/exports", req, &out)
}
