package server

import (
	"context"
	"net/http"

	"github.com/sam-jeston/braid/internal/api"
)

// Delivery: what became of a published plan. See api.Delivery.

// handlePlannedExports lists the latest export of every published plan that
// is still planned, so members' apps can check it against the tracker.
func (s *Server) handlePlannedExports(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	rows, err := s.db.QueryContext(r.Context(), `SELECT e.id, e.thread_id, e.target, e.member_id, e.items, e.created_at
		FROM exports e JOIN threads t ON t.id = e.thread_id
		WHERE t.team_id = ? AND t.status = ? AND t.delivery = ?
		AND e.rowid = (SELECT rowid FROM exports WHERE thread_id = e.thread_id ORDER BY created_at DESC, rowid DESC LIMIT 1)
		ORDER BY e.created_at`, a.teamID(), api.ThreadCompleted, api.DeliveryPlanned)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []api.Export{}
	for rows.Next() {
		e, err := scanExport(rows)
		if err != nil {
			return err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	writeJSON(w, 200, out)
	return nil
}

func latestExport(ctx context.Context, q queryer, threadID string) (api.Export, error) {
	return scanExport(q.QueryRowContext(ctx, `SELECT id, thread_id, target, member_id, items, created_at FROM exports
		WHERE thread_id = ? ORDER BY created_at DESC, rowid DESC LIMIT 1`, threadID))
}

// handleDelivery records a plan as planned, built or cancelled. Engineers set
// it by hand. Members' apps report what the tracker shows, which only moves a
// plan on from planned, so it never overrides an engineer's decision.
func (s *Server) handleDelivery(w http.ResponseWriter, r *http.Request, a *authCtx) error {
	var req api.DeliveryUpdate
	if err := decode(w, r, &req, 0); err != nil {
		return err
	}
	if !req.Delivery.Valid() {
		return errf(400, "invalid_delivery", "delivery must be planned, built or cancelled")
	}
	switch req.Tracker {
	case "":
		if !api.HasRole(a.member.Roles, api.RoleEngineering) {
			return errf(403, "forbidden", "only engineers can mark a plan as built or cancelled")
		}
	case api.OutputLinear, api.OutputJira:
	default:
		return errf(400, "invalid_tracker", "tracker must be linear or jira")
	}
	if len(req.Items) > 1000 {
		return errf(400, "too_many_items", "too many issues in one update")
	}
	ctx := r.Context()
	var t api.Thread
	err := s.tx(ctx, func(tx *Tx) error {
		var err error
		if t, err = getThread(ctx, tx, a.teamID(), r.PathValue("id")); err != nil {
			return err
		}
		if t.Delivery == "" {
			return errf(409, "not_published", "publish the plan before marking it %s", req.Delivery)
		}
		if req.Tracker != "" {
			if t.Delivery != api.DeliveryPlanned {
				return errf(409, "not_planned", "this plan is already marked %s", t.Delivery)
			}
			exp, err := latestExport(ctx, tx, t.ID)
			if err != nil {
				return err
			}
			if exp.Target != req.Tracker {
				return errf(409, "wrong_tracker", "this plan was published to %s", targetLabel(exp.Target))
			}
			if updateIssueStates(&exp, req.Items) {
				if _, err := tx.ExecContext(ctx, `UPDATE exports SET items = ? WHERE id = ?`, mustJSON(exp.Items), exp.ID); err != nil {
					return err
				}
				if err := tx.emit(t.TeamID, t.ID, "", api.EvExportUpdated, a.member.ID, exp); err != nil {
					return err
				}
			}
		}
		if t.Delivery == req.Delivery {
			return nil
		}
		now := s.now()
		source := api.DeliveryManual
		if req.Tracker != "" {
			source = req.Tracker
		}
		t.Delivery, t.DeliverySource, t.DeliveryBy, t.DeliveryAt = req.Delivery, source, a.member.ID, &now
		t.UpdatedAt = now
		if err := saveThread(ctx, tx, &t); err != nil {
			return err
		}
		if err := tx.emit(t.TeamID, t.ID, "", api.EvThreadUpdated, a.member.ID, t); err != nil {
			return err
		}
		_, err = s.insertMessage(ctx, tx, &t, api.StagePlan, a.member.ID, api.MessageSystem, deliveryMessage(req), "")
		return err
	})
	if err != nil {
		return err
	}
	writeJSON(w, 200, t)
	return nil
}

// updateIssueStates applies tracker states to the export's items by key and
// reports whether anything changed.
func updateIssueStates(exp *api.Export, states []api.IssueState) bool {
	byKey := map[string]string{}
	for _, st := range states {
		switch st.State {
		case api.IssueTodo, api.IssueInProgress, api.IssueDone, api.IssueCancelled:
			byKey[st.Key] = st.State
		}
	}
	changed := false
	for i := range exp.Items {
		if st, ok := byKey[exp.Items[i].Key]; ok && exp.Items[i].State != st {
			exp.Items[i].State = st
			changed = true
		}
	}
	return changed
}

// deliveryMessage is the system line in the plan's conversation, after the
// member's name.
func deliveryMessage(req api.DeliveryUpdate) string {
	if req.Tracker != "" {
		if req.Delivery == api.DeliveryCancelled {
			return "found the plan cancelled in " + targetLabel(req.Tracker)
		}
		return "found the plan done in " + targetLabel(req.Tracker)
	}
	if req.Delivery == api.DeliveryPlanned {
		return "moved the plan back to planned"
	}
	return "marked the plan as " + string(req.Delivery)
}
