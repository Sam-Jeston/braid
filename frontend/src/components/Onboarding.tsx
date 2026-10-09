import { useState, type FormEvent } from "react";
import { local } from "../api";
import { Icon, Logo } from "../icons";
import { errorToast, loadLocal, toast, useStore } from "../store";
import type { OutputType, RepoRef, Role } from "../types";
import { Button, Field, RolePicker, Segmented } from "../ui";
import { cx } from "../util";
import { emptyRepo, RepoList } from "./RepoList";

type Mode = "welcome" | "join" | "create";

interface Probe {
  url: string;
  info: { product: string; version: string; server_id: string };
}

function useServerCheck() {
  const [status, setStatus] = useState<{ ok: boolean; text: string } | null>(null);
  const [checking, setChecking] = useState(false);
  async function check(url: string, allowInsecure: boolean) {
    if (!url.trim()) return setStatus(null);
    setChecking(true);
    try {
      const p = await local<Probe>("POST", "/probe", { server_url: url, allow_insecure: allowInsecure });
      setStatus({ ok: true, text: `Braid server ${p.info.version} at ${p.url}` });
    } catch (e) {
      setStatus({ ok: false, text: e instanceof Error ? e.message : String(e) });
    } finally {
      setChecking(false);
    }
  }
  return { status, checking, check };
}

function ServerField({
  value,
  onChange,
  allowInsecure,
  setAllowInsecure,
}: {
  value: string;
  onChange: (v: string) => void;
  allowInsecure: boolean;
  setAllowInsecure: (v: boolean) => void;
}) {
  const { status, checking, check } = useServerCheck();
  const insecureLan = /^http:\/\//.test(value) && !/^http:\/\/(localhost|127\.0\.0\.1|\[::1\])/.test(value);
  return (
    <>
      <Field
        label="Server URL"
        hint={
          checking ? (
            "Checking…"
          ) : status ? (
            <span className={status.ok ? "ok-text" : "err-text"}>
              <Icon name={status.ok ? "check" : "alert"} size={12} /> {status.text}
            </span>
          ) : (
            "Your team's self-hosted braid-server, e.g. https://braid.acme.dev"
          )
        }
      >
        <input
          autoFocus
          value={value}
          placeholder="https://braid.yourcompany.com"
          onChange={(e) => onChange(e.target.value)}
          onBlur={() => check(value, allowInsecure)}
        />
      </Field>
      {insecureLan && (
        <label className="check-row">
          <input type="checkbox" checked={allowInsecure} onChange={(e) => setAllowInsecure(e.target.checked)} />
          Allow plain HTTP on my local network (not recommended)
        </label>
      )}
    </>
  );
}

export function Onboarding() {
  const profiles = useStore((s) => s.local?.profiles.length ?? 0);
  const [mode, setMode] = useState<Mode>("welcome");
  return (
    <div className="onboarding">
      <div className="onb-glow" />
      <div className="onb-card">
        <div className="onb-brand">
          <Logo size={40} />
          <div>
            <h1>Braid</h1>
            <p>Product, design and engineering agents on one thread.</p>
          </div>
        </div>
        {mode === "welcome" && (
          <>
            <div className="onb-choices">
              <button className="onb-choice" onClick={() => setMode("join")}>
                <Icon name="users" size={22} />
                <b>Join a team</b>
                <span>You have a server URL and an invite code from a teammate.</span>
              </button>
              <button className="onb-choice" onClick={() => setMode("create")}>
                <Icon name="sparkle" size={22} />
                <b>Create a team</b>
                <span>You run a braid-server and have its setup code.</span>
              </button>
            </div>
            <ul className="onb-points">
              <li>
                <Icon name="bolt" />
                <span>
                  Agents run on <b>your</b> machine with Claude Code or Codex.
                </span>
              </li>
              <li>
                <Icon name="server" />
                <span>Threads, documents and live agent activity sync through your team's own server.</span>
              </li>
              <li>
                <Icon name="key" />
                <span>This device gets its own key, and the server never stores a password.</span>
              </li>
            </ul>
            {profiles > 0 && (
              <Button variant="ghost" onClick={() => loadLocal()}>
                Back to Braid
              </Button>
            )}
          </>
        )}
        {mode === "join" && <JoinForm onBack={() => setMode("welcome")} />}
        {mode === "create" && <CreateForm onBack={() => setMode("welcome")} />}
      </div>
    </div>
  );
}

function JoinForm({ onBack }: { onBack: () => void }) {
  const hostname = useStore((s) => s.local?.hostname);
  const [server, setServer] = useState("");
  const [insecure, setInsecure] = useState(false);
  const [code, setCode] = useState("");
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [busy, setBusy] = useState(false);
  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    try {
      await local("POST", "/join", { server_url: server, allow_insecure: insecure, code, display_name: name, email });
      toast("Welcome aboard! This device is now registered with your team.", "success");
      await loadLocal();
    } catch (err) {
      errorToast(err);
    } finally {
      setBusy(false);
    }
  }
  return (
    <form className="onb-form" onSubmit={submit}>
      <h2>Join your team</h2>
      <ServerField value={server} onChange={setServer} allowInsecure={insecure} setAllowInsecure={setInsecure} />
      <Field label="Invite code" hint="Looks like BRD-7K2M-QX9D-P4TR. Your roles come from the invite.">
        <input value={code} onChange={(e) => setCode(e.target.value)} placeholder="BRD-XXXX-XXXX-XXXX" className="mono" />
      </Field>
      <div className="grid-2">
        <Field label="Your name">
          <input value={name} onChange={(e) => setName(e.target.value)} placeholder="Alex Rivera" />
        </Field>
        <Field label="Email (optional)">
          <input value={email} onChange={(e) => setEmail(e.target.value)} placeholder="alex@company.com" />
        </Field>
      </div>
      <p className="fine">
        Braid will create a signing key for <b>{hostname || "this device"}</b> and register its public half with the server.
      </p>
      <div className="onb-actions">
        <Button type="button" variant="ghost" onClick={onBack}>
          Back
        </Button>
        <Button variant="primary" loading={busy} disabled={!server || !code || !name}>
          Join team
        </Button>
      </div>
    </form>
  );
}

function CreateForm({ onBack }: { onBack: () => void }) {
  const agents = useStore((s) => s.local?.agents || []);
  const [server, setServer] = useState("");
  const [insecure, setInsecure] = useState(false);
  const [code, setCode] = useState("");
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [roles, setRoles] = useState<Role[]>(["product"]);
  const [teamName, setTeamName] = useState("");
  const [repos, setRepos] = useState<RepoRef[]>([emptyRepo()]);
  const [output, setOutput] = useState<OutputType>("linear");
  const [cfg, setCfg] = useState<Record<string, string>>({ epic_type: "Epic", issue_type: "Task" });
  const [scan, setScan] = useState(true);
  const [busy, setBusy] = useState(false);
  const agentReady = agents.some((a) => a.available);
  const hasRepo = repos.some((r) => r.url.trim());
  const setC = (k: string) => (e: { target: { value: string } }) => setCfg({ ...cfg, [k]: e.target.value });

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    try {
      const config: Record<string, string> = {};
      if (output === "linear") config.team_key = cfg.team_key || "";
      if (output === "jira") {
        for (const k of ["site", "project_key", "epic_type", "issue_type"]) config[k] = cfg[k] || "";
      }
      await local("POST", "/create-team", {
        server_url: server,
        allow_insecure: insecure,
        code,
        display_name: name,
        email,
        roles,
        team: { name: teamName, repos, output: { type: output, config } },
      });
      if (scan && hasRepo && agentReady) {
        await local("POST", "/scan").catch(errorToast);
        toast("Team created. Your agent is scanning the codebase on this machine.", "success");
      } else {
        toast("Team created.", "success");
      }
      await loadLocal();
    } catch (err) {
      errorToast(err);
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className="onb-form" onSubmit={submit}>
      <h2>Create a team</h2>
      <section>
        <h3>Server</h3>
        <ServerField value={server} onChange={setServer} allowInsecure={insecure} setAllowInsecure={setInsecure} />
        <Field label="Setup code" hint={<>Printed when braid-server first starts, or run <code>braid-server setup-code</code>.</>}>
          <input value={code} onChange={(e) => setCode(e.target.value)} placeholder="SETUP-XXXX-XXXX-XXXX-XXXX" className="mono" />
        </Field>
      </section>
      <section>
        <h3>You</h3>
        <div className="grid-2">
          <Field label="Your name">
            <input value={name} onChange={(e) => setName(e.target.value)} placeholder="Priya Shah" />
          </Field>
          <Field label="Email (optional)">
            <input value={email} onChange={(e) => setEmail(e.target.value)} />
          </Field>
        </div>
        <Field label="Your roles" hint="Roles decide which stages you can approve. You'll be the team admin.">
          <RolePicker value={roles} onChange={setRoles} />
        </Field>
      </section>
      <section>
        <h3>Team & codebase</h3>
        <Field label="Team name">
          <input value={teamName} onChange={(e) => setTeamName(e.target.value)} placeholder="Payments squad" />
        </Field>
        <RepoList
          value={repos}
          onChange={setRepos}
          hint="Every member's app clones these with their own git access. Agents only read them. Most teams need one."
        />
        {hasRepo && (
          <label className={cx("check-row", !agentReady && "disabled")}>
            <input type="checkbox" checked={scan && agentReady} disabled={!agentReady} onChange={(e) => setScan(e.target.checked)} />
            Scan the codebase now to write the shared codebase brief and agent rules
            {!agentReady && " (install Claude Code or Codex first)"}
          </label>
        )}
      </section>
      <section>
        <h3>Where finished plans go</h3>
        <Segmented
          value={output}
          onChange={setOutput}
          options={[
            { value: "linear", label: "Linear" },
            { value: "jira", label: "Jira" },
            { value: "markdown", label: "Markdown" },
          ]}
        />
        {output === "linear" && (
          <Field label="Linear team key" hint="Tickets are created as sub-issues of an epic issue in this team.">
            <input value={cfg.team_key || ""} onChange={setC("team_key")} placeholder="ENG" className="mono" />
          </Field>
        )}
        {output === "jira" && (
          <>
            <div className="grid-2">
              <Field label="Jira site">
                <input value={cfg.site || ""} onChange={setC("site")} placeholder="https://acme.atlassian.net" />
              </Field>
              <Field label="Project key">
                <input value={cfg.project_key || ""} onChange={setC("project_key")} placeholder="ENG" className="mono" />
              </Field>
            </div>
            <div className="grid-2">
              <Field label="Epic issue type">
                <input value={cfg.epic_type || ""} onChange={setC("epic_type")} />
              </Field>
              <Field label="Ticket issue type">
                <input value={cfg.issue_type || ""} onChange={setC("issue_type")} />
              </Field>
            </div>
          </>
        )}
        {output === "markdown" && <p className="fine">Plans are written as Markdown files to each publisher's machine.</p>}
        {output !== "markdown" && (
          <p className="fine">
            <Icon name="key" size={12} /> API credentials stay on each member's machine (Settings → This device). The server never sees them.
          </p>
        )}
      </section>
      <div className="onb-actions">
        <Button type="button" variant="ghost" onClick={onBack}>
          Back
        </Button>
        <Button variant="primary" loading={busy} disabled={!server || !code || !name || !teamName || roles.length === 0}>
          Create team
        </Button>
      </div>
    </form>
  );
}
