import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { remote } from "../api";
import { Icon } from "../icons";
import { Markdown } from "../markdown";
import { errorToast, go, selectStage, startRun, useStore } from "../store";
import type { Message, Run, Stage, Thread, ThreadDetail } from "../types";
import { Avatar, Button, RoleChip, Spinner } from "../ui";
import { agentName, cx, memberName, STAGE_ORDER, STAGES, stageState, timeAgo } from "../util";
import { DocPanel } from "./DocPanel";
import { RunCard } from "./RunCard";

export function ThreadView({ id, stage: routeStage }: { id: string; stage?: Stage }) {
  const detail = useStore((s) => s.details[id]);
  const thread = useStore((s) => s.threads.find((t) => t.id === id));
  if (!detail) {
    return (
      <div className="center-fill">
        <Spinner size={20} />
      </div>
    );
  }
  const t = detail.thread;
  const stage = routeStage || t.current_stage;
  return (
    <div className="thread-view">
      <ThreadHeader t={thread || t} />
      <StageStepper t={t} stage={stage} />
      <div className="thread-body">
        <section className="feed-col">
          <Feed detail={detail} stage={stage} />
          <Composer t={t} stage={stage} />
        </section>
        <section className="doc-col">
          <DocPanel detail={detail} stage={stage} />
        </section>
      </div>
    </div>
  );
}

function ThreadHeader({ t }: { t: Thread }) {
  const l = useStore((s) => s.local);
  const presence = useStore((s) => s.presence);
  const viewers = presence.filter((p) => p.thread_id === t.id).map((p) => l?.members.find((m) => m.id === p.member_id));
  const [editing, setEditing] = useState(false);
  const [title, setTitle] = useState(t.title);
  async function save() {
    setEditing(false);
    if (title.trim() && title !== t.title) await remote("PATCH", `/threads/${t.id}`, { title }).catch(errorToast);
  }
  return (
    <header className="thread-header">
      <div className="thread-title">
        {editing ? (
          <input autoFocus value={title} onChange={(e) => setTitle(e.target.value)} onBlur={save} onKeyDown={(e) => e.key === "Enter" && save()} />
        ) : (
          <h1 onDoubleClick={() => (setTitle(t.title), setEditing(true))} title="Double-click to rename">
            {t.title}
          </h1>
        )}
        <span className="muted small">
          Started by {memberName(l, t.created_by)} · {timeAgo(t.created_at)}
          {t.status === "completed" && " · Shipped"}
        </span>
      </div>
      <div className="viewers-lg">
        {viewers.map((m, i) => m && <Avatar key={m.id + i} member={m} size={26} title={`${m.display_name} is here`} />)}
      </div>
      {t.status !== "archived" ? (
        <Button
          size="sm"
          variant="ghost"
          icon="archive"
          title="Archive thread"
          onClick={async () => {
            await remote("PATCH", `/threads/${t.id}`, { status: "archived" }).catch(errorToast);
            go({ view: "home" });
          }}
        />
      ) : null}
    </header>
  );
}

function StageStepper({ t, stage }: { t: Thread; stage: Stage }) {
  const l = useStore((s) => s.local);
  return (
    <div className="stepper">
      {STAGE_ORDER.map((s, i) => {
        const st = stageState(t, s);
        const def = STAGES[s];
        return (
          <button
            key={s}
            className={cx("step", `step-${st.status}`, `step-${s}`, s === stage && "on", !!st.active_run_id && "running")}
            onClick={() => selectStage(s)}
          >
            <span className="step-num">
              {st.status === "approved" ? <Icon name="check" size={13} /> : st.status === "locked" ? <Icon name="lock" size={12} /> : i + 1}
            </span>
            <span className="step-text">
              <b>{def.title}</b>
              <small>
                {st.active_run_id
                  ? "agent working…"
                  : st.status === "approved"
                    ? s === "plan"
                      ? "published"
                      : `approved by ${memberName(l, st.approved_by)}`
                    : st.status === "open"
                      ? def.owner
                        ? `${def.owner} owns`
                        : "ready"
                      : "locked"}
              </small>
            </span>
            {i < STAGE_ORDER.length - 1 && <span className="step-line" />}
          </button>
        );
      })}
    </div>
  );
}

type FeedItem = { at: string; kind: "msg"; m: Message } | { at: string; kind: "run"; r: Run };

function Feed({ detail, stage }: { detail: ThreadDetail; stage: Stage }) {
  const l = useStore((s) => s.local);
  const endRef = useRef<HTMLDivElement>(null);
  const t = detail.thread;
  const st = stageState(t, stage);

  const items = useMemo(() => {
    const out: FeedItem[] = [];
    for (const m of detail.messages) if (m.stage === stage && m.kind !== "agent") out.push({ at: m.created_at, kind: "msg", m });
    for (const r of detail.runs) if (r.stage === stage) out.push({ at: r.started_at, kind: "run", r });
    return out.sort((a, b) => a.at.localeCompare(b.at));
  }, [detail.messages, detail.runs, stage]);

  const replies = useMemo(() => {
    const m: Record<string, Message> = {};
    for (const x of detail.messages) if (x.run_id) m[x.run_id] = x;
    return m;
  }, [detail.messages]);

  const touched = useMemo(() => {
    const m: Record<string, string[]> = {};
    for (const a of detail.artifacts) if (a.run_id) (m[a.run_id] ||= []).push(a.path);
    return m;
  }, [detail.artifacts]);

  const lastLen = useRef(0);
  useEffect(() => {
    if (items.length !== lastLen.current) endRef.current?.scrollIntoView({ block: "end" });
    lastLen.current = items.length;
  }, [items.length]);

  // Keep the kickoff visible until a run has actually produced something.
  const hasRuns = items.some((i) => i.kind === "run" && (i.r.status === "running" || i.r.status === "succeeded"));
  return (
    <div className="feed">
      <div className="feed-intro">
        <span className={cx("stage-badge", `stage-${stage}`)}>{STAGES[stage].title}</span>
        <span className="muted">{STAGES[stage].blurb}</span>
      </div>
      {st.status === "locked" && (
        <div className="locked-note">
          <Icon name="lock" /> This stage opens when {STAGES[STAGE_ORDER[STAGE_ORDER.indexOf(stage) - 1]].title} is approved.
        </div>
      )}
      {items.map((it) =>
        it.kind === "msg" ? (
          it.m.kind === "system" ? (
            <div key={it.m.id} className="system-line">
              <Icon name={it.m.body.startsWith("approved") || it.m.body.startsWith("published") ? "check" : "refresh"} size={12} />
              <b>{memberName(l, it.m.author_id)}</b> {it.m.body} · {timeAgo(it.m.created_at)}
            </div>
          ) : (
            <HumanMessage key={it.m.id} m={it.m} />
          )
        ) : (
          <RunCard key={it.r.id} run={it.r} reply={replies[it.r.id]} touched={touched[it.r.id]} />
        ),
      )}
      {st.status === "open" && !hasRuns && t.status === "active" && <Kickoff t={t} stage={stage} hasMessages={items.length > 0} />}
      <div ref={endRef} />
    </div>
  );
}

function HumanMessage({ m }: { m: Message }) {
  const l = useStore((s) => s.local);
  const author = l?.members.find((x) => x.id === m.author_id);
  return (
    <div className={cx("msg", l?.me?.id === m.author_id && "mine")}>
      <Avatar member={author} size={28} />
      <div className="msg-main">
        <div className="msg-meta">
          <b>{author?.display_name || "Someone"}</b>
          {author?.roles.map((r) => <RoleChip key={r} role={r} small />)}
          <span className="muted small">{timeAgo(m.created_at)}</span>
        </div>
        <Markdown text={m.body} />
      </div>
    </div>
  );
}

function Kickoff({ t, stage, hasMessages }: { t: Thread; stage: Stage; hasMessages: boolean }) {
  const l = useStore((s) => s.local);
  const [busy, setBusy] = useState(false);
  const agent = l?.agents.find((a) => a.id === l?.agent);
  const def = STAGES[stage];
  const lastMsg = useStore((s) => {
    const d = s.details[t.id];
    const msgs = d?.messages.filter((m) => m.stage === stage && m.kind === "human") || [];
    return msgs[msgs.length - 1];
  });
  return (
    <div className="kickoff">
      <div className="kickoff-icon">
        <Icon name="sparkle" size={20} />
      </div>
      <div>
        <h3>{def.verb}</h3>
        <p className="muted">
          {stage === "sparring" && hasMessages
            ? "The idea is posted. Ask the agent to pressure-test it against the codebase."
            : def.blurb}{" "}
          It runs on your machine with {agent?.available ? agentName(agent.id) : "your coding agent"}, and the whole team watches live.
        </p>
      </div>
      <Button
        variant="primary"
        icon="play"
        loading={busy}
        disabled={!agent?.available}
        title={agent?.available ? "" : "Pick an available agent in Settings → This device"}
        onClick={async () => {
          setBusy(true);
          await startRun(t.id, stage, stage === "sparring" ? lastMsg?.id : undefined);
          setBusy(false);
        }}
      >
        Run agent
      </Button>
    </div>
  );
}

const PLACEHOLDER: Record<Stage, string> = {
  sparring: "Challenge the idea, add context, or answer the agent's questions…",
  design: "Give design direction: what to explore, change or drop…",
  technical: "Add constraints, ask about trade-offs, or steer the approach…",
  plan: "Ask for changes to the ticket breakdown…",
};

function Composer({ t, stage }: { t: Thread; stage: Stage }) {
  const l = useStore((s) => s.local);
  const [text, setText] = useState("");
  const [ask, setAsk] = useState(true);
  const [busy, setBusy] = useState(false);
  const st = stageState(t, stage);
  const agent = l?.agents.find((a) => a.id === l?.agent);
  const locked = st.status === "locked" || t.status !== "active";
  const canAsk = st.status === "open" && !st.active_run_id && !!agent?.available;
  const disabledReason = st.active_run_id
    ? "An agent is already working on this stage"
    : st.status !== "open"
      ? "Agents only run while a stage is open"
      : !agent?.available
        ? "No coding agent available on this machine"
        : "";

  async function send() {
    if (!text.trim() || busy) return;
    setBusy(true);
    try {
      const m = await remote<Message>("POST", `/threads/${t.id}/messages`, { stage, body: text.trim() });
      setText("");
      if (ask && canAsk) await startRun(t.id, stage, m.id);
    } catch (e) {
      errorToast(e);
    } finally {
      setBusy(false);
    }
  }
  function onKey(e: KeyboardEvent<HTMLTextAreaElement>) {
    if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
      e.preventDefault();
      send();
    }
  }
  if (locked) return null;
  return (
    <div className="composer">
      <textarea
        value={text}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={onKey}
        placeholder={st.status === "approved" ? "This stage is approved. You can still leave comments…" : PLACEHOLDER[stage]}
        rows={3}
      />
      <div className="composer-bar">
        <label className={cx("ask-toggle", !canAsk && "disabled")} title={disabledReason}>
          <input type="checkbox" checked={ask && canAsk} disabled={!canAsk} onChange={(e) => setAsk(e.target.checked)} />
          <Icon name="bolt" size={13} />
          {canAsk && agent ? <span>Ask {agentName(agent.id)} to respond</span> : <span className="muted">{disabledReason}</span>}
        </label>
        <span className="muted small">Ctrl/⌘ + Enter</span>
        <Button variant="primary" size="sm" icon="send" loading={busy} disabled={!text.trim()} onClick={send}>
          Send
        </Button>
      </div>
    </div>
  );
}
