package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/sam-jeston/braid/internal/api"
	"github.com/sam-jeston/braid/internal/authn"
	"github.com/sam-jeston/braid/internal/workflow"
)

func (s *Server) handleListThreads(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	ctx := r.Context()
	rows, err := s.db.QueryContext(ctx, `SELECT `+threadCols+` FROM threads WHERE team_id = ? ORDER BY updated_at DESC LIMIT 500`, a.teamID())
	if err != nil {
		return err
	}
	threads := []api.Thread{}
	var ids []string
	for rows.Next() {
		t, err := scanThread(rows)
		if err != nil {
			rows.Close()
			return err
		}
		threads = append(threads, t)
		ids = append(ids, t.ID)
	}
	rows.Close()
	stages, err := loadStages(ctx, s.db, ids)
	if err != nil {
		return err
	}
	for i := range threads {
		threads[i].Stages = stages[threads[i].ID]
	}
	writeJSON(w, 200, threads)
	return nil
}

func (s *Server) handleCreateThread(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	var req api.CreateThreadRequest
	if err := decode(w, r, &req, 0); err != nil {
		return err
	}
	req.Title = strings.TrimSpace(req.Title)
	req.Body = strings.TrimSpace(req.Body)
	if req.Title == "" || len(req.Title) > 140 {
		return errf(400, "invalid_title", "a title is required (max 140 characters)")
	}
	if req.Body == "" {
		return errf(400, "invalid_body", "describe the idea to start sparring")
	}
	var id string
	err := s.tx(r.Context(), func(tx *Tx) error {
		ctx := r.Context()
		now := s.now()
		t := api.Thread{
			ID: authn.NewID("thr"), TeamID: a.teamID(), Title: req.Title, Status: api.ThreadActive,
			CurrentStage: api.StageSparring, CreatedBy: a.member.ID, CreatedAt: now, UpdatedAt: now,
			Stages: workflow.InitialStages(),
		}
		id = t.ID
		if _, err := tx.ExecContext(ctx, `INSERT INTO threads (id, team_id, title, status, current_stage, created_by, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			t.ID, t.TeamID, t.Title, t.Status, t.CurrentStage, t.CreatedBy, ms(now), ms(now)); err != nil {
			return err
		}
		for i, st := range t.Stages {
			if _, err := tx.ExecContext(ctx, `INSERT INTO thread_stages (thread_id, stage, position, status) VALUES (?, ?, ?, ?)`,
				t.ID, st.Stage, i, st.Status); err != nil {
				return err
			}
		}
		if err := tx.emit(t.TeamID, t.ID, "", api.EvThreadCreated, a.member.ID, t); err != nil {
			return err
		}
		_, err := s.insertMessage(ctx, tx, &t, api.StageSparring, a.member.ID, api.MessageHuman, req.Body, "")
		return err
	})
	if err != nil {
		return err
	}
	detail, err := s.threadDetail(r.Context(), a.teamID(), id)
	if err != nil {
		return err
	}
	writeJSON(w, 201, detail)
	return nil
}

func (s *Server) threadDetail(ctx context.Context, teamID, id string) (api.ThreadDetail, error) {
	var d api.ThreadDetail
	err := s.tx(ctx, func(tx *Tx) error {
		var err error
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM events WHERE team_id = ?`, teamID).Scan(&d.Seq); err != nil {
			return err
		}
		if d.Thread, err = getThread(ctx, tx, teamID, id); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT `+messageCols+` FROM messages WHERE thread_id = ? ORDER BY created_at, rowid`, id)
		if err != nil {
			return err
		}
		d.Messages = []api.Message{}
		for rows.Next() {
			m, err := scanMessage(rows)
			if err != nil {
				rows.Close()
				return err
			}
			d.Messages = append(d.Messages, m)
		}
		rows.Close()
		rows, err = tx.QueryContext(ctx, `SELECT `+runCols+` FROM runs WHERE thread_id = ? ORDER BY started_at`, id)
		if err != nil {
			return err
		}
		d.Runs = []api.Run{}
		for rows.Next() {
			x, err := scanRun(rows)
			if err != nil {
				rows.Close()
				return err
			}
			d.Runs = append(d.Runs, x)
		}
		rows.Close()
		if d.Artifacts, err = latestArtifacts(ctx, tx, id); err != nil {
			return err
		}
		rows, err = tx.QueryContext(ctx, `SELECT id, thread_id, target, member_id, items, created_at FROM exports WHERE thread_id = ? ORDER BY created_at`, id)
		if err != nil {
			return err
		}
		d.Exports = []api.Export{}
		for rows.Next() {
			e, err := scanExport(rows)
			if err != nil {
				rows.Close()
				return err
			}
			d.Exports = append(d.Exports, e)
		}
		rows.Close()
		return nil
	})
	return d, err
}

func (s *Server) handleGetThread(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	d, err := s.threadDetail(r.Context(), a.teamID(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, 200, d)
	return nil
}

// mutateThread loads a thread inside a transaction, lets fn modify it, then
// persists the stage state and emits thread.updated.
func (s *Server) mutateThread(ctx context.Context, a *authCtx, id string, fn func(tx *Tx, t *api.Thread) error) (api.Thread, error) {
	var t api.Thread
	err := s.tx(ctx, func(tx *Tx) error {
		var err error
		if t, err = getThread(ctx, tx, a.teamID(), id); err != nil {
			return err
		}
		if err := fn(tx, &t); err != nil {
			return err
		}
		t.UpdatedAt = s.now()
		if err := saveStages(ctx, tx, &t); err != nil {
			return err
		}
		return tx.emit(t.TeamID, t.ID, "", api.EvThreadUpdated, a.member.ID, t)
	})
	return t, err
}

func (s *Server) handlePatchThread(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	var p api.ThreadPatch
	if err := decode(w, r, &p, 0); err != nil {
		return err
	}
	t, err := s.mutateThread(r.Context(), a, r.PathValue("id"), func(tx *Tx, t *api.Thread) error {
		if p.Title != nil {
			title := strings.TrimSpace(*p.Title)
			if title == "" || len(title) > 140 {
				return errf(400, "invalid_title", "a title is required (max 140 characters)")
			}
			t.Title = title
		}
		if p.Status != nil {
			switch *p.Status {
			case api.ThreadArchived:
				t.Status = api.ThreadArchived
			case api.ThreadActive:
				if t.Status == api.ThreadArchived {
					t.Status = api.ThreadActive
					if st := t.StageState(api.StagePlan); st != nil && st.Status == api.StageApproved {
						t.Status = api.ThreadCompleted
					}
				}
			default:
				return errf(400, "invalid_status", "status must be active or archived")
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	writeJSON(w, 200, t)
	return nil
}

func (s *Server) insertMessage(ctx context.Context, tx *Tx, t *api.Thread, stage api.Stage, authorID, kind, body, runID string) (api.Message, error) {
	m := api.Message{
		ID: authn.NewID("msg"), ThreadID: t.ID, Stage: stage, AuthorID: authorID, Kind: kind, Body: body, RunID: runID, CreatedAt: s.now(),
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO messages (`+messageCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.ThreadID, m.Stage, m.AuthorID, m.Kind, m.Body, nullStr(m.RunID), ms(m.CreatedAt)); err != nil {
		return m, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE threads SET updated_at = ? WHERE id = ?`, ms(m.CreatedAt), t.ID); err != nil {
		return m, err
	}
	return m, tx.emit(t.TeamID, t.ID, "", api.EvMessageCreated, authorID, m)
}

func (s *Server) handleCreateMessage(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	var req api.CreateMessageRequest
	if err := decode(w, r, &req, 256<<10); err != nil {
		return err
	}
	req.Body = strings.TrimSpace(req.Body)
	if req.Body == "" {
		return errf(400, "invalid_body", "message is empty")
	}
	var m api.Message
	err := s.tx(r.Context(), func(tx *Tx) error {
		ctx := r.Context()
		t, err := getThread(ctx, tx, a.teamID(), r.PathValue("id"))
		if err != nil {
			return err
		}
		st := t.StageState(req.Stage)
		if st == nil {
			return errf(400, "invalid_stage", "unknown stage %q", req.Stage)
		}
		if st.Status == api.StageLocked {
			return errf(409, "stage_locked", "that stage hasn't started yet")
		}
		m, err = s.insertMessage(ctx, tx, &t, req.Stage, a.member.ID, api.MessageHuman, req.Body, "")
		return err
	})
	if err != nil {
		return err
	}
	writeJSON(w, 201, m)
	return nil
}

func hasPrimary(ctx context.Context, tx *Tx, threadID string, d workflow.StageDef) (bool, error) {
	arts, err := latestArtifacts(ctx, tx, threadID)
	if err != nil {
		return false, err
	}
	for _, a := range arts {
		if a.Stage == d.ID && (a.Path == d.Primary || (d.ID == api.StageDesign && workflow.IsMockup(a.Path))) {
			return true, nil
		}
	}
	return false, nil
}

func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	stage := api.Stage(r.PathValue("stage"))
	def, ok := workflow.Def(stage)
	if !ok || def.Owner == "" {
		return errf(400, "invalid_stage", "stage %q can't be approved", stage)
	}
	if !api.HasRole(a.member.Roles, def.Owner) {
		return errf(403, "forbidden", "only members with the %s role can approve %s", def.Owner, def.Title)
	}
	t, err := s.mutateThread(r.Context(), a, r.PathValue("id"), func(tx *Tx, t *api.Thread) error {
		ctx := r.Context()
		if t.Status != api.ThreadActive {
			return errf(409, "thread_closed", "this thread is %s", t.Status)
		}
		st := t.StageState(stage)
		if st.Status != api.StageOpen {
			return errf(409, "stage_not_open", "%s is %s", def.Title, st.Status)
		}
		if st.ActiveRunID != "" {
			return errf(409, "run_active", "wait for the running agent to finish before approving")
		}
		ok, err := hasPrimary(ctx, tx, t.ID, def)
		if err != nil {
			return err
		}
		if !ok {
			return errf(409, "missing_document", "%s needs %s before it can be approved", def.Title, def.Primary)
		}
		if stage == api.StageDesign && st.Selection == "" {
			arts, err := latestArtifacts(ctx, tx, t.ID)
			if err != nil {
				return err
			}
			for _, x := range arts {
				if x.Stage == api.StageDesign && workflow.IsMockup(x.Path) {
					return errf(409, "no_selection", "select a design variation before approving")
				}
			}
		}
		now := s.now()
		st.Status, st.ApprovedBy, st.ApprovedAt = api.StageApproved, a.member.ID, &now
		if next, ok := workflow.Next(stage); ok {
			if ns := t.StageState(next); ns.Status == api.StageLocked {
				ns.Status = api.StageOpen
			}
			t.CurrentStage = next
		}
		_, err = s.insertMessage(ctx, tx, t, stage, a.member.ID, api.MessageSystem, "approved "+def.Title, "")
		return err
	})
	if err != nil {
		return err
	}
	writeJSON(w, 200, t)
	return nil
}

func (s *Server) handleReopen(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	stage := api.Stage(r.PathValue("stage"))
	def, ok := workflow.Def(stage)
	if !ok || def.Owner == "" {
		return errf(400, "invalid_stage", "stage %q can't be reopened", stage)
	}
	if !api.HasRole(a.member.Roles, def.Owner) {
		return errf(403, "forbidden", "only members with the %s role can reopen %s", def.Owner, def.Title)
	}
	t, err := s.mutateThread(r.Context(), a, r.PathValue("id"), func(tx *Tx, t *api.Thread) error {
		if t.Status != api.ThreadActive {
			return errf(409, "thread_closed", "this thread is %s", t.Status)
		}
		st := t.StageState(stage)
		if st.Status != api.StageApproved {
			return errf(409, "stage_not_approved", "%s is not approved", def.Title)
		}
		idx := workflow.Index(stage)
		for i := idx + 1; i < len(t.Stages); i++ {
			if t.Stages[i].ActiveRunID != "" {
				return errf(409, "run_active", "an agent is running in a later stage; wait for it to finish")
			}
		}
		st.Status, st.ApprovedBy, st.ApprovedAt = api.StageOpen, "", nil
		for i := idx + 1; i < len(t.Stages); i++ {
			t.Stages[i].Status, t.Stages[i].ApprovedBy, t.Stages[i].ApprovedAt = api.StageLocked, "", nil
		}
		t.CurrentStage = stage
		_, err := s.insertMessage(r.Context(), tx, t, stage, a.member.ID, api.MessageSystem, "reopened "+def.Title, "")
		return err
	})
	if err != nil {
		return err
	}
	writeJSON(w, 200, t)
	return nil
}

func (s *Server) handleSelection(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	stage := api.Stage(r.PathValue("stage"))
	if stage != api.StageDesign {
		return errf(400, "invalid_stage", "only the design stage has a selection")
	}
	var req api.SelectionRequest
	if err := decode(w, r, &req, 0); err != nil {
		return err
	}
	if !api.HasRole(a.member.Roles, api.RoleDesign) {
		return errf(403, "forbidden", "only designers can select the design direction")
	}
	t, err := s.mutateThread(r.Context(), a, r.PathValue("id"), func(tx *Tx, t *api.Thread) error {
		st := t.StageState(stage)
		if st.Status != api.StageOpen {
			return errf(409, "stage_not_open", "design is %s", st.Status)
		}
		if req.Path != "" {
			arts, err := latestArtifacts(r.Context(), tx, t.ID)
			if err != nil {
				return err
			}
			found := false
			for _, x := range arts {
				if x.Stage == stage && x.Path == req.Path && workflow.IsMockup(x.Path) {
					found = true
				}
			}
			if !found {
				return errf(400, "invalid_selection", "no mockup at %s", req.Path)
			}
		}
		st.Selection = req.Path
		return nil
	})
	if err != nil {
		return err
	}
	writeJSON(w, 200, t)
	return nil
}

// handleSaveArtifact stores a human edit of a stage document.
func (s *Server) handleSaveArtifact(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	var req api.SaveArtifactRequest
	if err := decode(w, r, &req, workflow.MaxArtifactBytes+4096); err != nil {
		return err
	}
	if !workflow.ValidArtifactPath(req.Stage, req.Path) {
		return errf(400, "invalid_path", "%s can't be stored in %s", req.Path, req.Stage)
	}
	if req.Stage == api.StagePlan && req.Path == "plan.json" {
		if _, err := workflow.ParsePlan(req.Content); err != nil {
			return errf(400, "invalid_plan", "%v", err)
		}
	}
	var art api.Artifact
	err := s.tx(r.Context(), func(tx *Tx) error {
		ctx := r.Context()
		t, err := getThread(ctx, tx, a.teamID(), r.PathValue("id"))
		if err != nil {
			return err
		}
		st := t.StageState(req.Stage)
		if t.Status != api.ThreadActive || st.Status != api.StageOpen {
			return errf(409, "stage_not_open", "documents can only be edited while their stage is open")
		}
		if st.ActiveRunID != "" {
			return errf(409, "run_active", "an agent is editing this stage right now")
		}
		if art, err = insertArtifact(ctx, tx, t.ID, req.Stage, req.Path, req.Content, "", a.member.ID); err != nil {
			return err
		}
		if req.Stage == api.StageDesign && req.Content == "" && st.Selection == req.Path {
			st.Selection = ""
			if err := saveStages(ctx, tx, &t); err != nil {
				return err
			}
			if err := tx.emit(t.TeamID, t.ID, "", api.EvThreadUpdated, a.member.ID, t); err != nil {
				return err
			}
		}
		return tx.emit(t.TeamID, t.ID, "", api.EvArtifactUpdated, a.member.ID, art)
	})
	if err != nil {
		return err
	}
	writeJSON(w, 201, art)
	return nil
}

func (s *Server) handleArtifactHistory(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	ctx := r.Context()
	if _, err := getThread(ctx, s.db, a.teamID(), r.PathValue("id")); err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+artifactCols+` FROM artifacts WHERE thread_id = ? AND stage = ? AND path = ? ORDER BY version DESC LIMIT 50`,
		r.PathValue("id"), r.URL.Query().Get("stage"), r.URL.Query().Get("path"))
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []api.Artifact{}
	for rows.Next() {
		x, err := scanArtifact(rows)
		if err != nil {
			return err
		}
		out = append(out, x)
	}
	writeJSON(w, 200, out)
	return rows.Err()
}

// handleCreateExport records that the plan was published (from a member's
// machine, with their credentials) and completes the thread.
func (s *Server) handleCreateExport(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	var req api.CreateExportRequest
	if err := decode(w, r, &req, 0); err != nil {
		return err
	}
	if req.Target != api.OutputMarkdown && req.Target != api.OutputLinear && req.Target != api.OutputJira {
		return errf(400, "invalid_target", "unknown export target")
	}
	var exp api.Export
	_, err := s.mutateThread(r.Context(), a, r.PathValue("id"), func(tx *Tx, t *api.Thread) error {
		ctx := r.Context()
		st := t.StageState(api.StagePlan)
		if st.Status == api.StageLocked {
			return errf(409, "stage_not_open", "the plan isn't ready yet")
		}
		if st.ActiveRunID != "" {
			return errf(409, "run_active", "the plan is still being generated")
		}
		ok, err := hasPrimary(ctx, tx, t.ID, workflow.StageDef{ID: api.StagePlan, Primary: "plan.json"})
		if err != nil {
			return err
		}
		if !ok {
			return errf(409, "missing_document", "generate the plan before publishing it")
		}
		now := s.now()
		exp = api.Export{ID: authn.NewID("exp"), ThreadID: t.ID, Target: req.Target, MemberID: a.member.ID, Items: req.Items, CreatedAt: now}
		if exp.Items == nil {
			exp.Items = []api.ExportItem{}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO exports (id, thread_id, target, member_id, items, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			exp.ID, exp.ThreadID, exp.Target, exp.MemberID, mustJSON(exp.Items), ms(now)); err != nil {
			return err
		}
		st.Status, st.ApprovedBy, st.ApprovedAt = api.StageApproved, a.member.ID, &now
		t.Status = api.ThreadCompleted
		t.Delivery, t.DeliverySource, t.DeliveryBy, t.DeliveryAt = api.DeliveryPlanned, "", "", &now
		if err := tx.emit(t.TeamID, t.ID, "", api.EvExportCreated, a.member.ID, exp); err != nil {
			return err
		}
		_, err = s.insertMessage(ctx, tx, t, api.StagePlan, a.member.ID, api.MessageSystem, "published the plan to "+targetLabel(req.Target), "")
		return err
	})
	if err != nil {
		return err
	}
	writeJSON(w, 201, exp)
	return nil
}

func targetLabel(t string) string {
	switch t {
	case api.OutputLinear:
		return "Linear"
	case api.OutputJira:
		return "Jira"
	}
	return "Markdown"
}
