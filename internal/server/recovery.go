package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sam-jeston/braid/internal/api"
	"github.com/sam-jeston/braid/internal/authn"
)

// RecoveryCode is a code from CreateRecoveryCode and the member it recovers.
type RecoveryCode struct {
	Code      string
	Team      api.Team
	Member    api.Member
	ExpiresAt time.Time
}

// CreateRecoveryCode creates a single-use code that signs a new device in as
// an existing member. It's for members who lost every device they had,
// usually a team's creator, who has no other admin to invite them back.
// Only someone with access to the server's database can create one, which
// stands in for the password the server doesn't have.
//
// team and member each match an ID or a name, ignoring case. Either can be
// empty when there's one to choose from: the server's only team, or the
// team's only admin. A new code replaces the member's unused ones. Errors are
// written for the braid-server recovery-code command.
func (s *Server) CreateRecoveryCode(ctx context.Context, team, member string, ttl time.Duration) (RecoveryCode, error) {
	var rc RecoveryCode
	err := s.tx(ctx, func(tx *Tx) error {
		var err error
		if rc.Team, err = pickTeam(ctx, tx, team); err != nil {
			return err
		}
		if rc.Member, err = pickMember(ctx, tx, rc.Team, member); err != nil {
			return err
		}
		now := s.now()
		rc.Code = authn.NewCode("RECOVER", 4)
		rc.ExpiresAt = now.Add(ttl)
		if _, err := tx.ExecContext(ctx, `DELETE FROM recovery_codes WHERE member_id = ? AND used_at IS NULL`, rc.Member.ID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO recovery_codes (id, member_id, code_hash, created_at, expires_at) VALUES (?, ?, ?, ?, ?)`,
			authn.NewID("rcv"), rc.Member.ID, authn.HashCode(rc.Code), ms(now), ms(rc.ExpiresAt))
		return err
	})
	return rc, err
}

// redeemRecoveryCode registers the device for the code's member and revokes
// the member's other devices, since a lost one may be in someone else's hands.
func (s *Server) redeemRecoveryCode(ctx context.Context, tx *Tx, code string, d api.DeviceRegistration) (api.JoinResponse, error) {
	var codeID, memberID string
	err := tx.QueryRowContext(ctx, `SELECT r.id, r.member_id FROM recovery_codes r JOIN members m ON m.id = r.member_id
		WHERE r.code_hash = ? AND r.used_at IS NULL AND r.expires_at > ? AND m.removed_at IS NULL`,
		authn.HashCode(code), ms(s.now())).Scan(&codeID, &memberID)
	if isNoRows(err) {
		return api.JoinResponse{}, errf(403, "invalid_code", "that invite or recovery code is invalid, used up or expired")
	} else if err != nil {
		return api.JoinResponse{}, err
	}
	now := ms(s.now())
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE device_id IN (SELECT id FROM devices WHERE member_id = ?)`, memberID); err != nil {
		return api.JoinResponse{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE devices SET revoked_at = ? WHERE member_id = ? AND revoked_at IS NULL`, now, memberID); err != nil {
		return api.JoinResponse{}, err
	}
	deviceID, err := s.insertDevice(ctx, tx, memberID, d)
	if err != nil {
		return api.JoinResponse{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE recovery_codes SET used_at = ?, device_id = ? WHERE id = ?`, now, deviceID, codeID); err != nil {
		return api.JoinResponse{}, err
	}
	m, err := getMember(ctx, tx, memberID)
	if err != nil {
		return api.JoinResponse{}, err
	}
	team, err := getTeam(ctx, tx, m.TeamID)
	if err != nil {
		return api.JoinResponse{}, err
	}
	return api.JoinResponse{Team: team, Member: m, DeviceID: deviceID}, nil
}

func pickTeam(ctx context.Context, q queryer, want string) (api.Team, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+teamCols+` FROM teams ORDER BY created_at`)
	if err != nil {
		return api.Team{}, err
	}
	var all, match []api.Team
	for rows.Next() {
		t, err := scanTeam(rows)
		if err != nil {
			rows.Close()
			return api.Team{}, err
		}
		all = append(all, t)
		if want == "" || t.ID == want || strings.EqualFold(t.Name, want) {
			match = append(match, t)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return api.Team{}, err
	}
	if len(all) == 0 {
		return api.Team{}, fmt.Errorf("this server has no teams yet. Create one with braid-server setup-code")
	}
	switch {
	case len(match) == 1:
		return match[0], nil
	case len(match) == 0:
		return api.Team{}, fmt.Errorf("no team is called %q. Choose one with --team: %s", want, teamList(all))
	case want == "":
		return api.Team{}, fmt.Errorf("this server has several teams. Choose one with --team: %s", teamList(all))
	default:
		return api.Team{}, fmt.Errorf("several teams are called %q. Choose one by ID with --team: %s", want, teamList(match))
	}
}

func pickMember(ctx context.Context, q queryer, team api.Team, want string) (api.Member, error) {
	members, err := listMembers(ctx, q, team.ID)
	if err != nil {
		return api.Member{}, err
	}
	var active, match []api.Member
	for _, m := range members {
		if m.RemovedAt != nil {
			continue
		}
		active = append(active, m)
		if want == "" && m.IsAdmin || want != "" && (m.ID == want || strings.EqualFold(m.DisplayName, want)) {
			match = append(match, m)
		}
	}
	switch {
	case len(match) == 1:
		return match[0], nil
	case len(active) == 0:
		return api.Member{}, fmt.Errorf("%s has no members left. Create a new team with braid-server setup-code", team.Name)
	case len(match) == 0 && want == "":
		return api.Member{}, fmt.Errorf("%s has no admins. Choose a member with --member: %s", team.Name, memberList(active))
	case len(match) == 0:
		return api.Member{}, fmt.Errorf("no member of %s is called %q. Choose one with --member: %s", team.Name, want, memberList(active))
	case want == "":
		return api.Member{}, fmt.Errorf("%s has several admins. Choose one with --member: %s", team.Name, memberList(match))
	default:
		return api.Member{}, fmt.Errorf("several members of %s are called %q. Choose one by ID with --member: %s", team.Name, want, memberList(match))
	}
}

func teamList(ts []api.Team) string {
	var parts []string
	for _, t := range ts {
		parts = append(parts, fmt.Sprintf("%s (%s)", t.Name, t.ID))
	}
	return strings.Join(parts, ", ")
}

func memberList(members []api.Member) string {
	var parts []string
	for _, m := range members {
		label := m.ID
		if m.IsAdmin {
			label += ", admin"
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", m.DisplayName, label))
	}
	return strings.Join(parts, ", ")
}
