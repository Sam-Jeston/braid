import { Fragment, useEffect } from "react";
import { Icon } from "../icons";
import { Markdown } from "../markdown";
import { go, loadLatestScan, setState, startScan, useStore } from "../store";
import { Button } from "../ui";
import { agentName, checkoutLabel, cx, shortRevision, STAGE_ORDER, STAGES, stageState, timeAgo } from "../util";
import { RunCard } from "./RunCard";

export function Home() {
  const l = useStore((s) => s.local)!;
  const threads = useStore((s) => s.threads);
  const scanRun = useStore((s) => s.scanRun);
  const team = l.team;
  useEffect(() => {
    if (l.connection === "connected") loadLatestScan().catch(() => {});
  }, [l.connection]);

  const active = threads.filter((t) => t.status === "active");
  const running = active.filter((t) => t.stages.some((s) => s.active_run_id)).length;
  const agent = l.agents.find((a) => a.id === l.agent);
  const hour = new Date().getHours();
  const greet = hour < 12 ? "Good morning" : hour < 18 ? "Good afternoon" : "Good evening";

  return (
    <div className="home">
      <div className="home-hero">
        <div>
          <h1>
            {greet}, {l.me?.display_name.split(" ")[0]}
          </h1>
          <p className="muted">
            {active.length} active thread{active.length === 1 ? "" : "s"}
            {running > 0 && ` · ${running} with agents working right now`}
          </p>
        </div>
        <Button variant="primary" icon="plus" onClick={() => setState({ newThreadOpen: true })}>
          New thread
        </Button>
      </div>

      {threads.length === 0 && (
        <div className="how">
          {[
            { icon: "message", t: "Spar", d: "Product posts an idea. The agent challenges it against the real code." },
            { icon: "layout", t: "Design", d: "The agent proposes HTML mockups built from your UI. Design picks one." },
            { icon: "code", t: "Technical", d: "Engineering steers a code-grounded build plan." },
            { icon: "list", t: "Plan", d: `Approved work becomes ${team?.output.type === "markdown" ? "Markdown" : team?.output.type === "jira" ? "Jira" : "Linear"} tickets.` },
          ].map((s, i) => (
            <div key={s.t} className="how-step">
              <span className="how-num">{i + 1}</span>
              <Icon name={s.icon} size={18} />
              <b>{s.t}</b>
              <p className="muted">{s.d}</p>
            </div>
          ))}
        </div>
      )}

      <div className="home-grid">
        <section className="card">
          <div className="card-head">
            <h3>
              <Icon name="code" /> Codebase
            </h3>
            <Button
              size="sm"
              icon="refresh"
              disabled={scanRun?.status === "running" || !agent?.available || !team?.repos.length}
              onClick={() => startScan()}
            >
              {team?.scanned_at ? "Rescan" : "Scan now"}
            </Button>
          </div>
          <div className="kv">
            {!team?.repos.length && (
              <>
                <span>Repository</span>
                <code>not configured</code>
              </>
            )}
            {team?.repos.map((r) => {
              const st = l.repos.find((x) => x.name === r.name);
              return (
                <Fragment key={r.name}>
                  <span>{team.repos.length > 1 ? r.name : "Repository"}</span>
                  <div className="repo-cell">
                    <code>{r.url}</code>
                    <span className={cx("repo-state", "small", `repo-${st?.state || "none"}`)}>Your checkout: {checkoutLabel(st)}</span>
                  </div>
                </Fragment>
              );
            })}
            <span>Last scan</span>
            <span>{team?.scanned_at ? `${timeAgo(team.scanned_at)} at ${shortRevision(team.scan_commit)}` : "never"}</span>
            <span>Your agent</span>
            <span>
              {agent?.available ? (
                `${agentName(agent.id)} ${agent.version}`
              ) : (
                <button className="link-btn warn" onClick={() => go({ view: "settings", tab: "device" })}>
                  No agent available. Set one up
                </button>
              )}
            </span>
          </div>
          {scanRun?.status === "running" && <RunCard run={scanRun} title="Codebase scan" />}
          {team?.codebase_brief && scanRun?.status !== "running" && (
            <details className="brief-preview">
              <summary>Codebase brief</summary>
              <Markdown text={team.codebase_brief} />
            </details>
          )}
        </section>

        <section className="card">
          <div className="card-head">
            <h3>
              <Icon name="sparkle" /> Recent threads
            </h3>
          </div>
          {threads.length === 0 && <p className="muted">Nothing yet. Start a thread with an idea worth sparring on.</p>}
          <div className="recent">
            {threads.slice(0, 8).map((t) => (
              <button key={t.id} className="recent-item" onClick={() => go({ view: "thread", id: t.id })}>
                <span className="recent-title">{t.title}</span>
                <span className="stage-pips">
                  {STAGE_ORDER.map((s) => (
                    <i key={s} className={cx("pip", `pip-${stageState(t, s).status}`, `pip-${s}`, stageState(t, s).active_run_id && "pip-running")} />
                  ))}
                </span>
                <span className="muted small">{t.status === "completed" ? "planned" : STAGES[t.current_stage].title}</span>
                <span className="muted small">{timeAgo(t.updated_at)}</span>
              </button>
            ))}
          </div>
        </section>
      </div>
    </div>
  );
}
