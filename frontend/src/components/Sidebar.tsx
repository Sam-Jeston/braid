import { useState } from "react";
import { local } from "../api";
import { Icon, Logo } from "../icons";
import { errorToast, go, setState, useStore } from "../store";
import type { Thread } from "../types";
import { Avatar, RoleChip } from "../ui";
import { cx, STAGE_ORDER, STAGES, stageState, timeAgo } from "../util";

export function Sidebar() {
  const l = useStore((s) => s.local)!;
  const threads = useStore((s) => s.threads);
  const route = useStore((s) => s.route);
  const presence = useStore((s) => s.presence);
  const [switcher, setSwitcher] = useState(false);
  const active = threads.filter((t) => t.status === "active");
  const done = threads.filter((t) => t.status === "completed");
  const selected = route.view === "thread" ? route.id : null;

  const viewers = (t: Thread) =>
    presence
      .filter((p) => p.thread_id === t.id && p.member_id !== l.me?.id)
      .map((p) => l.members.find((m) => m.id === p.member_id))
      .filter(Boolean);

  return (
    <aside className="sidebar">
      <div className="team-switch" onClick={() => setSwitcher(!switcher)}>
        <Logo size={28} />
        <div className="team-switch-text">
          <b>{l.team?.name || l.active?.team_name}</b>
          <span className={cx("conn", `conn-${l.connection}`)} title={l.connection_error || l.connection}>
            <i /> {l.connection === "connected" ? new URL(l.active!.server_url).host : l.connection}
          </span>
        </div>
        <Icon name="chevronDown" size={14} />
      </div>
      {switcher && (
        <div className="switcher-menu" onMouseLeave={() => setSwitcher(false)}>
          {l.profiles.map((p) => (
            <button
              key={p.id}
              className={cx("switcher-item", p.id === l.active?.id && "on")}
              onClick={() => {
                setSwitcher(false);
                if (p.id !== l.active?.id) local("POST", `/profiles/${p.id}/activate`).catch(errorToast);
              }}
            >
              <b>{p.team_name}</b>
              <span>{new URL(p.server_url).host}</span>
            </button>
          ))}
          <button
            className="switcher-item add"
            onClick={() => {
              setSwitcher(false);
              setState((s) => ({ local: s.local ? { ...s.local, active: null } : s.local }));
            }}
          >
            <Icon name="plus" size={14} /> Join or create another team
          </button>
        </div>
      )}

      <button className="new-thread" onClick={() => setState({ newThreadOpen: true })} disabled={l.connection !== "connected"}>
        <Icon name="plus" /> New thread
      </button>

      <nav className="thread-nav">
        <button className={cx("nav-item", route.view === "home" && "on")} onClick={() => go({ view: "home" })}>
          <Icon name="layout" /> Overview
        </button>
        <div className="nav-section">Active · {active.length}</div>
        {active.length === 0 && <div className="nav-empty">No threads yet</div>}
        {active.map((t) => (
          <ThreadItem key={t.id} t={t} on={selected === t.id} viewers={viewers(t) as any[]} />
        ))}
        {done.length > 0 && <div className="nav-section">Planned · {done.length}</div>}
        {done.map((t) => (
          <ThreadItem key={t.id} t={t} on={selected === t.id} viewers={viewers(t) as any[]} />
        ))}
      </nav>

      <div className="me-bar">
        <Avatar member={l.me} size={30} />
        <div className="me-text">
          <b>{l.me?.display_name}</b>
          <span>{l.me?.roles.map((r) => <RoleChip key={r} role={r} small />)}</span>
        </div>
        <button
          className={cx("icon-btn", route.view === "settings" && "on")}
          title="Settings"
          onClick={() => go({ view: "settings" })}
        >
          <Icon name="settings" />
        </button>
      </div>
    </aside>
  );
}

function ThreadItem({ t, on, viewers }: { t: Thread; on: boolean; viewers: any[] }) {
  const running = t.stages.some((s) => s.active_run_id);
  return (
    <button className={cx("thread-item", on && "on")} onClick={() => go({ view: "thread", id: t.id })}>
      <div className="thread-item-top">
        <span className="thread-item-title">{t.title}</span>
        {running && <span className="pulse-dot" title="An agent is working" />}
      </div>
      <div className="thread-item-bottom">
        <span className="stage-pips">
          {STAGE_ORDER.map((s) => {
            const st = stageState(t, s);
            return (
              <i
                key={s}
                className={cx("pip", `pip-${st.status}`, `pip-${s}`, st.active_run_id && "pip-running")}
                title={`${STAGES[s].title}: ${st.status}`}
              />
            );
          })}
        </span>
        <span className="muted small">{t.status === "completed" ? "planned" : STAGES[t.current_stage].title}</span>
        <span className="viewers">
          {viewers.slice(0, 3).map((m) => (
            <Avatar key={m.id} member={m} size={16} />
          ))}
        </span>
        <span className="muted small grow-right">{timeAgo(t.updated_at)}</span>
      </div>
    </button>
  );
}
