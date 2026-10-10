// Package api defines the wire types shared by braid-server, the local core
// and (mirrored in TypeScript) the desktop UI.
package api

import (
	"encoding/json"
	"time"
)

type Role string

const (
	RoleProduct     Role = "product"
	RoleDesign      Role = "design"
	RoleEngineering Role = "engineering"
)

var AllRoles = []Role{RoleProduct, RoleDesign, RoleEngineering}

func (r Role) Valid() bool {
	for _, x := range AllRoles {
		if r == x {
			return true
		}
	}
	return false
}

func HasRole(roles []Role, r Role) bool {
	for _, x := range roles {
		if x == r {
			return true
		}
	}
	return false
}

type Stage string

const (
	StageSparring  Stage = "sparring"
	StageDesign    Stage = "design"
	StageTechnical Stage = "technical"
	StagePlan      Stage = "plan"
)

type StageStatus string

const (
	StageLocked   StageStatus = "locked"
	StageOpen     StageStatus = "open"
	StageApproved StageStatus = "approved"
)

type ThreadStatus string

const (
	ThreadActive    ThreadStatus = "active"
	ThreadCompleted ThreadStatus = "completed"
	ThreadArchived  ThreadStatus = "archived"
)

// Delivery is what became of a published plan. It starts as planned when the
// plan is published; engineers mark it built or cancelled, and members' apps
// do so automatically when the team's tracker shows the work closed.
type Delivery string

const (
	DeliveryPlanned   Delivery = "planned"
	DeliveryBuilt     Delivery = "built"
	DeliveryCancelled Delivery = "cancelled"
)

func (d Delivery) Valid() bool {
	return d == DeliveryPlanned || d == DeliveryBuilt || d == DeliveryCancelled
}

// DeliveryManual is the DeliverySource of a delivery an engineer set by hand.
const DeliveryManual = "manual"

// Issue states, as Braid reads them back from a tracker.
const (
	IssueTodo       = "todo"
	IssueInProgress = "in_progress"
	IssueDone       = "done"
	IssueCancelled  = "cancelled"
)

type RunKind string

const (
	RunKindStage RunKind = "stage"
	RunKindScan  RunKind = "scan"
)

type RunStatus string

const (
	RunRunning   RunStatus = "running"
	RunSucceeded RunStatus = "succeeded"
	RunFailed    RunStatus = "failed"
	RunCancelled RunStatus = "cancelled"
	RunAbandoned RunStatus = "abandoned"
)

// Output target types.
const (
	OutputMarkdown = "markdown"
	OutputLinear   = "linear"
	OutputJira     = "jira"
)

// OutputTarget describes where final plans are published. Config only holds
// non-secret settings (team keys, project keys, site URLs); credentials stay
// on each member's machine.
type OutputTarget struct {
	Type   string            `json:"type"`
	Config map[string]string `json:"config"`
}

type ServerInfo struct {
	Product  string `json:"product"`
	Version  string `json:"version"`
	ServerID string `json:"server_id"`
	// Protocol and MinClientProtocol are absent (0) on servers built before
	// protocol versioning; see version.go.
	Protocol          int `json:"protocol"`
	MinClientProtocol int `json:"min_client_protocol"`
}

// RepoRef is one repository the team's agents read. Name is short, unique
// within the team, and how agents and the UI refer to it.
type RepoRef struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Branch string `json:"branch"` // empty = the remote's default branch
}

type Team struct {
	ID            string       `json:"id"`
	Name          string       `json:"name"`
	Repos         []RepoRef    `json:"repos"`
	Output        OutputTarget `json:"output"`
	CodebaseBrief string       `json:"codebase_brief"`
	Rules         string       `json:"rules"`
	ScanCommit    string       `json:"scan_commit"` // same format as Run.CommitSHA
	ScannedAt     *time.Time   `json:"scanned_at"`
	CreatedAt     time.Time    `json:"created_at"`
}

type TeamSettings struct {
	Name   string       `json:"name"`
	Repos  []RepoRef    `json:"repos"`
	Output OutputTarget `json:"output"`
}

type TeamPatch struct {
	Name          *string       `json:"name,omitempty"`
	Repos         *[]RepoRef    `json:"repos,omitempty"`
	Output        *OutputTarget `json:"output,omitempty"`
	CodebaseBrief *string       `json:"codebase_brief,omitempty"`
	Rules         *string       `json:"rules,omitempty"`
}

type Member struct {
	ID          string     `json:"id"`
	TeamID      string     `json:"team_id"`
	DisplayName string     `json:"display_name"`
	Email       string     `json:"email"`
	Roles       []Role     `json:"roles"`
	IsAdmin     bool       `json:"is_admin"`
	CreatedAt   time.Time  `json:"created_at"`
	RemovedAt   *time.Time `json:"removed_at,omitempty"`
}

type MemberPatch struct {
	Roles   []Role `json:"roles,omitempty"`
	IsAdmin *bool  `json:"is_admin,omitempty"`
}

type Device struct {
	ID         string     `json:"id"`
	MemberID   string     `json:"member_id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastSeenAt *time.Time `json:"last_seen_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

type Invite struct {
	ID        string     `json:"id"`
	Roles     []Role     `json:"roles"`
	IsAdmin   bool       `json:"is_admin"`
	MaxUses   int        `json:"max_uses"`
	Uses      int        `json:"uses"`
	ExpiresAt time.Time  `json:"expires_at"`
	CreatedBy string     `json:"created_by"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
	Hint      string     `json:"hint"`
}

type CreateInviteRequest struct {
	Roles          []Role `json:"roles"`
	IsAdmin        bool   `json:"is_admin"`
	MaxUses        int    `json:"max_uses"`
	ExpiresInHours int    `json:"expires_in_hours"`
}

type CreateInviteResponse struct {
	Invite Invite `json:"invite"`
	Code   string `json:"code"`
}

// --- onboarding & auth ---

type DeviceRegistration struct {
	PublicKey string `json:"public_key"`
	Name      string `json:"name"`
	// Signature proves possession of the private key; see authn.RegistrationMessage.
	Signature string `json:"signature"`
}

type NewMember struct {
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	Roles       []Role `json:"roles,omitempty"`
}

type CreateTeamRequest struct {
	SetupCode string             `json:"setup_code"`
	Team      TeamSettings       `json:"team"`
	Member    NewMember          `json:"member"`
	Device    DeviceRegistration `json:"device"`
}

type RedeemInviteRequest struct {
	Code   string             `json:"code"`
	Member NewMember          `json:"member"`
	Device DeviceRegistration `json:"device"`
}

type JoinResponse struct {
	Team     Team   `json:"team"`
	Member   Member `json:"member"`
	DeviceID string `json:"device_id"`
}

type ChallengeRequest struct {
	DeviceID string `json:"device_id"`
}

type ChallengeResponse struct {
	Nonce     string    `json:"nonce"`
	ExpiresAt time.Time `json:"expires_at"`
}

type TokenRequest struct {
	DeviceID  string `json:"device_id"`
	Nonce     string `json:"nonce"`
	Signature string `json:"signature"`
}

type TokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Me struct {
	Member   Member `json:"member"`
	Team     Team   `json:"team"`
	DeviceID string `json:"device_id"`
}

// --- threads ---

type StageState struct {
	Stage       Stage       `json:"stage"`
	Status      StageStatus `json:"status"`
	ApprovedBy  string      `json:"approved_by,omitempty"`
	ApprovedAt  *time.Time  `json:"approved_at,omitempty"`
	Selection   string      `json:"selection,omitempty"`
	ActiveRunID string      `json:"active_run_id,omitempty"`
}

type Thread struct {
	ID           string       `json:"id"`
	TeamID       string       `json:"team_id"`
	Title        string       `json:"title"`
	Status       ThreadStatus `json:"status"`
	CurrentStage Stage        `json:"current_stage"`
	CreatedBy    string       `json:"created_by"`
	CreatedAt    time.Time    `json:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
	Stages       []StageState `json:"stages"`
	// Delivery is empty until the plan is published.
	Delivery       Delivery   `json:"delivery,omitempty"`
	DeliverySource string     `json:"delivery_source,omitempty"` // DeliveryManual, or the tracker it was read from (OutputLinear, OutputJira)
	DeliveryBy     string     `json:"delivery_by,omitempty"`     // the engineer who set it, or the member whose app read it from the tracker
	DeliveryAt     *time.Time `json:"delivery_at,omitempty"`
}

func (t *Thread) StageState(s Stage) *StageState {
	for i := range t.Stages {
		if t.Stages[i].Stage == s {
			return &t.Stages[i]
		}
	}
	return nil
}

type CreateThreadRequest struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

type ThreadPatch struct {
	Title  *string       `json:"title,omitempty"`
	Status *ThreadStatus `json:"status,omitempty"`
}

const (
	MessageHuman  = "human"
	MessageAgent  = "agent"
	MessageSystem = "system"
)

type Message struct {
	ID        string    `json:"id"`
	ThreadID  string    `json:"thread_id"`
	Stage     Stage     `json:"stage"`
	AuthorID  string    `json:"author_id"`
	Kind      string    `json:"kind"`
	Body      string    `json:"body"`
	RunID     string    `json:"run_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type CreateMessageRequest struct {
	Stage Stage  `json:"stage"`
	Body  string `json:"body"`
}

type SelectionRequest struct {
	Path string `json:"path"`
}

type Artifact struct {
	ID        string    `json:"id"`
	ThreadID  string    `json:"thread_id"`
	Stage     Stage     `json:"stage"`
	Path      string    `json:"path"`
	Version   int       `json:"version"`
	Content   string    `json:"content"`
	RunID     string    `json:"run_id,omitempty"`
	AuthorID  string    `json:"author_id"`
	CreatedAt time.Time `json:"created_at"`
}

type ArtifactUpload struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type SaveArtifactRequest struct {
	Stage   Stage  `json:"stage"`
	Path    string `json:"path"`
	Content string `json:"content"`
}

type ExportItem struct {
	Ref   string `json:"ref,omitempty"`
	Title string `json:"title"`
	Key   string `json:"key"`
	URL   string `json:"url"`
	State string `json:"state,omitempty"` // last state read from the tracker (Issue*); Linear and Jira only
}

type Export struct {
	ID        string       `json:"id"`
	ThreadID  string       `json:"thread_id"`
	Target    string       `json:"target"`
	MemberID  string       `json:"member_id"`
	Items     []ExportItem `json:"items"`
	CreatedAt time.Time    `json:"created_at"`
}

type CreateExportRequest struct {
	Target string       `json:"target"`
	Items  []ExportItem `json:"items"`
}

// DeliveryUpdate moves a published plan between planned, built and
// cancelled. An engineer sets it by hand when Tracker is empty. Otherwise a
// member's app reports what it read from the tracker, along with the state
// of each issue, and the server only accepts it while the plan is planned.
type DeliveryUpdate struct {
	Delivery Delivery     `json:"delivery"`
	Tracker  string       `json:"tracker,omitempty"` // OutputLinear | OutputJira
	Items    []IssueState `json:"items,omitempty"`
}

type IssueState struct {
	Key   string `json:"key"`
	State string `json:"state"`
}

type ThreadDetail struct {
	Thread    Thread     `json:"thread"`
	Messages  []Message  `json:"messages"`
	Runs      []Run      `json:"runs"`
	Artifacts []Artifact `json:"artifacts"`
	Exports   []Export   `json:"exports"`
	// Seq is the newest event sequence number reflected in this snapshot.
	Seq int64 `json:"seq"`
}

// --- agent runs ---

type Run struct {
	ID               string     `json:"id"`
	TeamID           string     `json:"team_id"`
	ThreadID         string     `json:"thread_id,omitempty"`
	Stage            Stage      `json:"stage,omitempty"`
	Kind             RunKind    `json:"kind"`
	MemberID         string     `json:"member_id"`
	Agent            string     `json:"agent"`
	AgentVersion     string     `json:"agent_version"`
	Status           RunStatus  `json:"status"`
	TriggerMessageID string     `json:"trigger_message_id,omitempty"`
	CommitSHA        string     `json:"commit_sha"` // a SHA, or "name@sha name@sha" with several repos
	Reply            string     `json:"reply"`
	Error            string     `json:"error"`
	CostUSD          float64    `json:"cost_usd"`
	StartedAt        time.Time  `json:"started_at"`
	FinishedAt       *time.Time `json:"finished_at"`
	HeartbeatAt      time.Time  `json:"heartbeat_at"`
}

type CreateRunRequest struct {
	Kind             RunKind `json:"kind"`
	ThreadID         string  `json:"thread_id,omitempty"`
	Stage            Stage   `json:"stage,omitempty"`
	Agent            string  `json:"agent"`
	AgentVersion     string  `json:"agent_version"`
	TriggerMessageID string  `json:"trigger_message_id,omitempty"`
}

// RunOutput is one normalized item of agent activity streamed to the team.
type RunOutput struct {
	Kind   string `json:"kind"` // text | thinking | tool | tool_error | status | error
	Text   string `json:"text,omitempty"`
	Tool   string `json:"tool,omitempty"`
	Detail string `json:"detail,omitempty"`
}

type RunOutputBatch struct {
	RunID string      `json:"run_id"`
	Items []RunOutput `json:"items"`
}

type RunOutputResponse struct {
	Items []RunOutput `json:"items"`
	Seq   int64       `json:"seq"`
}

type FinishRunRequest struct {
	Status    RunStatus        `json:"status"`
	Reply     string           `json:"reply"`
	Error     string           `json:"error"`
	CostUSD   float64          `json:"cost_usd"`
	CommitSHA string           `json:"commit_sha"`
	Artifacts []ArtifactUpload `json:"artifacts"`
}

type CancelRequested struct {
	RunID string `json:"run_id"`
	By    string `json:"by"`
}

// --- realtime ---

const (
	EvThreadCreated   = "thread.created"
	EvThreadUpdated   = "thread.updated"
	EvMessageCreated  = "message.created"
	EvRunStarted      = "run.started"
	EvRunOutput       = "run.output"
	EvRunFinished     = "run.finished"
	EvRunCancel       = "run.cancel_requested"
	EvArtifactUpdated = "artifact.updated"
	EvTeamUpdated     = "team.updated"
	EvMemberJoined    = "member.joined"
	EvMemberUpdated   = "member.updated"
	EvMemberRemoved   = "member.removed"
	EvExportCreated   = "export.created"
	EvExportUpdated   = "export.updated" // issue states read back from the tracker
	// Ephemeral (no seq):
	EvPresence = "presence"
	EvHello    = "hello"
	EvResync   = "resync"
)

type Event struct {
	Seq       int64           `json:"seq"`
	Type      string          `json:"type"`
	ThreadID  string          `json:"thread_id,omitempty"`
	RunID     string          `json:"run_id,omitempty"`
	ActorID   string          `json:"actor_id,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

type PresenceEntry struct {
	MemberID string `json:"member_id"`
	ThreadID string `json:"thread_id,omitempty"`
}

// ClientMessage is sent from clients over the websocket.
type ClientMessage struct {
	Type     string `json:"type"` // presence
	ThreadID string `json:"thread_id,omitempty"`
}

type ErrorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}
