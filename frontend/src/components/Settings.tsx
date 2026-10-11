import { useEffect, useState } from "react";
import { local, remote } from "../api";
import { Icon } from "../icons";
import { Markdown } from "../markdown";
import { errorToast, go, startScan, toast, useStore } from "../store";
import type { Invite, OutputType, RepoRef, Role, Team } from "../types";
import { Avatar, Badge, Button, CopyButton, Field, Modal, RoleChip, RolePicker, Segmented } from "../ui";
import { cx, outputLabel, shortRevision, timeAgo } from "../util";
import { RepoList } from "./RepoList";
import { RunCard } from "./RunCard";

type Tab = "team" | "members" | "device";

export function Settings({ tab = "team" }: { tab?: Tab }) {
  return (
    <div className="settings">
      <h1>Settings</h1>
      <div className="settings-tabs">
        {(
          [
            ["team", "Team", "users"],
            ["members", "Members & invites", "key"],
            ["device", "This device", "terminal"],
          ] as const
        ).map(([id, label, icon]) => (
          <button key={id} className={cx("doc-tab", tab === id && "on")} onClick={() => go({ view: "settings", tab: id })}>
            <Icon name={icon} size={14} /> {label}
          </button>
        ))}
      </div>
      {tab === "team" && <TeamSettings />}
      {tab === "members" && <MembersSettings />}
      {tab === "device" && <DeviceSettings />}
    </div>
  );
}

function TeamSettings() {
  const l = useStore((s) => s.local)!;
  const scanRun = useStore((s) => s.scanRun);
  const team = l.team!;
  const admin = !!l.me?.is_admin;
  const [name, setName] = useState(team.name);
  const [repos, setRepos] = useState<RepoRef[]>(team.repos);
  const [outType, setOutType] = useState<OutputType>(team.output.type);
  const [cfg, setCfg] = useState<Record<string, string>>(team.output.config || {});
  const [saving, setSaving] = useState(false);
  const [editDoc, setEditDoc] = useState<null | "codebase_brief" | "rules">(null);
  const [draft, setDraft] = useState("");

  async function save() {
    setSaving(true);
    try {
      const saved = await remote<Team>("PATCH", "/team", { name, repos, output: { type: outType, config: cfg } });
      setRepos(saved.repos);
      toast("Team settings saved", "success");
    } catch (e) {
      errorToast(e);
    } finally {
      setSaving(false);
    }
  }
  async function saveDoc() {
    try {
      await remote("PATCH", "/team", { [editDoc!]: draft });
      setEditDoc(null);
    } catch (e) {
      errorToast(e);
    }
  }
  const setC = (k: string) => (e: { target: { value: string } }) => setCfg({ ...cfg, [k]: e.target.value });

  return (
    <div className="settings-body">
      <section className="card">
        <h3>Team</h3>
        <Field label="Name">
          <input value={name} disabled={!admin} onChange={(e) => setName(e.target.value)} />
        </Field>
        <RepoList
          value={repos}
          onChange={setRepos}
          disabled={!admin}
          hint="Each member's app keeps its own read-only clone. Most teams need one; add more when your product spans several codebases."
        />
        <Field label="Publish plans to">
          <Segmented
            value={outType}
            onChange={(v) => admin && setOutType(v)}
            options={[
              { value: "linear", label: "Linear" },
              { value: "jira", label: "Jira" },
              { value: "markdown", label: "Markdown" },
            ]}
          />
        </Field>
        {outType === "linear" && (
          <Field label="Linear team key">
            <input className="mono" value={cfg.team_key || ""} disabled={!admin} onChange={setC("team_key")} />
          </Field>
        )}
        {outType === "jira" && (
          <div className="grid-2">
            <Field label="Jira site">
              <input value={cfg.site || ""} disabled={!admin} onChange={setC("site")} placeholder="https://acme.atlassian.net" />
            </Field>
            <Field label="Project key">
              <input className="mono" value={cfg.project_key || ""} disabled={!admin} onChange={setC("project_key")} />
            </Field>
            <Field label="Epic issue type">
              <input value={cfg.epic_type || ""} disabled={!admin} onChange={setC("epic_type")} placeholder="Epic" />
            </Field>
            <Field label="Ticket issue type">
              <input value={cfg.issue_type || ""} disabled={!admin} onChange={setC("issue_type")} placeholder="Task" />
            </Field>
          </div>
        )}
        {admin ? (
          <div className="row-end">
            <Button variant="primary" loading={saving} onClick={save}>
              Save
            </Button>
          </div>
        ) : (
          <p className="muted small">Only admins can change team settings.</p>
        )}
      </section>

      <section className="card">
        <div className="card-head">
          <h3>Shared agent context</h3>
          <Button size="sm" icon="refresh" disabled={scanRun?.status === "running" || !team.repos.length} onClick={() => startScan()}>
            Rescan codebase on this machine
          </Button>
        </div>
        <p className="muted small">
          Every agent run, on anyone's machine, gets these two documents. Last scan: {team.scanned_at ? `${timeAgo(team.scanned_at)} at ${shortRevision(team.scan_commit)}` : "never"}.
        </p>
        {scanRun?.status === "running" && <RunCard run={scanRun} title="Codebase scan" />}
        {(["rules", "codebase_brief"] as const).map((k) => (
          <details key={k} className="brief-preview" open={k === "rules"}>
            <summary>
              {k === "rules" ? "Agent rules" : "Codebase brief"}
              <Button
                size="sm"
                variant="ghost"
                icon="edit"
                onClick={(e) => {
                  e.preventDefault();
                  setDraft(team[k]);
                  setEditDoc(k);
                }}
              >
                Edit
              </Button>
            </summary>
            {team[k] ? <Markdown text={team[k]} /> : <p className="muted">Empty. Run a scan or write it yourself.</p>}
          </details>
        ))}
      </section>
      {editDoc && (
        <Modal
          title={editDoc === "rules" ? "Agent rules" : "Codebase brief"}
          onClose={() => setEditDoc(null)}
          width={820}
          footer={
            <>
              <span className="grow" />
              <Button variant="ghost" onClick={() => setEditDoc(null)}>
                Cancel
              </Button>
              <Button variant="primary" onClick={saveDoc}>
                Save
              </Button>
            </>
          }
        >
          <textarea className="doc-editor mono tall" value={draft} onChange={(e) => setDraft(e.target.value)} />
        </Modal>
      )}
    </div>
  );
}

function MembersSettings() {
  const l = useStore((s) => s.local)!;
  const admin = !!l.me?.is_admin;
  const [invites, setInvites] = useState<Invite[]>([]);
  const [roles, setRoles] = useState<Role[]>(["engineering"]);
  const [inviteAdmin, setInviteAdmin] = useState(false);
  const [uses, setUses] = useState(1);
  const [hours, setHours] = useState(72);
  const [created, setCreated] = useState<{ code: string; invite: Invite } | null>(null);
  const [editing, setEditing] = useState<string | null>(null);

  const load = () => {
    if (admin) remote<Invite[]>("GET", "/invites").then(setInvites).catch(errorToast);
  };
  useEffect(() => {
    load();
  }, [admin]);

  async function createInvite() {
    try {
      const res = await remote<{ code: string; invite: Invite }>("POST", "/invites", { roles, is_admin: inviteAdmin, max_uses: uses, expires_in_hours: hours });
      setCreated(res);
      load();
    } catch (e) {
      errorToast(e);
    }
  }
  const members = l.members.filter((m) => !m.removed_at);
  return (
    <div className="settings-body">
      <section className="card">
        <h3>Members · {members.length}</h3>
        <div className="member-list">
          {members.map((m) => (
            <div key={m.id} className="member-row">
              <Avatar member={m} size={30} />
              <div className="member-name">
                <b>
                  {m.display_name} {m.id === l.me?.id && <span className="muted">(you)</span>}
                </b>
                <span className="muted small">{m.email}</span>
              </div>
              {editing === m.id ? (
                <RolePicker
                  value={m.roles}
                  onChange={(r) => r.length && remote("PATCH", `/members/${m.id}`, { roles: r }).catch(errorToast)}
                />
              ) : (
                <span className="chips">
                  {m.roles.map((r) => (
                    <RoleChip key={r} role={r} />
                  ))}
                </span>
              )}
              {m.is_admin && <Badge tone="accent">admin</Badge>}
              {admin && (
                <span className="member-actions">
                  <Button size="sm" variant="ghost" onClick={() => setEditing(editing === m.id ? null : m.id)}>
                    {editing === m.id ? "Done" : "Roles"}
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => remote("PATCH", `/members/${m.id}`, { is_admin: !m.is_admin }).catch(errorToast)}
                  >
                    {m.is_admin ? "Remove admin" : "Make admin"}
                  </Button>
                  {m.id !== l.me?.id && (
                    <Button
                      size="sm"
                      variant="ghost"
                      icon="trash"
                      title="Remove from team (revokes their devices)"
                      onClick={() => confirm(`Remove ${m.display_name} from the team? Their devices lose access immediately.`) && remote("DELETE", `/members/${m.id}`).catch(errorToast)}
                    />
                  )}
                </span>
              )}
            </div>
          ))}
        </div>
      </section>

      {admin && (
        <section className="card">
          <h3>Invite teammates</h3>
          <p className="muted small">Invite codes are single-use by default and only stored hashed on the server.</p>
          <Field label="Roles for the new member">
            <RolePicker value={roles} onChange={setRoles} />
          </Field>
          <div className="grid-3">
            <Field label="Uses">
              <input type="number" min={1} max={100} value={uses} onChange={(e) => setUses(Number(e.target.value))} />
            </Field>
            <Field label="Expires in (hours)">
              <input type="number" min={1} max={720} value={hours} onChange={(e) => setHours(Number(e.target.value))} />
            </Field>
            <label className="check-row self-end">
              <input type="checkbox" checked={inviteAdmin} onChange={(e) => setInviteAdmin(e.target.checked)} /> Admin
            </label>
          </div>
          <div className="row-end">
            <Button variant="primary" icon="plus" disabled={!roles.length} onClick={createInvite}>
              Create invite code
            </Button>
          </div>
          {invites.length > 0 && (
            <div className="invite-list">
              {invites.map((i) => {
                const dead = !!i.revoked_at || new Date(i.expires_at) < new Date() || i.uses >= i.max_uses;
                return (
                  <div key={i.id} className={cx("invite-row", dead && "dead")}>
                    <code>…{i.hint}</code>
                    <span className="chips">
                      {i.roles.map((r) => (
                        <RoleChip key={r} role={r} small />
                      ))}
                    </span>
                    <span className="muted small">
                      {i.uses}/{i.max_uses} used · {i.revoked_at ? "revoked" : new Date(i.expires_at) < new Date() ? "expired" : `expires ${new Date(i.expires_at).toLocaleString()}`}
                    </span>
                    {!dead && (
                      <Button size="sm" variant="ghost" onClick={() => remote("DELETE", `/invites/${i.id}`).then(load).catch(errorToast)}>
                        Revoke
                      </Button>
                    )}
                  </div>
                );
              })}
            </div>
          )}
        </section>
      )}
      {created && (
        <Modal title="Invite created" onClose={() => setCreated(null)}>
          <p>Send your teammate these two things. The code is only shown once.</p>
          <div className="invite-share">
            <span className="muted small">Server</span>
            <code>{l.active?.server_url}</code>
            <CopyButton text={l.active?.server_url || ""} />
            <span className="muted small">Invite code</span>
            <code className="big">{created.code}</code>
            <CopyButton text={created.code} />
          </div>
          <p className="muted small">
            Joins as {created.invite.roles.join(", ")}
            {created.invite.is_admin && " (admin)"} · valid for {created.invite.max_uses} use{created.invite.max_uses > 1 && "s"} until{" "}
            {new Date(created.invite.expires_at).toLocaleString()}.
          </p>
        </Modal>
      )}
    </div>
  );
}

function DeviceSettings() {
  const l = useStore((s) => s.local)!;
  const [model, setModel] = useState(l.agent_model);
  const [overrides, setOverrides] = useState<Record<string, string>>({});
  const [linear, setLinear] = useState("");
  const [jiraEmail, setJiraEmail] = useState(l.jira_email);
  const [jiraToken, setJiraToken] = useState("");
  const [exportDir, setExportDir] = useState(l.export_dir);
  const [syncing, setSyncing] = useState(false);
  const target = l.team?.output.type;
  const repos = l.team?.repos || [];

  const patch = (body: object, msg = "Saved") =>
    local("PUT", "/settings", body)
      .then(() => toast(msg, "success"))
      .catch(errorToast);
  const cred = (key: string, value: string) =>
    local("PUT", "/credentials", { key, value })
      .then(() => toast(value ? "Stored in " + (l.secret_backend === "keychain" ? "your OS keychain" : "Braid's local secrets file") : "Removed", "success"))
      .catch(errorToast);

  return (
    <div className="settings-body">
      <section className="card">
        <div className="card-head">
          <h3>Coding agent</h3>
          <Button size="sm" variant="ghost" icon="refresh" onClick={() => local("POST", "/agents/refresh").catch(errorToast)}>
            Detect again
          </Button>
        </div>
        <p className="muted small">Agents run on this machine with your own subscription or API key. Braid never sends code to the server, only the documents and activity the agent produces.</p>
        {l.agent_hosts.length > 1 && (
          <Field label="Run agents in" hint="In WSL, Braid uses the Claude Code, Codex and git logins inside that Linux distribution, and keeps its clones there.">
            <div className="agent-options">
              {l.agent_hosts.map((h) => (
                <button key={h.id} className={cx("agent-option", l.agent_host === h.id && "on")} onClick={() => patch({ agent_host: h.id }, `Agents now run in ${h.label}`)}>
                  <Icon name={l.agent_host === h.id ? "check" : h.id.startsWith("wsl:") ? "terminal" : "server"} />
                  <div>
                    <b>{h.label}</b>
                  </div>
                </button>
              ))}
            </div>
          </Field>
        )}
        <div className="agent-options">
          {l.agents.map((a) => (
            <button key={a.id} className={cx("agent-option", l.agent === a.id && "on", !a.available && "unavailable")} disabled={!a.available} onClick={() => patch({ agent: a.id }, `${a.name} selected`)}>
              <Icon name={l.agent === a.id ? "check" : "bolt"} />
              <div>
                <b>{a.name}</b>
                <span className="muted small">{a.available ? `v${a.version}` : a.error || "not installed"}</span>
              </div>
            </button>
          ))}
        </div>
        <Field label="Model override (optional)" hint="Passed to the agent CLI as --model / -m. Leave blank for its default.">
          <div className="inline-input">
            <input className="mono" value={model} onChange={(e) => setModel(e.target.value)} placeholder="e.g. sonnet, opus, gpt-5" />
            <Button size="sm" onClick={() => patch({ agent_model: model })}>
              Save
            </Button>
          </div>
        </Field>
      </section>

      <section className="card">
        <div className="card-head">
          <h3>{repos.length > 1 ? "Codebase checkouts" : "Codebase checkout"}</h3>
          <Button
            size="sm"
            icon="refresh"
            loading={syncing}
            onClick={async () => {
              setSyncing(true);
              await local("POST", "/repo/sync").catch(errorToast);
              setSyncing(false);
            }}
          >
            Sync now
          </Button>
        </div>
        {repos.length === 0 && <p className="muted small">Your team hasn't added a repository yet.</p>}
        {repos.map((r) => {
          const st = l.repos.find((x) => x.name === r.name);
          const override = overrides[r.name] ?? st?.override ?? "";
          return (
            <div key={r.name} className="checkout">
              {repos.length > 1 && <h4 className="mono">{r.name}</h4>}
              <div className="kv">
                <span>Status</span>
                <span className={cx("repo-state", `repo-${st?.state || "none"}`)}>{st?.error || st?.state || "none"}</span>
                <span>Location</span>
                <code>{st?.dir || "—"}</code>
                <span>Commit</span>
                <code>{st?.commit || "—"}</code>
              </div>
              <Field
                label="Use an existing local checkout instead"
                hint={`Braid reads it as-is and never resets it. Leave blank to let Braid manage its own clone.${l.agent_host.startsWith("wsl:") ? " Paths inside WSL, like /home/you/code/app, work too." : ""}`}
              >
                <div className="inline-input">
                  <input className="mono" value={override} onChange={(e) => setOverrides({ ...overrides, [r.name]: e.target.value })} placeholder={`/home/you/code/${r.name}`} />
                  <Button size="sm" onClick={() => patch({ repo_overrides: { [r.name]: override } })}>
                    Save
                  </Button>
                </div>
              </Field>
            </div>
          );
        })}
      </section>

      <section className="card">
        <h3>Tracker credentials</h3>
        <p className="muted small">
          <Icon name="key" size={12} /> Stored in {l.secret_backend === "keychain" ? "your OS keychain" : `${l.home}/secrets.json (mode 0600)`}. Used only when you publish a plan.
          {target && <> Your team publishes to <b>{outputLabel(target)}</b>.</>}
        </p>
        <Field
          label={
            <>
              Linear API key {l.credentials["linear-api-key"] && <Badge tone="ok">saved</Badge>}
            </>
          }
          hint="Linear → Settings → Security & access → Personal API keys"
        >
          <div className="inline-input">
            <input type="password" value={linear} onChange={(e) => setLinear(e.target.value)} placeholder={l.credentials["linear-api-key"] ? "••••••••" : "lin_api_…"} />
            <Button size="sm" onClick={() => cred("linear-api-key", linear).then(() => setLinear(""))}>
              {linear ? "Save" : "Clear"}
            </Button>
          </div>
        </Field>
        <div className="grid-2">
          <Field label="Jira email">
            <div className="inline-input">
              <input value={jiraEmail} onChange={(e) => setJiraEmail(e.target.value)} />
              <Button size="sm" onClick={() => patch({ jira_email: jiraEmail })}>
                Save
              </Button>
            </div>
          </Field>
          <Field
            label={
              <>
                Jira API token {l.credentials["jira-api-token"] && <Badge tone="ok">saved</Badge>}
              </>
            }
            hint="id.atlassian.com → Security → API tokens"
          >
            <div className="inline-input">
              <input type="password" value={jiraToken} onChange={(e) => setJiraToken(e.target.value)} placeholder={l.credentials["jira-api-token"] ? "••••••••" : ""} />
              <Button size="sm" onClick={() => cred("jira-api-token", jiraToken).then(() => setJiraToken(""))}>
                {jiraToken ? "Save" : "Clear"}
              </Button>
            </div>
          </Field>
        </div>
        <Field label="Markdown export folder">
          <div className="inline-input">
            <input className="mono" value={exportDir} onChange={(e) => setExportDir(e.target.value)} />
            <Button size="sm" onClick={() => patch({ export_dir: exportDir })}>
              Save
            </Button>
          </div>
        </Field>
      </section>

      <section className="card danger-zone">
        <h3>This device</h3>
        <div className="kv">
          <span>Device</span>
          <code>{l.hostname}</code>
          <span>Server</span>
          <code>{l.active?.server_url}</code>
          <span>Braid</span>
          <code>
            {l.version}
            {l.server_version && ` (server ${l.server_version})`}
          </code>
        </div>
        <div className="row-end">
          <Button
            variant="danger"
            onClick={() =>
              confirm(`Leave ${l.team?.name} on this device? Its key is revoked and deleted.`) &&
              local("DELETE", `/profiles/${l.active!.id}`).catch(errorToast)
            }
          >
            Leave team on this device
          </Button>
        </div>
      </section>
    </div>
  );
}
