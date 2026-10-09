// Mirrors internal/api/types.go and internal/local state.

export type Role = "product" | "design" | "engineering";
export type Stage = "sparring" | "design" | "technical" | "plan";
export type StageStatus = "locked" | "open" | "approved";
export type ThreadStatus = "active" | "completed" | "archived";
export type RunStatus = "running" | "succeeded" | "failed" | "cancelled" | "abandoned";
export type OutputType = "markdown" | "linear" | "jira";

export interface OutputTarget {
  type: OutputType;
  config: Record<string, string>;
}

/** One repository the team's agents read. The name is unique within the team. */
export interface RepoRef {
  name: string;
  url: string;
  branch: string;
}

export interface Team {
  id: string;
  name: string;
  repos: RepoRef[];
  output: OutputTarget;
  codebase_brief: string;
  rules: string;
  /** A SHA, or "name@sha name@sha" for several repositories (same as Run.commit_sha). */
  scan_commit: string;
  scanned_at: string | null;
  created_at: string;
}

export interface Member {
  id: string;
  team_id: string;
  display_name: string;
  email: string;
  roles: Role[];
  is_admin: boolean;
  created_at: string;
  removed_at?: string | null;
}

export interface Invite {
  id: string;
  roles: Role[];
  is_admin: boolean;
  max_uses: number;
  uses: number;
  expires_at: string;
  created_by: string;
  created_at: string;
  revoked_at?: string | null;
  hint: string;
}

export interface StageState {
  stage: Stage;
  status: StageStatus;
  approved_by?: string;
  approved_at?: string | null;
  selection?: string;
  active_run_id?: string;
}

export interface Thread {
  id: string;
  team_id: string;
  title: string;
  status: ThreadStatus;
  current_stage: Stage;
  created_by: string;
  created_at: string;
  updated_at: string;
  stages: StageState[];
}

export interface Message {
  id: string;
  thread_id: string;
  stage: Stage;
  author_id: string;
  kind: "human" | "agent" | "system";
  body: string;
  run_id?: string;
  created_at: string;
}

export interface Run {
  id: string;
  team_id: string;
  thread_id?: string;
  stage?: Stage;
  kind: "stage" | "scan";
  member_id: string;
  agent: string;
  agent_version: string;
  status: RunStatus;
  trigger_message_id?: string;
  commit_sha: string;
  reply: string;
  error: string;
  cost_usd: number;
  started_at: string;
  finished_at: string | null;
  heartbeat_at: string;
}

export interface RunOutput {
  kind: "text" | "thinking" | "tool" | "tool_error" | "status" | "error";
  text?: string;
  tool?: string;
  detail?: string;
}

export interface Artifact {
  id: string;
  thread_id: string;
  stage: Stage;
  path: string;
  version: number;
  content: string;
  run_id?: string;
  author_id: string;
  created_at: string;
}

export interface ExportItem {
  ref?: string;
  title: string;
  key: string;
  url: string;
}

export interface Export {
  id: string;
  thread_id: string;
  target: OutputType;
  member_id: string;
  items: ExportItem[];
  created_at: string;
}

export interface ThreadDetail {
  thread: Thread;
  messages: Message[];
  runs: Run[];
  artifacts: Artifact[];
  exports: Export[];
  seq: number;
}

export interface BraidEvent {
  seq: number;
  type: string;
  thread_id?: string;
  run_id?: string;
  actor_id?: string;
  data?: any;
  created_at: string;
}

export interface PresenceEntry {
  member_id: string;
  thread_id?: string;
}

export interface Profile {
  id: string;
  server_url: string;
  server_id: string;
  team_id: string;
  team_name: string;
  member_id: string;
  device_id: string;
  display_name: string;
  repo_overrides?: Record<string, string>;
  repo_override?: string;
}

export interface AgentInfo {
  id: string;
  name: string;
  available: boolean;
  version: string;
  error?: string;
}

/** This machine's checkout of one team repository. */
export interface RepoStatus {
  name: string;
  state: "none" | "cloning" | "syncing" | "ready" | "override" | "error";
  dir: string;
  commit: string;
  url: string;
  branch: string;
  error?: string;
  synced_at?: string | null;
  /** Local folder this member reads instead of a managed clone. */
  override?: string;
}

export interface StageDescription {
  id: Stage;
  title: string;
  owner: Role | "";
  primary: string;
}

export interface LocalState {
  version: string;
  hostname: string;
  home: string;
  profiles: Profile[];
  active: Profile | null;
  me: Member | null;
  team: Team | null;
  members: Member[];
  connection: "offline" | "connecting" | "connected" | "revoked";
  connection_error?: string;
  agents: AgentInfo[];
  agent: string;
  agent_model: string;
  repos: RepoStatus[];
  secret_backend: string;
  credentials: Record<string, boolean>;
  jira_email: string;
  export_dir: string;
  local_runs: string[];
  stages: StageDescription[];
}

export interface Plan {
  summary: string;
  epic: { title: string; description: string };
  tickets: PlanTicket[];
}

export interface PlanTicket {
  ref: string;
  title: string;
  type: string;
  role: string;
  description: string;
  acceptance_criteria: string[];
  estimate: number;
  labels: string[];
  depends_on: string[];
}
