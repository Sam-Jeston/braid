package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/sam-jeston/braid/internal/api"
	"github.com/sam-jeston/braid/internal/authn"
	"github.com/sam-jeston/braid/internal/workflow"
)

func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		kind = string(api.RunKindScan)
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.QueryContext(r.Context(), `SELECT `+runCols+` FROM runs WHERE team_id = ? AND kind = ? ORDER BY started_at DESC LIMIT ?`, a.teamID(), kind, limit)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []api.Run{}
	for rows.Next() {
		x, err := scanRun(rows)
		if err != nil {
			return err
		}
		out = append(out, x)
	}
	writeJSON(w, 200, out)
	return rows.Err()
}

func (s *Server) handleCreateRun(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	var req api.CreateRunRequest
	if err := decode(w, r, &req, 0); err != nil {
		return err
	}
	req.Agent = strings.TrimSpace(req.Agent)
	if req.Agent == "" || len(req.Agent) > 40 || len(req.AgentVersion) > 120 {
		return errf(400, "invalid_agent", "agent name is required")
	}
	if req.Kind == "" {
		req.Kind = api.RunKindStage
	}
	now := s.now()
	run := api.Run{
		ID: authn.NewID("run"), TeamID: a.teamID(), Kind: req.Kind, MemberID: a.member.ID, Agent: req.Agent,
		AgentVersion: req.AgentVersion, Status: api.RunRunning, StartedAt: now, HeartbeatAt: now,
	}
	err := s.tx(r.Context(), func(tx *Tx) error {
		ctx := r.Context()
		var thread *api.Thread
		switch req.Kind {
		case api.RunKindScan:
			var busy string
			err := tx.QueryRowContext(ctx, `SELECT member_id FROM runs WHERE team_id = ? AND kind = ? AND status = ?`, a.teamID(), api.RunKindScan, api.RunRunning).Scan(&busy)
			if err == nil {
				return errf(409, "run_active", "a codebase scan is already running")
			} else if !isNoRows(err) {
				return err
			}
		case api.RunKindStage:
			t, err := getThread(ctx, tx, a.teamID(), req.ThreadID)
			if err != nil {
				return err
			}
			if t.Status != api.ThreadActive {
				return errf(409, "thread_closed", "this thread is %s", t.Status)
			}
			st := t.StageState(req.Stage)
			if st == nil {
				return errf(400, "invalid_stage", "unknown stage %q", req.Stage)
			}
			if st.Status != api.StageOpen {
				return errf(409, "stage_not_open", "agents can only run in open stages (this one is %s)", st.Status)
			}
			if st.ActiveRunID != "" {
				other, err := getRun(ctx, tx, a.teamID(), st.ActiveRunID)
				if err == nil && other.Status == api.RunRunning {
					who := "a teammate"
					if m, err := getMember(ctx, tx, other.MemberID); err == nil {
						who = m.DisplayName
					}
					return errf(409, "run_active", "%s's agent is already working on this stage", who)
				}
			}
			if req.TriggerMessageID != "" {
				var n int
				if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE id = ? AND thread_id = ?`, req.TriggerMessageID, t.ID).Scan(&n); err != nil {
					return err
				}
				if n == 0 {
					return errf(400, "invalid_trigger", "trigger message not found in this thread")
				}
			}
			run.ThreadID, run.Stage, run.TriggerMessageID = t.ID, req.Stage, req.TriggerMessageID
			st.ActiveRunID = run.ID
			t.UpdatedAt = now
			if err := saveStages(ctx, tx, &t); err != nil {
				return err
			}
			thread = &t
		default:
			return errf(400, "invalid_kind", "unknown run kind %q", req.Kind)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO runs (id, team_id, thread_id, stage, kind, member_id, agent, agent_version, status, trigger_message_id, started_at, heartbeat_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			run.ID, run.TeamID, nullStr(run.ThreadID), run.Stage, run.Kind, run.MemberID, run.Agent, run.AgentVersion, run.Status,
			nullStr(run.TriggerMessageID), ms(now), ms(now)); err != nil {
			return err
		}
		if err := tx.emit(run.TeamID, run.ThreadID, run.ID, api.EvRunStarted, a.member.ID, run); err != nil {
			return err
		}
		if thread != nil {
			return tx.emit(thread.TeamID, thread.ID, "", api.EvThreadUpdated, a.member.ID, *thread)
		}
		return nil
	})
	if err != nil {
		return err
	}
	writeJSON(w, 201, run)
	return nil
}

func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	x, err := getRun(r.Context(), s.db, a.teamID(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, 200, x)
	return nil
}

func (s *Server) handleGetRunOutput(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	ctx := r.Context()
	if _, err := getRun(ctx, s.db, a.teamID(), r.PathValue("id")); err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT seq, data FROM events WHERE run_id = ? AND type = ? ORDER BY seq`, r.PathValue("id"), api.EvRunOutput)
	if err != nil {
		return err
	}
	defer rows.Close()
	resp := api.RunOutputResponse{Items: []api.RunOutput{}}
	for rows.Next() {
		var data string
		if err := rows.Scan(&resp.Seq, &data); err != nil {
			return err
		}
		var b api.RunOutputBatch
		if json.Unmarshal([]byte(data), &b) == nil {
			resp.Items = append(resp.Items, b.Items...)
		}
	}
	writeJSON(w, 200, resp)
	return rows.Err()
}

// handleRunOutput appends streamed agent activity; an empty batch is a heartbeat.
func (s *Server) handleRunOutput(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	var batch api.RunOutputBatch
	if err := decode(w, r, &batch, 2<<20); err != nil {
		return err
	}
	if len(batch.Items) > 1000 {
		return errf(400, "too_large", "too many output items in one batch")
	}
	for i := range batch.Items {
		if len(batch.Items[i].Text) > 64<<10 {
			batch.Items[i].Text = batch.Items[i].Text[:64<<10]
		}
		if len(batch.Items[i].Detail) > 4<<10 {
			batch.Items[i].Detail = batch.Items[i].Detail[:4<<10]
		}
	}
	err := s.tx(r.Context(), func(tx *Tx) error {
		ctx := r.Context()
		run, err := getRun(ctx, tx, a.teamID(), r.PathValue("id"))
		if err != nil {
			return err
		}
		if run.MemberID != a.member.ID {
			return errForbidden
		}
		if run.Status != api.RunRunning {
			return errf(409, "run_finished", "run is %s", run.Status)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET heartbeat_at = ? WHERE id = ?`, ms(s.now()), run.ID); err != nil {
			return err
		}
		if len(batch.Items) == 0 {
			return nil
		}
		batch.RunID = run.ID
		return tx.emit(run.TeamID, run.ThreadID, run.ID, api.EvRunOutput, a.member.ID, batch)
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}

func (s *Server) handleFinishRun(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	var req api.FinishRunRequest
	if err := decode(w, r, &req, int64(workflow.MaxArtifactsPer+1)*workflow.MaxArtifactBytes); err != nil {
		return err
	}
	switch req.Status {
	case api.RunSucceeded, api.RunFailed, api.RunCancelled:
	default:
		return errf(400, "invalid_status", "status must be succeeded, failed or cancelled")
	}
	if len(req.Artifacts) > workflow.MaxArtifactsPer {
		return errf(400, "too_many_artifacts", "at most %d documents per run", workflow.MaxArtifactsPer)
	}
	if len(req.Reply) > 64<<10 {
		req.Reply = req.Reply[:64<<10]
	}
	if len(req.Error) > 4<<10 {
		req.Error = req.Error[:4<<10]
	}
	var run api.Run
	err := s.tx(r.Context(), func(tx *Tx) error {
		ctx := r.Context()
		var err error
		if run, err = getRun(ctx, tx, a.teamID(), r.PathValue("id")); err != nil {
			return err
		}
		if run.MemberID != a.member.ID {
			return errForbidden
		}
		if run.Status != api.RunRunning {
			return errf(409, "run_finished", "run is already %s", run.Status)
		}
		for _, art := range req.Artifacts {
			if len(art.Content) > workflow.MaxArtifactBytes {
				return errf(400, "too_large", "%s is larger than 1 MB", art.Path)
			}
			valid := false
			if run.Kind == api.RunKindScan {
				valid = workflow.ValidScanPath(art.Path)
			} else {
				valid = workflow.ValidArtifactPath(run.Stage, art.Path)
			}
			if !valid {
				return errf(400, "invalid_path", "%s is not a valid document for this run", art.Path)
			}
			if art.Path == "plan.json" && art.Content != "" {
				if _, err := workflow.ParsePlan(art.Content); err != nil {
					return errf(400, "invalid_plan", "%v", err)
				}
			}
		}
		now := s.now()
		run.Status, run.Reply, run.Error, run.CostUSD, run.FinishedAt = req.Status, req.Reply, req.Error, req.CostUSD, &now
		if req.CommitSHA != "" {
			run.CommitSHA = req.CommitSHA
		}
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET status = ?, reply = ?, error = ?, cost_usd = ?, commit_sha = ?, finished_at = ?, heartbeat_at = ? WHERE id = ?`,
			run.Status, run.Reply, run.Error, run.CostUSD, run.CommitSHA, ms(now), ms(now), run.ID); err != nil {
			return err
		}
		switch run.Kind {
		case api.RunKindStage:
			if err := s.finishStageRun(ctx, tx, a, run, req); err != nil {
				return err
			}
		case api.RunKindScan:
			if err := s.finishScanRun(ctx, tx, a, run, req); err != nil {
				return err
			}
		}
		return tx.emit(run.TeamID, run.ThreadID, run.ID, api.EvRunFinished, a.member.ID, run)
	})
	if err != nil {
		return err
	}
	writeJSON(w, 200, run)
	return nil
}

func (s *Server) finishStageRun(ctx context.Context, tx *Tx, a *authCtx, run api.Run, req api.FinishRunRequest) error {
	t, err := getThread(ctx, tx, a.teamID(), run.ThreadID)
	if err != nil {
		return err
	}
	st := t.StageState(run.Stage)
	for _, up := range req.Artifacts {
		art, err := insertArtifact(ctx, tx, t.ID, run.Stage, up.Path, up.Content, run.ID, run.MemberID)
		if err != nil {
			return err
		}
		if up.Content == "" && st.Selection == up.Path {
			st.Selection = ""
		}
		if err := tx.emit(t.TeamID, t.ID, run.ID, api.EvArtifactUpdated, a.member.ID, art); err != nil {
			return err
		}
	}
	if strings.TrimSpace(req.Reply) != "" {
		if _, err := s.insertMessage(ctx, tx, &t, run.Stage, run.MemberID, api.MessageAgent, strings.TrimSpace(req.Reply), run.ID); err != nil {
			return err
		}
	}
	if st.ActiveRunID == run.ID {
		st.ActiveRunID = ""
	}
	t.UpdatedAt = s.now()
	if err := saveStages(ctx, tx, &t); err != nil {
		return err
	}
	return tx.emit(t.TeamID, t.ID, "", api.EvThreadUpdated, a.member.ID, t)
}

func (s *Server) finishScanRun(ctx context.Context, tx *Tx, a *authCtx, run api.Run, req api.FinishRunRequest) error {
	if run.Status != api.RunSucceeded || len(req.Artifacts) == 0 {
		return nil
	}
	team, err := getTeam(ctx, tx, a.teamID())
	if err != nil {
		return err
	}
	for _, art := range req.Artifacts {
		switch art.Path {
		case "codebase-brief.md":
			if strings.TrimSpace(art.Content) != "" {
				team.CodebaseBrief = art.Content
			}
		case "rules.md":
			if strings.TrimSpace(art.Content) != "" {
				team.Rules = art.Content
			}
		}
	}
	now := s.now()
	team.ScanCommit, team.ScannedAt = run.CommitSHA, &now
	if _, err := tx.ExecContext(ctx, `UPDATE teams SET codebase_brief = ?, rules = ?, scan_commit = ?, scanned_at = ? WHERE id = ?`,
		team.CodebaseBrief, team.Rules, team.ScanCommit, ms(now), team.ID); err != nil {
		return err
	}
	return tx.emit(team.ID, "", run.ID, api.EvTeamUpdated, a.member.ID, team)
}

func (s *Server) handleCancelRun(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	err := s.tx(r.Context(), func(tx *Tx) error {
		run, err := getRun(r.Context(), tx, a.teamID(), r.PathValue("id"))
		if err != nil {
			return err
		}
		if run.Status != api.RunRunning {
			return errf(409, "run_finished", "run is already %s", run.Status)
		}
		return tx.emit(run.TeamID, run.ThreadID, run.ID, api.EvRunCancel, a.member.ID, api.CancelRequested{RunID: run.ID, By: a.member.ID})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(202)
	return nil
}

// reapRuns marks runs whose machine stopped heartbeating as abandoned and
// releases their stage lock.
func (s *Server) reapRuns(ctx context.Context) error {
	cutoff := ms(s.now().Add(-s.cfg.RunTimeout))
	rows, err := s.db.QueryContext(ctx, `SELECT id, team_id FROM runs WHERE status = ? AND heartbeat_at < ?`, api.RunRunning, cutoff)
	if err != nil {
		return err
	}
	type ref struct{ id, team string }
	var stale []ref
	for rows.Next() {
		var x ref
		if err := rows.Scan(&x.id, &x.team); err != nil {
			rows.Close()
			return err
		}
		stale = append(stale, x)
	}
	rows.Close()
	for _, x := range stale {
		err := s.tx(ctx, func(tx *Tx) error {
			run, err := getRun(ctx, tx, x.team, x.id)
			if err != nil || run.Status != api.RunRunning {
				return err
			}
			now := s.now()
			run.Status, run.FinishedAt, run.Error = api.RunAbandoned, &now, "the machine running this agent stopped responding"
			if _, err := tx.ExecContext(ctx, `UPDATE runs SET status = ?, error = ?, finished_at = ? WHERE id = ?`, run.Status, run.Error, ms(now), run.ID); err != nil {
				return err
			}
			if run.ThreadID != "" {
				t, err := getThread(ctx, tx, run.TeamID, run.ThreadID)
				if err != nil {
					return err
				}
				if st := t.StageState(run.Stage); st != nil && st.ActiveRunID == run.ID {
					st.ActiveRunID = ""
					if err := saveStages(ctx, tx, &t); err != nil {
						return err
					}
					if err := tx.emit(t.TeamID, t.ID, "", api.EvThreadUpdated, "", t); err != nil {
						return err
					}
				}
			}
			return tx.emit(run.TeamID, run.ThreadID, run.ID, api.EvRunFinished, "", run)
		})
		if err != nil {
			return err
		}
		s.log.Info("run abandoned", "run", x.id)
	}
	return nil
}
