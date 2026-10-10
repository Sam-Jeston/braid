package local

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/sam-jeston/braid/internal/api"
	"github.com/sam-jeston/braid/internal/client"
	"github.com/sam-jeston/braid/internal/export"
)

// Delivery tracking. While connected, each member's app checks the tracker
// for published plans that are still planned, using that member's own
// credentials, and reports which ones were built or cancelled. Any member
// with credentials can do it, so the team sees progress as long as one of
// them has Braid open.

// deliveryTiming is when a connected app checks the tracker. Tests shorten it.
type deliveryTiming struct {
	first time.Duration // after connecting
	every time.Duration // then, while connected
}

var defaultDeliveryTiming = deliveryTiming{first: 5 * time.Second, every: 15 * time.Minute}

type DeliveryCheck struct {
	Checked int `json:"checked"` // plans looked up in the tracker
	Updated int `json:"updated"` // plans now marked built or cancelled
	Skipped int `json:"skipped"` // plans this device has no credentials for
}

// CheckDeliveries checks the tracker now, for one thread or (threadID "")
// every planned one.
func (c *Core) CheckDeliveries(ctx context.Context, threadID string) (DeliveryCheck, error) {
	s, err := c.session()
	if err != nil {
		return DeliveryCheck{}, err
	}
	return c.checkDeliveries(ctx, s, threadID)
}

func (c *Core) checkDeliveries(ctx context.Context, s *session, threadID string) (DeliveryCheck, error) {
	var res DeliveryCheck
	s.mu.Lock()
	s.lastDeliveryCheck = time.Now()
	s.mu.Unlock()
	team, err := s.freshTeam(ctx)
	if err != nil {
		return res, err
	}
	var planned []api.Export
	if err := s.client.Do(ctx, "GET", "/v1/exports/planned", nil, &planned); err != nil {
		return res, err
	}
	var firstErr error
	for _, exp := range planned {
		if threadID != "" && exp.ThreadID != threadID || exp.Target == api.OutputMarkdown {
			continue
		}
		tr, keys := c.issueTracker(team, exp)
		if tr == nil {
			res.Skipped++
			continue
		}
		states, err := tr.States(ctx, keys)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		res.Checked++
		var update []api.IssueState
		changed := false
		for i, it := range exp.Items {
			st, ok := states[it.Key]
			if ok {
				update = append(update, api.IssueState{Key: it.Key, State: st})
				changed = changed || st != it.State
			}
			exp.Items[i].State = st // unknown issues don't count towards the outcome
		}
		delivery := export.Outcome(exp.Items)
		if delivery == api.DeliveryPlanned && !changed {
			continue
		}
		err = s.client.Do(ctx, "POST", "/v1/threads/"+url.PathEscape(exp.ThreadID)+"/delivery",
			api.DeliveryUpdate{Delivery: delivery, Tracker: exp.Target, Items: update}, nil)
		switch {
		case client.IsStatus(err, 409):
			// A teammate's app or an engineer got there first.
		case err != nil:
			if firstErr == nil {
				firstErr = err
			}
		case delivery != api.DeliveryPlanned:
			res.Updated++
		}
	}
	if res.Updated > 0 {
		c.log.Info("tracker shows plans delivered", "updated", res.Updated, "checked", res.Checked)
	}
	return res, firstErr
}

// issueTracker returns a reader for the tracker exp was published to, using
// this member's credentials, and the issue keys to look up. It returns nil
// when this device can't check it.
func (c *Core) issueTracker(team api.Team, exp api.Export) (export.Tracker, []string) {
	var keys []string
	switch exp.Target {
	case api.OutputLinear:
		key, err := c.secrets.Get(secretLinear)
		if err != nil || key == "" {
			return nil, nil
		}
		for _, it := range exp.Items {
			keys = append(keys, it.Key)
		}
		c.mu.Lock()
		endpoint := c.linearEndpoint
		c.mu.Unlock()
		return &export.Linear{APIKey: key, Endpoint: endpoint}, keys
	case api.OutputJira:
		// Credentials only ever go to the team's configured Jira site, over
		// HTTPS, and only for issues that were published there.
		site := team.Output.Config["site"]
		if team.Output.Type != api.OutputJira || !strings.HasPrefix(site, "https://") {
			return nil, nil
		}
		token, err := c.secrets.Get(secretJira)
		c.mu.Lock()
		email := c.cfg.JiraEmail
		c.mu.Unlock()
		if err != nil || token == "" || email == "" {
			return nil, nil
		}
		for _, it := range exp.Items {
			if strings.HasPrefix(it.URL, site+"/browse/") {
				keys = append(keys, it.Key)
			}
		}
		if len(keys) == 0 {
			return nil, nil
		}
		return &export.Jira{Site: site, Email: email, Token: token}, keys
	}
	return nil, nil
}

// trackDeliveries checks the tracker shortly after connecting, then
// periodically, until ctx ends with the connection. Reconnecting doesn't
// bring the next check forward.
func (s *session) trackDeliveries(ctx context.Context) {
	s.c.mu.Lock()
	timing := s.c.deliveryTiming
	s.c.mu.Unlock()
	s.mu.Lock()
	wait := max(timing.first, time.Until(s.lastDeliveryCheck.Add(timing.every)))
	s.mu.Unlock()
	t := time.NewTimer(wait)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if _, err := s.c.checkDeliveries(ctx, s, ""); err != nil && ctx.Err() == nil {
			s.c.log.Warn("check the tracker for delivered plans", "err", err)
		}
		t.Reset(timing.every)
	}
}
