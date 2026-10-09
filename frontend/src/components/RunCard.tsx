import { useEffect, useState } from "react";
import { local } from "../api";
import { Icon } from "../icons";
import { Markdown } from "../markdown";
import { ensureRunOutput, errorToast, useStore } from "../store";
import type { Message, Run, RunOutput } from "../types";
import { Avatar, Badge, Button } from "../ui";
import { agentName, cx, duration, possessive } from "../util";

const toolIcon = (tool = "") =>
  /read/i.test(tool) ? "eye" : /grep|glob|search/i.test(tool) ? "list" : /write|edit/i.test(tool) ? "edit" : /shell|bash/i.test(tool) ? "terminal" : "bolt";

function ActivityLine({ it }: { it: RunOutput }) {
  switch (it.kind) {
    case "tool":
      return (
        <div className="act act-tool">
          <Icon name={toolIcon(it.tool)} size={13} />
          <b>{it.tool}</b>
          <span className="mono">{it.detail}</span>
        </div>
      );
    case "tool_error":
      return (
        <div className="act act-warn">
          <Icon name="alert" size={13} />
          <span>{it.detail}</span>
        </div>
      );
    case "status":
      return (
        <div className="act act-status">
          <Icon name="dot" size={10} />
          <span>{it.text}</span>
        </div>
      );
    case "error":
      return (
        <div className="act act-error">
          <Icon name="alert" size={13} />
          <span>{it.text}</span>
        </div>
      );
    case "thinking":
      return (
        <details className="act act-thinking">
          <summary>
            <Icon name="sparkle" size={13} /> Thinking
          </summary>
          <p>{it.text}</p>
        </details>
      );
    default:
      return null;
  }
}

const STATUS: Record<string, { label: string; tone: string }> = {
  running: { label: "Working", tone: "live" },
  succeeded: { label: "Done", tone: "ok" },
  failed: { label: "Failed", tone: "err" },
  cancelled: { label: "Stopped", tone: "neutral" },
  abandoned: { label: "Lost connection", tone: "err" },
};

export function RunCard({ run, reply, touched = [], title }: { run: Run; reply?: Message; touched?: string[]; title?: string }) {
  const l = useStore((s) => s.local);
  const stream = useStore((s) => s.streams[run.id]);
  const running = run.status === "running";
  const [showActivity, setShowActivity] = useState(false);
  const [, tick] = useState(0);
  const member = l?.members.find((m) => m.id === run.member_id);
  const mine = l?.me?.id === run.member_id;

  useEffect(() => {
    if (running || showActivity) ensureRunOutput(run.id);
  }, [running, showActivity, run.id]);
  useEffect(() => {
    if (!running) return;
    const t = setInterval(() => tick((x) => x + 1), 1000);
    return () => clearInterval(t);
  }, [running]);

  const items = stream?.items || [];
  const activity = items.filter((i) => i.kind !== "text");
  const liveText = items
    .filter((i) => i.kind === "text")
    .map((i) => i.text)
    .join("");
  const body = reply?.body || run.reply;
  const st = STATUS[run.status] || STATUS.failed;

  async function stop() {
    try {
      await local("POST", `/runs/${run.id}/cancel`);
    } catch (e) {
      errorToast(e);
    }
  }

  return (
    <div className={cx("run-card", `run-${run.status}`)}>
      <div className="run-head">
        <span className="run-icon">
          <Icon name="bolt" size={15} />
        </span>
        <div className="run-title">
          <b>{title || agentName(run.agent)}</b>
          <span className="muted">
            {" "}
            on {possessive(l, run.member_id)} machine
          </span>
        </div>
        <Avatar member={member} size={18} />
        <Badge tone={st.tone}>{st.label}</Badge>
        <span className="muted small mono">{duration(run.started_at, run.finished_at)}</span>
        {run.commit_sha && (
          <span className="muted small mono" title={`Codebase at ${run.commit_sha}`}>
            <Icon name="commit" size={12} /> {run.commit_sha.slice(0, 7)}
          </span>
        )}
        {running && (
          <Button size="sm" variant="ghost" icon="stop" onClick={stop} title={mine ? "Stop your agent" : "Ask its machine to stop"}>
            Stop
          </Button>
        )}
      </div>

      {running ? (
        <div className="run-live">
          <div className="activity live">
            {activity.slice(-5).map((it, i) => (
              <ActivityLine key={i} it={it} />
            ))}
          </div>
          {liveText ? (
            <Markdown text={liveText} className="run-stream" />
          ) : (
            <div className="working">
              <span className="dots">
                <i />
                <i />
                <i />
              </span>{" "}
              {stream?.loaded === false ? "Connecting to the run…" : "Working…"}
            </div>
          )}
        </div>
      ) : (
        <div className="run-body">
          {run.error && run.status !== "succeeded" && run.status !== "cancelled" && (
            <div className="run-error">
              <Icon name="alert" size={14} /> {run.error}
            </div>
          )}
          {body && <Markdown text={body} />}
          {touched.length > 0 && (
            <div className="touched">
              {touched.map((p) => (
                <span key={p} className="file-chip">
                  <Icon name="file" size={12} />
                  {p}
                </span>
              ))}
            </div>
          )}
          <button className="link-btn" onClick={() => setShowActivity(!showActivity)}>
            <Icon name={showActivity ? "chevronDown" : "chevronRight"} size={12} />
            {showActivity ? "Hide activity" : "Show activity"}
            {run.cost_usd > 0 && <span className="muted"> · ${run.cost_usd.toFixed(2)}</span>}
            {run.agent_version && <span className="muted"> · {run.agent_version}</span>}
          </button>
          {showActivity && (
            <div className="activity">
              {!stream?.loaded && <div className="muted small">Loading…</div>}
              {activity.map((it, i) => (
                <ActivityLine key={i} it={it} />
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  );
}
