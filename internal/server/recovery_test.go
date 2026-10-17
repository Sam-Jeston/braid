package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sam-jeston/braid/internal/api"
)

func TestRecoveryCode(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	admin := f.createTeam("Acme", api.RoleProduct)
	eng := f.join(admin, "Eli", api.RoleEngineering)
	th := f.thread(admin, "Onboarding revamp")

	// With one team and one admin, there's nothing to choose.
	rc, err := f.s.CreateRecoveryCode(ctx, "", "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if rc.Member.ID != admin.ID || rc.Team.ID != admin.TeamID || !strings.HasPrefix(rc.Code, "RECOVER-") || !rc.ExpiresAt.Equal(f.now.Add(time.Hour)) {
		t.Fatalf("recovery code = %+v", rc)
	}

	// The new device signs in as the same member, whatever name it gives.
	back, status := f.redeem(strings.ToLower(rc.Code), "")
	if status != 201 || back.ID != admin.ID || !back.IsAdmin || back.DisplayName != "Acme admin" {
		t.Fatalf("redeem: %d %+v", status, back.Member)
	}
	var members []api.Member
	f.must(200, back, "GET", "/v1/members", nil, &members)
	if len(members) != 2 {
		t.Errorf("recovery added a member: %+v", members)
	}
	var d api.ThreadDetail
	f.must(200, back, "GET", "/v1/threads/"+th.Thread.ID, nil, &d)
	if d.Thread.CreatedBy != admin.ID {
		t.Errorf("thread created by %s", d.Thread.CreatedBy)
	}

	// The lost device is signed out for good, and other members aren't touched.
	if s := f.call(admin, "GET", "/v1/me", nil, nil); s != 401 {
		t.Errorf("old device's token still works: %d", s)
	}
	if s, _ := login(t, f.s, f.hs, admin.DeviceID, admin.key); s != 401 {
		t.Errorf("old device logged in: %d", s)
	}
	var devices []api.Device
	f.must(200, back, "GET", "/v1/devices", nil, &devices)
	for _, dev := range devices {
		if (dev.RevokedAt == nil) != (dev.ID == back.DeviceID) {
			t.Errorf("device %s revoked at %v", dev.ID, dev.RevokedAt)
		}
	}
	if len(devices) != 2 {
		t.Errorf("devices = %+v", devices)
	}
	f.must(200, eng, "GET", "/v1/me", nil, nil)

	// Codes work once.
	if _, status := f.redeem(rc.Code, ""); status != 403 {
		t.Errorf("recovery code redeemed twice: %d", status)
	}

	// A new code replaces the member's unused one, and codes expire.
	first, err := f.s.CreateRecoveryCode(ctx, "acme", "eli", time.Hour)
	if err != nil || first.Member.ID != eng.ID {
		t.Fatalf("by name: %+v %v", first.Member, err)
	}
	second, err := f.s.CreateRecoveryCode(ctx, admin.TeamID, eng.ID, time.Hour)
	if err != nil || second.Member.ID != eng.ID {
		t.Fatalf("by ID: %+v %v", second.Member, err)
	}
	if _, status := f.redeem(first.Code, ""); status != 403 {
		t.Errorf("replaced code redeemed: %d", status)
	}
	f.advance(2 * time.Hour)
	if _, status := f.redeem(second.Code, ""); status != 403 {
		t.Errorf("expired code redeemed: %d", status)
	}

	// Removed members can't be recovered.
	f.login(&back)
	third, err := f.s.CreateRecoveryCode(ctx, "", "Eli", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	f.must(204, back, "DELETE", "/v1/members/"+eng.ID, nil, nil)
	if _, status := f.redeem(third.Code, ""); status != 403 {
		t.Errorf("removed member recovered: %d", status)
	}
	if _, err := f.s.CreateRecoveryCode(ctx, "", "Eli", time.Hour); err == nil || !strings.Contains(err.Error(), `no member of Acme is called "Eli"`) {
		t.Errorf("code for a removed member: %v", err)
	}

	// Invites still need a name.
	if _, status := f.redeem(f.invite(back, api.CreateInviteRequest{Roles: []api.Role{api.RoleDesign}}), ""); status != 400 {
		t.Errorf("invite redeemed without a name: %d", status)
	}
}

func TestRecoveryCodeChoices(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	pick := func(team, member string) (RecoveryCode, string) {
		t.Helper()
		rc, err := f.s.CreateRecoveryCode(ctx, team, member, time.Hour)
		if err != nil {
			return rc, err.Error()
		}
		return rc, ""
	}

	if _, msg := pick("", ""); !strings.Contains(msg, "no teams yet") {
		t.Errorf("no teams: %q", msg)
	}
	acme := f.createTeam("Acme", api.RoleProduct)
	beta := f.createTeam("Beta", api.RoleProduct)
	if _, msg := pick("", ""); !strings.Contains(msg, "several teams. Choose one with --team: Acme ("+acme.TeamID+"), Beta ("+beta.TeamID+")") {
		t.Errorf("several teams: %q", msg)
	}
	if _, msg := pick("Gamma", ""); !strings.Contains(msg, `no team is called "Gamma"`) {
		t.Errorf("unknown team: %q", msg)
	}
	if rc, msg := pick("beta", ""); msg != "" || rc.Member.ID != beta.ID {
		t.Errorf("by team name: %q %+v", msg, rc.Member)
	}
	other := f.createTeam("Acme", api.RoleProduct)
	if _, msg := pick("Acme", ""); !strings.Contains(msg, `several teams are called "Acme". Choose one by ID`) {
		t.Errorf("same team names: %q", msg)
	}
	if rc, msg := pick(other.TeamID, ""); msg != "" || rc.Member.ID != other.ID {
		t.Errorf("by team ID: %q %+v", msg, rc.Member)
	}

	// Several admins, or several members with one name, need a choice.
	sam := f.join(acme, "Sam", api.RoleDesign)
	f.must(200, acme, "PATCH", "/v1/members/"+sam.ID, map[string]any{"is_admin": true}, nil)
	if _, msg := pick(acme.TeamID, ""); !strings.Contains(msg, "Acme has several admins. Choose one with --member: Acme admin ("+acme.ID+", admin), Sam ("+sam.ID+", admin)") {
		t.Errorf("several admins: %q", msg)
	}
	if rc, msg := pick(acme.TeamID, "SAM"); msg != "" || rc.Member.ID != sam.ID {
		t.Errorf("by member name: %q %+v", msg, rc.Member)
	}
	sam2 := f.join(acme, "Sam", api.RoleEngineering)
	if _, msg := pick(acme.TeamID, "Sam"); !strings.Contains(msg, `several members of Acme are called "Sam"`) {
		t.Errorf("same member names: %q", msg)
	}
	if rc, msg := pick(acme.TeamID, sam2.ID); msg != "" || rc.Member.ID != sam2.ID {
		t.Errorf("by member ID: %q %+v", msg, rc.Member)
	}
}
