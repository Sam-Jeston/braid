package server

import (
	"context"
	"database/sql"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sam-jeston/braid/internal/api"
	"github.com/sam-jeston/braid/internal/authn"
	"github.com/sam-jeston/braid/internal/workflow"
)

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) error {
	writeJSON(w, 200, api.ServerInfo{
		Product: "braid", Version: s.cfg.Version, ServerID: s.serverID,
		Protocol: api.Protocol, MinClientProtocol: s.cfg.MinClientProtocol,
	})
	return nil
}

// CreateSetupCode creates a single-use code that allows creating one team.
func (s *Server) CreateSetupCode(ctx context.Context, ttl time.Duration) (string, error) {
	code := authn.NewCode("SETUP", 4)
	now := s.now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO setup_codes (id, code_hash, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		authn.NewID("stp"), authn.HashCode(code), ms(now), ms(now.Add(ttl)))
	return code, err
}

// BootstrapCode returns a fresh setup code if the server has no teams and no
// unused setup codes, so a brand-new server prints one on first boot.
func (s *Server) BootstrapCode(ctx context.Context) (string, error) {
	var teams, pending int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM teams`).Scan(&teams); err != nil {
		return "", err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM setup_codes WHERE used_at IS NULL AND expires_at > ?`, ms(s.now())).Scan(&pending); err != nil {
		return "", err
	}
	if teams > 0 || pending > 0 {
		return "", nil
	}
	return s.CreateSetupCode(ctx, 7*24*time.Hour)
}

func validateNewMember(m *api.NewMember, needRoles bool) error {
	m.DisplayName = strings.TrimSpace(m.DisplayName)
	m.Email = strings.TrimSpace(m.Email)
	if m.DisplayName == "" || len(m.DisplayName) > 80 {
		return errf(400, "invalid_name", "display name is required (max 80 characters)")
	}
	if len(m.Email) > 200 {
		return errf(400, "invalid_email", "email is too long")
	}
	if needRoles {
		return validateRoles(m.Roles)
	}
	return nil
}

func validateRoles(rs []api.Role) error {
	if len(rs) == 0 {
		return errf(400, "invalid_roles", "pick at least one role")
	}
	for _, r := range rs {
		if !r.Valid() {
			return errf(400, "invalid_roles", "unknown role %q", r)
		}
	}
	return nil
}

func validateOutput(o *api.OutputTarget) error {
	if o.Config == nil {
		o.Config = map[string]string{}
	}
	for k, v := range o.Config {
		o.Config[k] = strings.TrimSpace(v)
	}
	switch o.Type {
	case api.OutputMarkdown:
	case api.OutputLinear:
		if o.Config["team_key"] == "" {
			return errf(400, "invalid_output", "Linear needs a team key (e.g. ENG)")
		}
	case api.OutputJira:
		u, err := url.Parse(o.Config["site"])
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return errf(400, "invalid_output", "Jira needs a site URL such as https://acme.atlassian.net")
		}
		o.Config["site"] = "https://" + u.Host
		if o.Config["project_key"] == "" {
			return errf(400, "invalid_output", "Jira needs a project key (e.g. ENG)")
		}
	default:
		return errf(400, "invalid_output", "output type must be markdown, linear or jira")
	}
	return nil
}

func (s *Server) verifyDevice(d api.DeviceRegistration, code string) error {
	pub, err := authn.DecodePublicKey(d.PublicKey)
	if err != nil {
		return errf(400, "invalid_key", "invalid device public key")
	}
	if !authn.Verify(pub, authn.RegistrationMessage(s.serverID, code, d.PublicKey), d.Signature) {
		return errf(400, "invalid_signature", "device signature does not match")
	}
	return nil
}

func (s *Server) insertMemberAndDevice(ctx context.Context, tx *Tx, teamID string, nm api.NewMember, roles []api.Role, admin bool, d api.DeviceRegistration) (api.Member, string, error) {
	now := s.now()
	m := api.Member{ID: authn.NewID("mem"), TeamID: teamID, DisplayName: nm.DisplayName, Email: nm.Email, Roles: roles, IsAdmin: admin, CreatedAt: now}
	if _, err := tx.ExecContext(ctx, `INSERT INTO members (id, team_id, display_name, email, roles, is_admin, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.TeamID, m.DisplayName, m.Email, mustJSON(m.Roles), m.IsAdmin, ms(now)); err != nil {
		return m, "", err
	}
	deviceID, err := s.insertDevice(ctx, tx, m.ID, d)
	return m, deviceID, err
}

func (s *Server) insertDevice(ctx context.Context, tx *Tx, memberID string, d api.DeviceRegistration) (string, error) {
	deviceID := authn.NewID("dev")
	name := strings.TrimSpace(d.Name)
	if len(name) > 80 {
		name = name[:80]
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO devices (id, member_id, public_key, name, created_at) VALUES (?, ?, ?, ?, ?)`,
		deviceID, memberID, d.PublicKey, name, ms(s.now())); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return "", errf(409, "key_in_use", "this device key is already registered")
		}
		return "", err
	}
	return deviceID, nil
}

func (s *Server) handleCreateTeam(w http.ResponseWriter, r *http.Request) error {
	var req api.CreateTeamRequest
	if err := decode(w, r, &req, 0); err != nil {
		return err
	}
	req.Team.Name = strings.TrimSpace(req.Team.Name)
	if req.Team.Name == "" || len(req.Team.Name) > 80 {
		return errf(400, "invalid_team", "team name is required (max 80 characters)")
	}
	repos, err := workflow.NormalizeRepos(req.Team.Repos)
	if err != nil {
		return errf(400, "invalid_repos", "%s", err)
	}
	if err := validateNewMember(&req.Member, true); err != nil {
		return err
	}
	if err := validateOutput(&req.Team.Output); err != nil {
		return err
	}
	if err := s.verifyDevice(req.Device, req.SetupCode); err != nil {
		return err
	}
	var resp api.JoinResponse
	err = s.tx(r.Context(), func(tx *Tx) error {
		ctx := r.Context()
		var codeID string
		err := tx.QueryRowContext(ctx, `SELECT id FROM setup_codes WHERE code_hash = ? AND used_at IS NULL AND expires_at > ?`,
			authn.HashCode(req.SetupCode), ms(s.now())).Scan(&codeID)
		if isNoRows(err) {
			return errf(403, "invalid_code", "that setup code is invalid, used or expired")
		} else if err != nil {
			return err
		}
		now := s.now()
		team := api.Team{
			ID: authn.NewID("team"), Name: req.Team.Name, Repos: repos, Output: req.Team.Output, CreatedAt: now,
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO teams (id, name, repos_json, output_json, created_at) VALUES (?, ?, ?, ?, ?)`,
			team.ID, team.Name, mustJSON(team.Repos), mustJSON(team.Output), ms(now)); err != nil {
			return err
		}
		m, deviceID, err := s.insertMemberAndDevice(ctx, tx, team.ID, req.Member, req.Member.Roles, true, req.Device)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE setup_codes SET used_at = ?, team_id = ? WHERE id = ?`, ms(now), team.ID, codeID); err != nil {
			return err
		}
		resp = api.JoinResponse{Team: team, Member: m, DeviceID: deviceID}
		return nil
	})
	if err != nil {
		return err
	}
	s.log.Info("team created", "team", resp.Team.Name, "admin", resp.Member.DisplayName)
	writeJSON(w, 201, resp)
	return nil
}

func (s *Server) handleRedeemInvite(w http.ResponseWriter, r *http.Request) error {
	var req api.RedeemInviteRequest
	if err := decode(w, r, &req, 0); err != nil {
		return err
	}
	if err := s.verifyDevice(req.Device, req.Code); err != nil {
		return err
	}
	var resp api.JoinResponse
	recovered := false
	err := s.tx(r.Context(), func(tx *Tx) error {
		ctx := r.Context()
		var inviteID, teamID, roles string
		var admin bool
		err := tx.QueryRowContext(ctx, `SELECT id, team_id, roles, is_admin FROM invites
			WHERE code_hash = ? AND revoked_at IS NULL AND expires_at > ? AND uses < max_uses`,
			authn.HashCode(req.Code), ms(s.now())).Scan(&inviteID, &teamID, &roles, &admin)
		if isNoRows(err) {
			// Recovery codes are entered in the same form as invites.
			recovered = true
			resp, err = s.redeemRecoveryCode(ctx, tx, req.Code, req.Device)
			return err
		} else if err != nil {
			return err
		}
		if err := validateNewMember(&req.Member, false); err != nil {
			return err
		}
		m, deviceID, err := s.insertMemberAndDevice(ctx, tx, teamID, req.Member, parseRoles(roles), admin, req.Device)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE invites SET uses = uses + 1 WHERE id = ?`, inviteID); err != nil {
			return err
		}
		team, err := getTeam(ctx, tx, teamID)
		if err != nil {
			return err
		}
		resp = api.JoinResponse{Team: team, Member: m, DeviceID: deviceID}
		return tx.emit(teamID, "", "", api.EvMemberJoined, m.ID, m)
	})
	if err != nil {
		return err
	}
	if recovered {
		// The new device hasn't connected yet, so this only drops the old ones.
		s.hub.disconnectMember(resp.Member.ID)
		s.log.Info("member recovered", "team", resp.Team.Name, "member", resp.Member.DisplayName)
	}
	writeJSON(w, 201, resp)
	return nil
}

func (s *Server) handleChallenge(w http.ResponseWriter, r *http.Request) error {
	var req api.ChallengeRequest
	if err := decode(w, r, &req, 0); err != nil {
		return err
	}
	// Always issue a nonce, even for unknown devices, so device IDs can't be probed.
	nonce, exp := s.challenges.issue(req.DeviceID)
	writeJSON(w, 200, api.ChallengeResponse{Nonce: nonce, ExpiresAt: exp})
	return nil
}

func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) error {
	var req api.TokenRequest
	if err := decode(w, r, &req, 0); err != nil {
		return err
	}
	if !s.challenges.consume(req.Nonce, req.DeviceID) {
		return errf(401, "invalid_challenge", "challenge expired; request a new one")
	}
	var pubStr string
	var revoked, removed sql.NullInt64
	err := s.db.QueryRowContext(r.Context(), `SELECT d.public_key, d.revoked_at, m.removed_at FROM devices d
		JOIN members m ON m.id = d.member_id WHERE d.id = ?`, req.DeviceID).Scan(&pubStr, &revoked, &removed)
	if isNoRows(err) {
		return errf(401, "unknown_device", "this device is not registered")
	} else if err != nil {
		return err
	}
	if revoked.Valid || removed.Valid {
		return errf(401, "device_revoked", "this device's access has been revoked")
	}
	pub, err := authn.DecodePublicKey(pubStr)
	if err != nil {
		return err
	}
	if !authn.Verify(pub, authn.TokenMessage(s.serverID, req.DeviceID, req.Nonce), req.Signature) {
		return errf(401, "invalid_signature", "signature verification failed")
	}
	tok := authn.RandomToken("brd_", 32)
	now := s.now()
	exp := now.Add(s.cfg.TokenTTL)
	if _, err := s.db.ExecContext(r.Context(), `INSERT INTO sessions (token_hash, device_id, expires_at, created_at) VALUES (?, ?, ?, ?)`,
		authn.HashSecret(tok), req.DeviceID, ms(exp), ms(now)); err != nil {
		return err
	}
	_, _ = s.db.ExecContext(r.Context(), `UPDATE devices SET last_seen_at = ? WHERE id = ?`, ms(now), req.DeviceID)
	writeJSON(w, 200, api.TokenResponse{Token: tok, ExpiresAt: exp})
	return nil
}
