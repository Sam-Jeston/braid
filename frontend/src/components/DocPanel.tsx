import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { local, openExternal, remote } from "../api";
import { Icon } from "../icons";
import { Markdown } from "../markdown";
import { errorToast, go, startRun, toast, useStore } from "../store";
import type { Artifact, Plan, Stage, Thread, ThreadDetail } from "../types";
import { Badge, Button, Empty, Modal, RoleChip } from "../ui";
import { capitalize, cx, fileLabel, hasRole, memberName, outputLabel, possessive, STAGE_ORDER, STAGES, stageState, timeAgo } from "../util";

type Tab = { id: string; label: string; icon: string };

function tabsFor(stage: Stage, docs: Artifact[]): Tab[] {
  switch (stage) {
    case "sparring":
      return [{ id: "brief.md", label: "Product brief", icon: "file" }];
    case "design": {
      const n = docs.filter((a) => a.path.startsWith("mockups/")).length;
      return [
        { id: "variations", label: `Variations${n ? ` · ${n}` : ""}`, icon: "layout" },
        { id: "design.md", label: "Rationale", icon: "file" },
      ];
    }
    case "technical":
      return [{ id: "technical.md", label: "Technical plan", icon: "code" }];
    case "plan":
      return [
        { id: "plan", label: "Tickets", icon: "list" },
        { id: "plan.md", label: "Summary", icon: "file" },
      ];
  }
}

export function DocPanel({ detail, stage }: { detail: ThreadDetail; stage: Stage }) {
  const docs = useMemo(() => detail.artifacts.filter((a) => a.stage === stage), [detail.artifacts, stage]);
  const tabs = tabsFor(stage, docs);
  const [tab, setTab] = useState(tabs[0].id);
  useEffect(() => setTab(tabsFor(stage, [])[0].id), [stage]);
  const current = tabs.find((t) => t.id === tab) ? tab : tabs[0].id;

  return (
    <div className="doc-panel">
      <div className="doc-tabs">
        {tabs.map((t) => (
          <button key={t.id} className={cx("doc-tab", current === t.id && "on")} onClick={() => setTab(t.id)}>
            <Icon name={t.icon} size={14} /> {t.label}
          </button>
        ))}
      </div>
      <div className="doc-content">
        {current === "variations" ? (
          <DesignGallery detail={detail} docs={docs} />
        ) : current === "plan" ? (
          <PlanView detail={detail} />
        ) : (
          <DocView detail={detail} stage={stage} path={current} art={docs.find((a) => a.path === current)} />
        )}
      </div>
      <StageActions detail={detail} stage={stage} />
    </div>
  );
}

function canEdit(t: Thread, stage: Stage) {
  const st = stageState(t, stage);
  return t.status === "active" && st.status === "open" && !st.active_run_id;
}

function DocMeta({ art }: { art: Artifact }) {
  const l = useStore((s) => s.local);
  return (
    <span className="muted small">
      v{art.version} · {art.run_id ? `${capitalize(possessive(l, art.author_id))} agent` : memberName(l, art.author_id)} · {timeAgo(art.created_at)}
    </span>
  );
}

function DocView({ detail, stage, path, art }: { detail: ThreadDetail; stage: Stage; path: string; art?: Artifact }) {
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState("");
  const [saving, setSaving] = useState(false);
  const [history, setHistory] = useState<Artifact[] | null>(null);
  const [viewing, setViewing] = useState<Artifact | null>(null);
  const editable = canEdit(detail.thread, stage);
  useEffect(() => {
    setEditing(false);
    setViewing(null);
    setHistory(null);
  }, [path, stage]);

  async function openHistory() {
    try {
      setHistory(await remote<Artifact[]>("GET", `/threads/${detail.thread.id}/artifacts/history?stage=${stage}&path=${encodeURIComponent(path)}`));
    } catch (e) {
      errorToast(e);
    }
  }
  async function save() {
    setSaving(true);
    try {
      await remote("POST", `/threads/${detail.thread.id}/artifacts`, { stage, path, content: draft });
      setEditing(false);
      toast("Saved. The next agent run will build on your edits.", "success");
    } catch (e) {
      errorToast(e);
    } finally {
      setSaving(false);
    }
  }

  if (!art && !editing) {
    return (
      <Empty icon="file" title={`No ${fileLabel(path)} yet`}>
        <p className="muted">
          {stageState(detail.thread, stage).status === "locked" ? "This stage hasn't started." : "The agent writes this document when it runs. You can also start it by hand."}
        </p>
        {editable && (
          <Button
            size="sm"
            icon="edit"
            onClick={() => {
              setDraft("");
              setEditing(true);
            }}
          >
            Write it yourself
          </Button>
        )}
      </Empty>
    );
  }
  const shown = viewing || art!;
  return (
    <div className="doc-view">
      <div className="doc-toolbar">
        <span className="mono doc-path">{path}</span>
        {shown && <DocMeta art={shown} />}
        <span className="grow" />
        {viewing && (
          <Button size="sm" variant="ghost" onClick={() => setViewing(null)}>
            Back to latest
          </Button>
        )}
        {!editing && art && art.version > 1 && (
          <Button size="sm" variant="ghost" icon="history" onClick={openHistory}>
            History
          </Button>
        )}
        {!editing && editable && !viewing && (
          <Button
            size="sm"
            icon="edit"
            onClick={() => {
              setDraft(art?.content || "");
              setEditing(true);
            }}
          >
            Edit
          </Button>
        )}
        {editing && (
          <>
            <Button size="sm" variant="ghost" onClick={() => setEditing(false)}>
              Cancel
            </Button>
            <Button size="sm" variant="primary" loading={saving} onClick={save}>
              Save
            </Button>
          </>
        )}
      </div>
      {history && (
        <div className="history-strip">
          {history.map((h) => (
            <button key={h.id} className={cx("history-chip", (viewing?.id || art?.id) === h.id && "on")} onClick={() => setViewing(h.id === art?.id ? null : h)}>
              v{h.version} <span className="muted">{timeAgo(h.created_at)}</span>
            </button>
          ))}
          <button className="icon-btn" onClick={() => setHistory(null)}>
            <Icon name="x" size={12} />
          </button>
        </div>
      )}
      {editing ? (
        <textarea className="doc-editor mono" value={draft} onChange={(e) => setDraft(e.target.value)} autoFocus />
      ) : (
        <Markdown text={shown.content} className="doc-md" />
      )}
    </div>
  );
}

// --- design ---

function MockupFrame({ html, title }: { html: string; title: string }) {
  const ref = useRef<HTMLDivElement>(null);
  const [scale, setScale] = useState(0.3);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setScale(el.clientWidth / 1280));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  return (
    <div className="mockup-frame" ref={ref}>
      {/* sandbox="" = no scripts, no same-origin, no navigation: mockups are inert. */}
      <iframe sandbox="" srcDoc={html} title={title} tabIndex={-1} style={{ transform: `scale(${scale})` }} />
    </div>
  );
}

function DesignGallery({ detail, docs }: { detail: ThreadDetail; docs: Artifact[] }) {
  const l = useStore((s) => s.local);
  const [full, setFull] = useState<Artifact | null>(null);
  const t = detail.thread;
  const st = stageState(t, "design");
  const mockups = docs.filter((a) => a.path.startsWith("mockups/")).sort((a, b) => a.path.localeCompare(b.path));
  const canSelect = hasRole(l?.me, "design") && st.status === "open" && t.status === "active";

  async function select(path: string) {
    try {
      await remote("POST", `/threads/${t.id}/stages/design/selection`, { path });
    } catch (e) {
      errorToast(e);
    }
  }
  if (mockups.length === 0) {
    return (
      <Empty icon="layout" title="No variations yet">
        <p className="muted">When the design stage runs, the agent writes 2–3 HTML mockups based on your product's existing UI.</p>
      </Empty>
    );
  }
  return (
    <div className="gallery">
      {mockups.map((a) => {
        const selected = st.selection === a.path;
        return (
          <div key={a.path} className={cx("mockup-card", selected && "selected")}>
            <button className="mockup-open" onClick={() => setFull(a)} title="Open full size">
              <MockupFrame html={a.content} title={a.path} />
            </button>
            <div className="mockup-meta">
              <div>
                <b>{fileLabel(a.path)}</b>
                <DocMeta art={a} />
              </div>
              <span className="grow" />
              {selected && (
                <Badge tone="accent" icon="check">
                  Selected
                </Badge>
              )}
              {canSelect && !selected && (
                <Button size="sm" onClick={() => select(a.path)}>
                  Select
                </Button>
              )}
              <Button size="sm" variant="ghost" icon="maximize" onClick={() => setFull(a)} />
            </div>
          </div>
        );
      })}
      {!st.selection && st.status === "open" && (
        <p className="muted small gallery-hint">
          <Icon name="alert" size={12} /> A designer selects the direction before Design can be approved.
        </p>
      )}
      {full && (
        <Modal
          title={fileLabel(full.path)}
          onClose={() => setFull(null)}
          width={1320}
          footer={
            canSelect && st.selection !== full.path ? (
              <Button
                variant="primary"
                icon="check"
                onClick={() => {
                  select(full.path);
                  setFull(null);
                }}
              >
                Select this variation
              </Button>
            ) : undefined
          }
        >
          <iframe className="mockup-full" sandbox="" srcDoc={full.content} title={full.path} />
        </Modal>
      )}
    </div>
  );
}

// --- plan ---

function PlanView({ detail }: { detail: ThreadDetail }) {
  const art = detail.artifacts.find((a) => a.stage === "plan" && a.path === "plan.json");
  const [open, setOpen] = useState<string | null>(null);
  const plan = useMemo<Plan | null>(() => {
    try {
      return art ? JSON.parse(art.content) : null;
    } catch {
      return null;
    }
  }, [art]);
  if (!art || !plan) {
    return (
      <Empty icon="list" title="No plan yet">
        <p className="muted">Once every stage is approved, an agent turns the brief, design and technical plan into tickets.</p>
      </Empty>
    );
  }
  const points = plan.tickets.reduce((n, t) => n + (t.estimate || 0), 0);
  const exp = detail.exports[detail.exports.length - 1];
  const keyFor = (ref: string) => exp?.items.find((i) => i.ref === ref);
  return (
    <div className="plan-view">
      <div className="epic-card">
        <div className="epic-top">
          <Badge tone="accent">Epic</Badge>
          <span className="muted small">
            {plan.tickets.length} tickets · {points} points
          </span>
          {exp && keyFor("epic") && (
            <button className="link-btn" onClick={() => openExternal(keyFor("epic")!.url, errorToast)}>
              {keyFor("epic")!.key} <Icon name="external" size={12} />
            </button>
          )}
        </div>
        <h2>{plan.epic.title}</h2>
        {plan.summary && <Markdown text={plan.summary} />}
      </div>
      <div className="tickets">
        {plan.tickets.map((t) => {
          const created = keyFor(t.ref);
          return (
            <div key={t.ref} className={cx("ticket", open === t.ref && "open")}>
              <button className="ticket-head" onClick={() => setOpen(open === t.ref ? null : t.ref)}>
                <span className="ticket-ref mono">{created?.key || t.ref}</span>
                <span className="ticket-title">{t.title}</span>
                {t.role && ["product", "design", "engineering"].includes(t.role) && <RoleChip role={t.role as any} small />}
                <span className="ticket-type">{t.type}</span>
                {t.depends_on?.length > 0 && <span className="muted small">after {t.depends_on.join(", ")}</span>}
                <span className="estimate">{t.estimate}</span>
              </button>
              {open === t.ref && (
                <div className="ticket-body">
                  <Markdown text={t.description} />
                  {t.acceptance_criteria?.length > 0 && (
                    <>
                      <h4>Acceptance criteria</h4>
                      <ul className="ac">
                        {t.acceptance_criteria.map((c, i) => (
                          <li key={i}>
                            <Icon name="check" size={12} /> {c}
                          </li>
                        ))}
                      </ul>
                    </>
                  )}
                  {t.labels?.length > 0 && (
                    <div className="labels">
                      {t.labels.map((x) => (
                        <span key={x} className="label">
                          {x}
                        </span>
                      ))}
                    </div>
                  )}
                  {created && (
                    <Button size="sm" variant="ghost" icon="external" onClick={() => openExternal(created.url, errorToast)}>
                      Open {created.key}
                    </Button>
                  )}
                </div>
              )}
            </div>
          );
        })}
      </div>
    </div>
  );
}

// --- approvals & publishing ---

function StageActions({ detail, stage }: { detail: ThreadDetail; stage: Stage }) {
  const l = useStore((s) => s.local)!;
  const [busy, setBusy] = useState(false);
  const t = detail.thread;
  const st = stageState(t, stage);
  const def = STAGES[stage];
  const me = l.me;

  if (stage === "plan") return <PlanActions detail={detail} />;

  if (st.status === "locked") {
    const prev = STAGES[STAGE_ORDER[STAGE_ORDER.indexOf(stage) - 1]].title;
    return (
      <div className="stage-actions">
        <Icon name="lock" /> <span className="muted">Opens when {prev} is approved</span>
      </div>
    );
  }

  const isOwner = hasRole(me, def.owner);
  if (st.status === "approved") {
    return (
      <div className="stage-actions approved">
        <span className="approved-mark">
          <Icon name="check" size={14} />
        </span>
        <span>
          Approved by <b>{memberName(l, st.approved_by)}</b> · {timeAgo(st.approved_at)}
        </span>
        <span className="grow" />
        {isOwner && t.status === "active" && (
          <Button
            size="sm"
            variant="ghost"
            icon="refresh"
            loading={busy}
            title="Reopening locks every later stage until this one is re-approved"
            onClick={async () => {
              setBusy(true);
              await remote("POST", `/threads/${t.id}/stages/${stage}/reopen`).catch(errorToast);
              setBusy(false);
            }}
          >
            Reopen
          </Button>
        )}
      </div>
    );
  }

  const docs = detail.artifacts.filter((a) => a.stage === stage);
  const hasPrimary = docs.some((a) => a.path === def.primary || (stage === "design" && a.path.startsWith("mockups/")));
  const needsSelection = stage === "design" && docs.some((a) => a.path.startsWith("mockups/")) && !st.selection;
  const reason = !isOwner
    ? `Only ${def.owner} can approve ${def.title}`
    : st.active_run_id
      ? "Wait for the agent to finish"
      : !hasPrimary
        ? `Waiting for ${def.primary}`
        : needsSelection
          ? "Select a design variation first"
          : "";

  async function approve() {
    setBusy(true);
    try {
      const thread = await remote<Thread>("POST", `/threads/${t.id}/stages/${stage}/approve`);
      toast(`${def.title} approved`, "success");
      const next = STAGE_ORDER[STAGE_ORDER.indexOf(stage) + 1];
      if (next === "plan" && stageState(thread, "plan").status === "open") {
        toast("Every stage is approved. Generating the delivery plan on your machine…");
        await startRun(t.id, "plan");
      }
      go({ view: "thread", id: t.id, stage: next });
    } catch (e) {
      errorToast(e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="stage-actions">
      <span className="muted small">Owned by</span>
      {def.owner && <RoleChip role={def.owner} />}
      <span className="grow" />
      {reason && <span className="muted small">{reason}</span>}
      <Button variant="approve" icon="check" disabled={!!reason || t.status !== "active"} loading={busy} onClick={approve}>
        Approve {def.title}
      </Button>
    </div>
  );
}

function PlanActions({ detail }: { detail: ThreadDetail }) {
  const l = useStore((s) => s.local)!;
  const [busy, setBusy] = useState<"" | "publish" | "md" | "gen">("");
  const t = detail.thread;
  const st = stageState(t, "plan");
  const target = l.team?.output.type || "markdown";
  const hasPlan = detail.artifacts.some((a) => a.stage === "plan" && a.path === "plan.json");
  const exp = detail.exports[detail.exports.length - 1];
  const agent = l.agents.find((a) => a.id === l.agent);
  const missingCreds =
    (target === "linear" && !l.credentials["linear-api-key"]) || (target === "jira" && (!l.credentials["jira-api-token"] || !l.jira_email));

  if (st.status === "locked") {
    return (
      <div className="stage-actions">
        <Icon name="lock" /> <span className="muted">Generated automatically once Technical is approved</span>
      </div>
    );
  }
  if (exp) {
    return (
      <div className="stage-actions approved">
        <span className="approved-mark">
          <Icon name="check" size={14} />
        </span>
        <span>
          Published to <b>{outputLabel(exp.target)}</b> by {memberName(l, exp.member_id)} · {exp.items.length} items
        </span>
        <span className="grow" />
        {exp.items[0] && (
          <Button size="sm" variant="ghost" icon="external" onClick={() => openExternal(exp.items[0].url, errorToast)}>
            Open
          </Button>
        )}
      </div>
    );
  }
  async function publish() {
    setBusy("publish");
    try {
      const res = await local<{ export: { items: unknown[] }; folder?: string }>("POST", `/threads/${t.id}/publish`);
      toast(`Published ${res.export.items.length} items to ${outputLabel(target)}${res.folder ? ` in ${res.folder}` : ""}`, "success");
      if (res.folder) openExternal(res.folder);
    } catch (e) {
      errorToast(e);
    } finally {
      setBusy("");
    }
  }
  async function saveMd() {
    setBusy("md");
    try {
      const res = await local<{ folder: string }>("POST", `/threads/${t.id}/save-markdown`);
      toast(`Saved to ${res.folder}`, "success");
      openExternal(res.folder);
    } catch (e) {
      errorToast(e);
    } finally {
      setBusy("");
    }
  }
  return (
    <div className="stage-actions">
      {!hasPlan ? (
        <>
          <span className="muted small">{st.active_run_id ? "The plan is being generated…" : "No plan yet"}</span>
          <span className="grow" />
          <Button
            variant="primary"
            icon="play"
            loading={busy === "gen"}
            disabled={!!st.active_run_id || !agent?.available}
            onClick={async () => {
              setBusy("gen");
              await startRun(t.id, "plan");
              setBusy("");
            }}
          >
            Generate plan
          </Button>
        </>
      ) : (
        <>
          {missingCreds ? (
            <button className="link-btn warn" onClick={() => go({ view: "settings", tab: "device" })}>
              <Icon name="key" size={13} /> Add your {outputLabel(target)} credentials to publish
            </button>
          ) : (
            <span className="muted small">Publishing uses your own {outputLabel(target)} access, from this machine</span>
          )}
          <span className="grow" />
          {target !== "markdown" && (
            <Button size="sm" variant="ghost" icon="folder" loading={busy === "md"} onClick={saveMd}>
              Save Markdown
            </Button>
          )}
          <Button variant="approve" icon="send" loading={busy === "publish"} disabled={missingCreds || !!st.active_run_id} onClick={publish}>
            Publish to {outputLabel(target)}
          </Button>
        </>
      )}
    </div>
  );
}
