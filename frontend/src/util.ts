import type { Delivery, IssueState, LocalState, Member, RepoStatus, Role, Stage, StageState, Thread } from "./types";

export const cx = (...xs: (string | false | null | undefined)[]) => xs.filter(Boolean).join(" ");

export const ROLES: Record<Role, { label: string; short: string }> = {
  product: { label: "Product", short: "PM" },
  design: { label: "Design", short: "DS" },
  engineering: { label: "Engineering", short: "EN" },
};

export const STAGES: Record<Stage, { title: string; owner: Role | null; verb: string; blurb: string; primary: string }> = {
  sparring: {
    title: "Sparring",
    owner: "product",
    verb: "Spar on the idea",
    blurb: "Pressure-test the idea against the real codebase before anyone designs or builds it.",
    primary: "brief.md",
  },
  design: {
    title: "Design",
    owner: "design",
    verb: "Explore design variations",
    blurb: "Generate 2–3 distinct HTML mockups that reuse the product's existing UI patterns.",
    primary: "design.md",
  },
  technical: {
    title: "Technical",
    owner: "engineering",
    verb: "Draft the technical plan",
    blurb: "Turn the brief and the selected design into a build plan grounded in the code.",
    primary: "technical.md",
  },
  plan: {
    title: "Plan",
    owner: null,
    verb: "Generate the delivery plan",
    blurb: "Synthesize every approved stage into tickets for your tracker.",
    primary: "plan.json",
  },
};

export const STAGE_ORDER: Stage[] = ["sparring", "design", "technical", "plan"];

export const DELIVERY: Record<Delivery, { label: string; icon: string }> = {
  planned: { label: "Planned", icon: "list" },
  built: { label: "Built", icon: "check" },
  cancelled: { label: "Cancelled", icon: "x" },
};

export const ISSUE_STATES: Record<IssueState, string> = {
  todo: "To do",
  in_progress: "In progress",
  done: "Done",
  cancelled: "Cancelled",
};

/** A published thread's delivery (threads published before tracking existed count as planned). */
export function deliveryOf(t: Thread): Delivery {
  return t.delivery || "planned";
}

/** Short status for thread lists: the current stage, or what became of the published plan. */
export function phaseLabel(t: Thread): string {
  return t.status === "completed" ? deliveryOf(t) : STAGES[t.current_stage].title;
}

export function stageState(t: Thread, s: Stage): StageState {
  return t.stages.find((x) => x.stage === s) || { stage: s, status: "locked" };
}

export function timeAgo(iso: string | null | undefined): string {
  if (!iso) return "";
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 45) return "just now";
  if (s < 3600) return `${Math.round(s / 60)}m ago`;
  if (s < 86400) return `${Math.round(s / 3600)}h ago`;
  if (s < 86400 * 7) return `${Math.round(s / 86400)}d ago`;
  return new Date(iso).toLocaleDateString();
}

export function duration(fromIso: string, toIso?: string | null): string {
  const ms = (toIso ? new Date(toIso).getTime() : Date.now()) - new Date(fromIso).getTime();
  const s = Math.max(0, Math.round(ms / 1000));
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  return `${m}m ${String(s % 60).padStart(2, "0")}s`;
}

export function initials(name: string): string {
  const parts = name.trim().split(/\s+/);
  return ((parts[0]?.[0] || "?") + (parts.length > 1 ? parts[parts.length - 1][0] : "")).toUpperCase();
}

export function memberName(l: LocalState | null, id: string | undefined): string {
  if (!id) return "Someone";
  const m = l?.members.find((x) => x.id === id);
  if (!m) return "Someone";
  return l?.me?.id === id ? "You" : m.display_name;
}

export function possessive(l: LocalState | null, id: string): string {
  const n = memberName(l, id);
  return n === "You" ? "your" : `${n}'s`;
}

export const capitalize = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

export function hasRole(m: Member | null | undefined, r: Role | null): boolean {
  return !!m && !!r && m.roles.includes(r);
}

export function hue(id: string): number {
  let h = 0;
  for (let i = 0; i < id.length; i++) h = (h * 31 + id.charCodeAt(i)) % 360;
  return h;
}

export function fileLabel(path: string): string {
  const base = path.split("/").pop() || path;
  return base.replace(/\.(html|md|json)$/, "").replace(/[-_]+/g, " ");
}

export function agentName(id: string): string {
  return id === "claude" ? "Claude Code" : id === "codex" ? "Codex" : id === "fake" ? "Demo agent" : id;
}

export function outputLabel(type: string): string {
  return type === "linear" ? "Linear" : type === "jira" ? "Jira" : "Markdown";
}

/** Shortens a codebase revision: a SHA, or "name@sha" pairs when the team has several repositories. */
export function shortRevision(rev: string, n = 8): string {
  return rev
    .split(" ")
    .map((part) => {
      const at = part.lastIndexOf("@");
      return at >= 0 ? `${part.slice(0, at)}@${part.slice(at + 1, at + 1 + n)}` : part.slice(0, n);
    })
    .join(" ");
}

/** Describes this machine's checkout of a repository. */
export function checkoutLabel(r: RepoStatus | undefined): string {
  if (!r || r.state === "none") return "syncs on first agent run";
  if (r.state === "ready" || r.state === "override") return `${r.commit.slice(0, 8)} · ${r.state === "override" ? "local folder" : r.branch}`;
  if (r.state === "error") return r.error || "couldn't sync";
  return `${r.state}…`;
}
