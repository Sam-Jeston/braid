package server

import (
	"database/sql"
	"net/http"
	"strings"
	"time"

	"github.com/sam-jeston/braid/internal/api"
	"github.com/sam-jeston/braid/internal/authn"
)

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	team, err := getTeam(r.Context(), s.db, a.teamID())
	if err != nil {
		return err
	}
	writeJSON(w, 200, api.Me{Member: a.member, Team: team, DeviceID: a.deviceID})
	return nil
}

func (s *Server) handleGetTeam(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	team, err := getTeam(r.Context(), s.db, a.teamID())
	if err != nil {
		return err
	}
	writeJSON(w, 200, team)
	return nil
}

func (s *Server) handlePatchTeam(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	var p api.TeamPatch
	if err := decode(w, r, &p, 2<<20); err != nil {
		return err
	}
	// Any member may curate the codebase brief and rules; structural settings are admin-only.
	if (p.Name != nil || p.RepoURL != nil || p.DefaultBranch != nil || p.Output != nil) && !a.member.IsAdmin {
		return errf(403, "forbidden", "only team admins can change team settings")
	}
	var team api.Team
	err := s.tx(r.Context(), func(tx *Tx) error {
		ctx := r.Context()
		var err error
		if team, err = getTeam(ctx, tx, a.teamID()); err != nil {
			return err
		}
		if p.Name != nil {
			if n := strings.TrimSpace(*p.Name); n == "" || len(n) > 80 {
				return errf(400, "invalid_team", "team name is required (max 80 characters)")
			}
			team.Name = strings.TrimSpace(*p.Name)
		}
		if p.RepoURL != nil {
			team.RepoURL = strings.TrimSpace(*p.RepoURL)
		}
		if p.DefaultBranch != nil {
			team.DefaultBranch = strings.TrimSpace(*p.DefaultBranch)
		}
		if p.Output != nil {
			if err := validateOutput(p.Output); err != nil {
				return err
			}
			team.Output = *p.Output
		}
		if p.CodebaseBrief != nil {
			team.CodebaseBrief = *p.CodebaseBrief
		}
		if p.Rules != nil {
			team.Rules = *p.Rules
		}
		if _, err := tx.ExecContext(ctx, `UPDATE teams SET name = ?, repo_url = ?, default_branch = ?, output_json = ?, codebase_brief = ?, rules = ? WHERE id = ?`,
			team.Name, team.RepoURL, team.DefaultBranch, mustJSON(team.Output), team.CodebaseBrief, team.Rules, team.ID); err != nil {
			return err
		}
		return tx.emit(team.ID, "", "", api.EvTeamUpdated, a.member.ID, team)
	})
	if err != nil {
		return err
	}
	writeJSON(w, 200, team)
	return nil
}

func (s *Server) handleListMembers(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	ms, err := listMembers(r.Context(), s.db, a.teamID())
	if err != nil {
		return err
	}
	writeJSON(w, 200, ms)
	return nil
}

func (s *Server) handlePatchMember(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	if err := requireAdmin(a); err != nil {
		return err
	}
	var p api.MemberPatch
	if err := decode(w, r, &p, 0); err != nil {
		return err
	}
	var m api.Member
	err := s.tx(r.Context(), func(tx *Tx) error {
		ctx := r.Context()
		var err error
		if m, err = getMember(ctx, tx, r.PathValue("id")); err != nil || m.TeamID != a.teamID() || m.RemovedAt != nil {
			return errNotFound
		}
		if p.Roles != nil {
			if err := validateRoles(p.Roles); err != nil {
				return err
			}
			m.Roles = p.Roles
		}
		if p.IsAdmin != nil {
			if !*p.IsAdmin && m.IsAdmin {
				if err := ensureAnotherAdmin(tx, r, a.teamID(), m.ID); err != nil {
					return err
				}
			}
			m.IsAdmin = *p.IsAdmin
		}
		if _, err := tx.ExecContext(ctx, `UPDATE members SET roles = ?, is_admin = ? WHERE id = ?`, mustJSON(m.Roles), m.IsAdmin, m.ID); err != nil {
			return err
		}
		return tx.emit(a.teamID(), "", "", api.EvMemberUpdated, a.member.ID, m)
	})
	if err != nil {
		return err
	}
	writeJSON(w, 200, m)
	return nil
}

func ensureAnotherAdmin(tx *Tx, r *http.Request, teamID, exceptID string) error {
	var n int
	if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM members WHERE team_id = ? AND is_admin = 1 AND removed_at IS NULL AND id != ?`,
		teamID, exceptID).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return errf(409, "last_admin", "a team needs at least one admin")
	}
	return nil
}

// handleRemoveMember removes a member (admins) or leaves the team (self).
func (s *Server) handleRemoveMember(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	id := r.PathValue("id")
	if id != a.member.ID && !a.member.IsAdmin {
		return errForbidden
	}
	err := s.tx(r.Context(), func(tx *Tx) error {
		ctx := r.Context()
		m, err := getMember(ctx, tx, id)
		if err != nil || m.TeamID != a.teamID() || m.RemovedAt != nil {
			return errNotFound
		}
		if m.IsAdmin {
			if err := ensureAnotherAdmin(tx, r, a.teamID(), m.ID); err != nil {
				return err
			}
		}
		now := s.now()
		if _, err := tx.ExecContext(ctx, `UPDATE members SET removed_at = ? WHERE id = ?`, ms(now), m.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE devices SET revoked_at = ? WHERE member_id = ? AND revoked_at IS NULL`, ms(now), m.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE device_id IN (SELECT id FROM devices WHERE member_id = ?)`, m.ID); err != nil {
			return err
		}
		m.RemovedAt = &now
		return tx.emit(a.teamID(), "", "", api.EvMemberRemoved, a.member.ID, m)
	})
	if err != nil {
		return err
	}
	s.hub.disconnectMember(id)
	w.WriteHeader(204)
	return nil
}

func (s *Server) handleListInvites(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	if err := requireAdmin(a); err != nil {
		return err
	}
	rows, err := s.db.QueryContext(r.Context(), `SELECT `+inviteCols+` FROM invites WHERE team_id = ? ORDER BY created_at DESC LIMIT 100`, a.teamID())
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []api.Invite{}
	for rows.Next() {
		in, err := scanInvite(rows)
		if err != nil {
			return err
		}
		out = append(out, in)
	}
	writeJSON(w, 200, out)
	return rows.Err()
}

func (s *Server) handleCreateInvite(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	if err := requireAdmin(a); err != nil {
		return err
	}
	var req api.CreateInviteRequest
	if err := decode(w, r, &req, 0); err != nil {
		return err
	}
	if err := validateRoles(req.Roles); err != nil {
		return err
	}
	if req.MaxUses <= 0 {
		req.MaxUses = 1
	}
	if req.MaxUses > 100 {
		req.MaxUses = 100
	}
	if req.ExpiresInHours <= 0 {
		req.ExpiresInHours = 72
	}
	if req.ExpiresInHours > 24*30 {
		req.ExpiresInHours = 24 * 30
	}
	code := authn.NewCode("BRD", 3)
	now := s.now()
	in := api.Invite{
		ID: authn.NewID("inv"), Roles: req.Roles, IsAdmin: req.IsAdmin, MaxUses: req.MaxUses,
		ExpiresAt: now.Add(time.Duration(req.ExpiresInHours) * time.Hour), CreatedBy: a.member.ID, CreatedAt: now,
		Hint: code[len(code)-4:],
	}
	_, err := s.db.ExecContext(r.Context(), `INSERT INTO invites (id, team_id, code_hash, roles, is_admin, max_uses, expires_at, created_by, created_at, hint)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ID, a.teamID(), authn.HashCode(code), mustJSON(in.Roles), in.IsAdmin, in.MaxUses, ms(in.ExpiresAt), in.CreatedBy, ms(now), in.Hint)
	if err != nil {
		return err
	}
	writeJSON(w, 201, api.CreateInviteResponse{Invite: in, Code: code})
	return nil
}

func (s *Server) handleRevokeInvite(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	if err := requireAdmin(a); err != nil {
		return err
	}
	res, err := s.db.ExecContext(r.Context(), `UPDATE invites SET revoked_at = ? WHERE id = ? AND team_id = ? AND revoked_at IS NULL`,
		ms(s.now()), r.PathValue("id"), a.teamID())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNotFound
	}
	w.WriteHeader(204)
	return nil
}

func (s *Server) handleListDevices(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	rows, err := s.db.QueryContext(r.Context(), `SELECT id, member_id, name, created_at, last_seen_at, revoked_at FROM devices WHERE member_id = ? ORDER BY created_at`, a.member.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []api.Device{}
	for rows.Next() {
		var d api.Device
		var created int64
		var seen, revoked sql.NullInt64
		if err := rows.Scan(&d.ID, &d.MemberID, &d.Name, &created, &seen, &revoked); err != nil {
			return err
		}
		d.CreatedAt = fromMs(created)
		d.LastSeenAt, d.RevokedAt = fromNullMs(seen), fromNullMs(revoked)
		out = append(out, d)
	}
	writeJSON(w, 200, out)
	return rows.Err()
}

func (s *Server) handleRevokeDevice(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	id := r.PathValue("id")
	err := s.tx(r.Context(), func(tx *Tx) error {
		res, err := tx.ExecContext(r.Context(), `UPDATE devices SET revoked_at = ? WHERE id = ? AND member_id = ? AND revoked_at IS NULL`, ms(s.now()), id, a.member.ID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return errNotFound
		}
		_, err = tx.ExecContext(r.Context(), `DELETE FROM sessions WHERE device_id = ?`, id)
		return err
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}
