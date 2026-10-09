package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"github.com/sam-jeston/braid/internal/api"
	"github.com/sam-jeston/braid/internal/authn"
	"github.com/sam-jeston/braid/internal/workflow"
)

var migrations = []string{
	`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);

	CREATE TABLE setup_codes (
		id TEXT PRIMARY KEY,
		code_hash TEXT NOT NULL UNIQUE,
		created_at INTEGER NOT NULL,
		expires_at INTEGER NOT NULL,
		used_at INTEGER,
		team_id TEXT
	);

	CREATE TABLE teams (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		repo_url TEXT NOT NULL DEFAULT '',
		default_branch TEXT NOT NULL DEFAULT '',
		output_json TEXT NOT NULL DEFAULT '{}',
		codebase_brief TEXT NOT NULL DEFAULT '',
		rules TEXT NOT NULL DEFAULT '',
		scan_commit TEXT NOT NULL DEFAULT '',
		scanned_at INTEGER,
		created_at INTEGER NOT NULL
	);

	CREATE TABLE members (
		id TEXT PRIMARY KEY,
		team_id TEXT NOT NULL REFERENCES teams(id),
		display_name TEXT NOT NULL,
		email TEXT NOT NULL DEFAULT '',
		roles TEXT NOT NULL DEFAULT '[]',
		is_admin INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL,
		removed_at INTEGER
	);
	CREATE INDEX members_team ON members(team_id);

	CREATE TABLE devices (
		id TEXT PRIMARY KEY,
		member_id TEXT NOT NULL REFERENCES members(id),
		public_key TEXT NOT NULL UNIQUE,
		name TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL,
		last_seen_at INTEGER,
		revoked_at INTEGER
	);

	CREATE TABLE sessions (
		token_hash TEXT PRIMARY KEY,
		device_id TEXT NOT NULL REFERENCES devices(id),
		expires_at INTEGER NOT NULL,
		created_at INTEGER NOT NULL
	);

	CREATE TABLE invites (
		id TEXT PRIMARY KEY,
		team_id TEXT NOT NULL REFERENCES teams(id),
		code_hash TEXT NOT NULL UNIQUE,
		roles TEXT NOT NULL,
		is_admin INTEGER NOT NULL DEFAULT 0,
		max_uses INTEGER NOT NULL,
		uses INTEGER NOT NULL DEFAULT 0,
		expires_at INTEGER NOT NULL,
		created_by TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		revoked_at INTEGER,
		hint TEXT NOT NULL
	);

	CREATE TABLE threads (
		id TEXT PRIMARY KEY,
		team_id TEXT NOT NULL REFERENCES teams(id),
		title TEXT NOT NULL,
		status TEXT NOT NULL,
		current_stage TEXT NOT NULL,
		created_by TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	);
	CREATE INDEX threads_team ON threads(team_id, updated_at DESC);

	CREATE TABLE thread_stages (
		thread_id TEXT NOT NULL REFERENCES threads(id),
		stage TEXT NOT NULL,
		position INTEGER NOT NULL,
		status TEXT NOT NULL,
		approved_by TEXT,
		approved_at INTEGER,
		selection TEXT NOT NULL DEFAULT '',
		active_run_id TEXT,
		PRIMARY KEY (thread_id, stage)
	);

	CREATE TABLE messages (
		id TEXT PRIMARY KEY,
		thread_id TEXT NOT NULL REFERENCES threads(id),
		stage TEXT NOT NULL,
		author_id TEXT NOT NULL,
		kind TEXT NOT NULL,
		body TEXT NOT NULL,
		run_id TEXT,
		created_at INTEGER NOT NULL
	);
	CREATE INDEX messages_thread ON messages(thread_id, created_at);

	CREATE TABLE runs (
		id TEXT PRIMARY KEY,
		team_id TEXT NOT NULL REFERENCES teams(id),
		thread_id TEXT,
		stage TEXT NOT NULL DEFAULT '',
		kind TEXT NOT NULL,
		member_id TEXT NOT NULL,
		agent TEXT NOT NULL,
		agent_version TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL,
		trigger_message_id TEXT,
		commit_sha TEXT NOT NULL DEFAULT '',
		reply TEXT NOT NULL DEFAULT '',
		error TEXT NOT NULL DEFAULT '',
		cost_usd REAL NOT NULL DEFAULT 0,
		started_at INTEGER NOT NULL,
		finished_at INTEGER,
		heartbeat_at INTEGER NOT NULL
	);
	CREATE INDEX runs_thread ON runs(thread_id, started_at);
	CREATE INDEX runs_team_kind ON runs(team_id, kind, started_at);
	CREATE INDEX runs_status ON runs(status);

	CREATE TABLE artifacts (
		id TEXT PRIMARY KEY,
		thread_id TEXT NOT NULL REFERENCES threads(id),
		stage TEXT NOT NULL,
		path TEXT NOT NULL,
		version INTEGER NOT NULL,
		content TEXT NOT NULL,
		run_id TEXT,
		author_id TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		UNIQUE (thread_id, stage, path, version)
	);

	CREATE TABLE exports (
		id TEXT PRIMARY KEY,
		thread_id TEXT NOT NULL REFERENCES threads(id),
		target TEXT NOT NULL,
		member_id TEXT NOT NULL,
		items TEXT NOT NULL,
		created_at INTEGER NOT NULL
	);

	CREATE TABLE events (
		seq INTEGER PRIMARY KEY AUTOINCREMENT,
		team_id TEXT NOT NULL,
		thread_id TEXT,
		run_id TEXT,
		type TEXT NOT NULL,
		actor_id TEXT,
		data TEXT NOT NULL,
		created_at INTEGER NOT NULL
	);
	CREATE INDEX events_team ON events(team_id, seq);
	CREATE INDEX events_run ON events(run_id, seq);`,

	// Teams can reference several repositories. Names for existing ones are
	// derived from their URL when the team is next read (see scanTeam).
	`ALTER TABLE teams ADD COLUMN repos_json TEXT NOT NULL DEFAULT '[]';
	UPDATE teams SET repos_json = json_array(json_object('name', '', 'url', repo_url, 'branch', default_branch)) WHERE repo_url != '';
	ALTER TABLE teams DROP COLUMN repo_url;
	ALTER TABLE teams DROP COLUMN default_branch;`,
}

func openDB(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite allows one writer; a single connection keeps things simple and
	// avoids SQLITE_BUSY under concurrent requests. Team-scale load is tiny.
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	for i := v; i < len(migrations); i++ {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Tx wraps a transaction and collects events, which are published to
// websocket subscribers only after a successful commit.
type Tx struct {
	*sql.Tx
	s      *Server
	events []api.Event
	teamOf map[int64]string
}

func (s *Server) tx(ctx context.Context, fn func(tx *Tx) error) error {
	sqlTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	tx := &Tx{Tx: sqlTx, s: s, teamOf: map[int64]string{}}
	if err := fn(tx); err != nil {
		sqlTx.Rollback()
		return err
	}
	if err := sqlTx.Commit(); err != nil {
		return err
	}
	for _, ev := range tx.events {
		s.hub.publish(tx.teamOf[ev.Seq], ev)
	}
	return nil
}

func (tx *Tx) emit(teamID, threadID, runID, typ, actorID string, data any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	now := tx.s.now()
	res, err := tx.Exec(`INSERT INTO events (team_id, thread_id, run_id, type, actor_id, data, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		teamID, nullStr(threadID), nullStr(runID), typ, nullStr(actorID), string(raw), ms(now))
	if err != nil {
		return err
	}
	seq, _ := res.LastInsertId()
	tx.events = append(tx.events, api.Event{Seq: seq, Type: typ, ThreadID: threadID, RunID: runID, ActorID: actorID, Data: raw, CreatedAt: now})
	tx.teamOf[seq] = teamID
	return nil
}

// --- small helpers ---

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

type scanner interface{ Scan(dest ...any) error }

func ms(t time.Time) int64 { return t.UnixMilli() }

func fromMs(v int64) time.Time { return time.UnixMilli(v).UTC() }

func fromNullMs(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := fromMs(v.Int64)
	return &t
}

func nullStr(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

func nullMs(t *time.Time) sql.NullInt64 {
	if t == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: ms(*t), Valid: true}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func parseRoles(s string) []api.Role {
	var rs []api.Role
	_ = json.Unmarshal([]byte(s), &rs)
	if rs == nil {
		rs = []api.Role{}
	}
	return rs
}

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

// --- meta ---

func (s *Server) loadServerID(ctx context.Context) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'server_id'`).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !isNoRows(err) {
		return "", err
	}
	id = authn.NewID("srv")
	_, err = s.db.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES ('server_id', ?)`, id)
	return id, err
}

// --- teams ---

const teamCols = `id, name, repos_json, output_json, codebase_brief, rules, scan_commit, scanned_at, created_at`

func scanTeam(r scanner) (api.Team, error) {
	var t api.Team
	var repos, out string
	var scanned sql.NullInt64
	var created int64
	err := r.Scan(&t.ID, &t.Name, &repos, &out, &t.CodebaseBrief, &t.Rules, &t.ScanCommit, &scanned, &created)
	if err != nil {
		return t, err
	}
	_ = json.Unmarshal([]byte(repos), &t.Repos)
	// Fills in names for rows migrated from single-repository teams.
	if norm, err := workflow.NormalizeRepos(t.Repos); err == nil {
		t.Repos = norm
	}
	if t.Repos == nil {
		t.Repos = []api.RepoRef{}
	}
	_ = json.Unmarshal([]byte(out), &t.Output)
	if t.Output.Config == nil {
		t.Output.Config = map[string]string{}
	}
	t.ScannedAt = fromNullMs(scanned)
	t.CreatedAt = fromMs(created)
	return t, nil
}

func getTeam(ctx context.Context, q queryer, id string) (api.Team, error) {
	return scanTeam(q.QueryRowContext(ctx, `SELECT `+teamCols+` FROM teams WHERE id = ?`, id))
}

// --- members ---

const memberCols = `id, team_id, display_name, email, roles, is_admin, created_at, removed_at`

func scanMember(r scanner) (api.Member, error) {
	var m api.Member
	var roles string
	var created int64
	var removed sql.NullInt64
	if err := r.Scan(&m.ID, &m.TeamID, &m.DisplayName, &m.Email, &roles, &m.IsAdmin, &created, &removed); err != nil {
		return m, err
	}
	m.Roles = parseRoles(roles)
	m.CreatedAt = fromMs(created)
	m.RemovedAt = fromNullMs(removed)
	return m, nil
}

func getMember(ctx context.Context, q queryer, id string) (api.Member, error) {
	return scanMember(q.QueryRowContext(ctx, `SELECT `+memberCols+` FROM members WHERE id = ?`, id))
}

func listMembers(ctx context.Context, q queryer, teamID string) ([]api.Member, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+memberCols+` FROM members WHERE team_id = ? ORDER BY created_at`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.Member{}
	for rows.Next() {
		m, err := scanMember(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// --- threads ---

const threadCols = `id, team_id, title, status, current_stage, created_by, created_at, updated_at`

func scanThread(r scanner) (api.Thread, error) {
	var t api.Thread
	var created, updated int64
	if err := r.Scan(&t.ID, &t.TeamID, &t.Title, &t.Status, &t.CurrentStage, &t.CreatedBy, &created, &updated); err != nil {
		return t, err
	}
	t.CreatedAt, t.UpdatedAt = fromMs(created), fromMs(updated)
	return t, nil
}

func loadStages(ctx context.Context, q queryer, threadIDs []string) (map[string][]api.StageState, error) {
	out := map[string][]api.StageState{}
	if len(threadIDs) == 0 {
		return out, nil
	}
	args := make([]any, len(threadIDs))
	ph := ""
	for i, id := range threadIDs {
		args[i] = id
		if i > 0 {
			ph += ","
		}
		ph += "?"
	}
	rows, err := q.QueryContext(ctx, `SELECT thread_id, stage, status, approved_by, approved_at, selection, active_run_id
		FROM thread_stages WHERE thread_id IN (`+ph+`) ORDER BY thread_id, position`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var tid string
		var st api.StageState
		var approvedBy, activeRun sql.NullString
		var approvedAt sql.NullInt64
		if err := rows.Scan(&tid, &st.Stage, &st.Status, &approvedBy, &approvedAt, &st.Selection, &activeRun); err != nil {
			return nil, err
		}
		st.ApprovedBy = approvedBy.String
		st.ApprovedAt = fromNullMs(approvedAt)
		st.ActiveRunID = activeRun.String
		out[tid] = append(out[tid], st)
	}
	return out, rows.Err()
}

func getThread(ctx context.Context, q queryer, teamID, id string) (api.Thread, error) {
	t, err := scanThread(q.QueryRowContext(ctx, `SELECT `+threadCols+` FROM threads WHERE id = ? AND team_id = ?`, id, teamID))
	if err != nil {
		return t, err
	}
	st, err := loadStages(ctx, q, []string{id})
	if err != nil {
		return t, err
	}
	t.Stages = st[id]
	return t, nil
}

func saveStages(ctx context.Context, q queryer, t *api.Thread) error {
	for _, st := range t.Stages {
		_, err := q.ExecContext(ctx, `UPDATE thread_stages SET status = ?, approved_by = ?, approved_at = ?, selection = ?, active_run_id = ?
			WHERE thread_id = ? AND stage = ?`,
			st.Status, nullStr(st.ApprovedBy), nullMs(st.ApprovedAt), st.Selection, nullStr(st.ActiveRunID), t.ID, st.Stage)
		if err != nil {
			return err
		}
	}
	_, err := q.ExecContext(ctx, `UPDATE threads SET title = ?, status = ?, current_stage = ?, updated_at = ? WHERE id = ?`,
		t.Title, t.Status, t.CurrentStage, ms(t.UpdatedAt), t.ID)
	return err
}

// --- messages, runs, artifacts, exports ---

const messageCols = `id, thread_id, stage, author_id, kind, body, run_id, created_at`

func scanMessage(r scanner) (api.Message, error) {
	var m api.Message
	var runID sql.NullString
	var created int64
	if err := r.Scan(&m.ID, &m.ThreadID, &m.Stage, &m.AuthorID, &m.Kind, &m.Body, &runID, &created); err != nil {
		return m, err
	}
	m.RunID = runID.String
	m.CreatedAt = fromMs(created)
	return m, nil
}

const runCols = `id, team_id, thread_id, stage, kind, member_id, agent, agent_version, status, trigger_message_id, commit_sha, reply, error, cost_usd, started_at, finished_at, heartbeat_at`

func scanRun(r scanner) (api.Run, error) {
	var x api.Run
	var threadID, trigger sql.NullString
	var started, hb int64
	var finished sql.NullInt64
	if err := r.Scan(&x.ID, &x.TeamID, &threadID, &x.Stage, &x.Kind, &x.MemberID, &x.Agent, &x.AgentVersion, &x.Status,
		&trigger, &x.CommitSHA, &x.Reply, &x.Error, &x.CostUSD, &started, &finished, &hb); err != nil {
		return x, err
	}
	x.ThreadID, x.TriggerMessageID = threadID.String, trigger.String
	x.StartedAt, x.HeartbeatAt = fromMs(started), fromMs(hb)
	x.FinishedAt = fromNullMs(finished)
	return x, nil
}

func getRun(ctx context.Context, q queryer, teamID, id string) (api.Run, error) {
	return scanRun(q.QueryRowContext(ctx, `SELECT `+runCols+` FROM runs WHERE id = ? AND team_id = ?`, id, teamID))
}

const artifactCols = `id, thread_id, stage, path, version, content, run_id, author_id, created_at`

func scanArtifact(r scanner) (api.Artifact, error) {
	var a api.Artifact
	var runID sql.NullString
	var created int64
	if err := r.Scan(&a.ID, &a.ThreadID, &a.Stage, &a.Path, &a.Version, &a.Content, &runID, &a.AuthorID, &created); err != nil {
		return a, err
	}
	a.RunID = runID.String
	a.CreatedAt = fromMs(created)
	return a, nil
}

// latestArtifacts returns the newest version of every (stage, path) in a thread.
func latestArtifacts(ctx context.Context, q queryer, threadID string) ([]api.Artifact, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+artifactCols+` FROM artifacts a
		WHERE thread_id = ? AND content != '' AND version = (
			SELECT MAX(version) FROM artifacts b WHERE b.thread_id = a.thread_id AND b.stage = a.stage AND b.path = a.path)
		ORDER BY stage, path`, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.Artifact{}
	for rows.Next() {
		a, err := scanArtifact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func insertArtifact(ctx context.Context, tx *Tx, threadID string, stage api.Stage, path, content, runID, authorID string) (api.Artifact, error) {
	var v int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM artifacts WHERE thread_id = ? AND stage = ? AND path = ?`,
		threadID, stage, path).Scan(&v); err != nil {
		return api.Artifact{}, err
	}
	a := api.Artifact{
		ID: authn.NewID("art"), ThreadID: threadID, Stage: stage, Path: path, Version: v + 1,
		Content: content, RunID: runID, AuthorID: authorID, CreatedAt: tx.s.now(),
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO artifacts (`+artifactCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.ThreadID, a.Stage, a.Path, a.Version, a.Content, nullStr(a.RunID), a.AuthorID, ms(a.CreatedAt))
	return a, err
}

func scanExport(r scanner) (api.Export, error) {
	var e api.Export
	var items string
	var created int64
	if err := r.Scan(&e.ID, &e.ThreadID, &e.Target, &e.MemberID, &items, &created); err != nil {
		return e, err
	}
	_ = json.Unmarshal([]byte(items), &e.Items)
	e.CreatedAt = fromMs(created)
	return e, nil
}

func scanInvite(r scanner) (api.Invite, error) {
	var in api.Invite
	var roles string
	var expires, created int64
	var revoked sql.NullInt64
	if err := r.Scan(&in.ID, &roles, &in.IsAdmin, &in.MaxUses, &in.Uses, &expires, &in.CreatedBy, &created, &revoked, &in.Hint); err != nil {
		return in, err
	}
	in.Roles = parseRoles(roles)
	in.ExpiresAt, in.CreatedAt = fromMs(expires), fromMs(created)
	in.RevokedAt = fromNullMs(revoked)
	return in, nil
}

const inviteCols = `id, roles, is_admin, max_uses, uses, expires_at, created_by, created_at, revoked_at, hint`
