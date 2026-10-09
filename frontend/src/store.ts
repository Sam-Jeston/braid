import { useSyncExternalStore } from "react";
import { ApiError, bootstrap, connectEvents, local, remote } from "./api";
import type {
  Artifact,
  BraidEvent,
  LocalState,
  Member,
  PresenceEntry,
  Run,
  RunOutput,
  Stage,
  Team,
  Thread,
  ThreadDetail,
} from "./types";

export interface RunStream {
  items: RunOutput[];
  seq: number;
  loaded: boolean;
  loading: boolean;
  pending: { seq: number; items: RunOutput[] }[];
}

export interface Toast {
  id: number;
  text: string;
  kind: "info" | "error" | "success";
}

export type Route =
  | { view: "home" }
  | { view: "thread"; id: string; stage?: Stage }
  | { view: "settings"; tab?: "team" | "members" | "device" };

export interface AppState {
  boot: "loading" | "ready" | "error";
  bootError?: string;
  coreConnected: boolean;
  local: LocalState | null;
  threads: Thread[];
  threadsLoaded: boolean;
  details: Record<string, ThreadDetail>;
  streams: Record<string, RunStream>;
  presence: PresenceEntry[];
  route: Route;
  scanRun: Run | null;
  toasts: Toast[];
  newThreadOpen: boolean;
}

let state: AppState = {
  boot: "loading",
  coreConnected: false,
  local: null,
  threads: [],
  threadsLoaded: false,
  details: {},
  streams: {},
  presence: [],
  route: { view: "home" },
  scanRun: null,
  toasts: [],
  newThreadOpen: false,
};

const listeners = new Set<() => void>();
const subscribe = (l: () => void) => {
  listeners.add(l);
  return () => listeners.delete(l);
};

export const getState = () => state;

export function setState(patch: Partial<AppState> | ((s: AppState) => Partial<AppState>)) {
  const p = typeof patch === "function" ? patch(state) : patch;
  state = { ...state, ...p };
  listeners.forEach((l) => l());
}

/** Selectors must return existing references (or primitives). */
export function useStore<T>(sel: (s: AppState) => T): T {
  return useSyncExternalStore(subscribe, () => sel(state));
}

// --- toasts ---

let toastId = 0;
export function toast(text: string, kind: Toast["kind"] = "info") {
  const id = ++toastId;
  setState((s) => ({ toasts: [...s.toasts, { id, text, kind }] }));
  setTimeout(() => dismissToast(id), kind === "error" ? 8000 : 4500);
}
export function dismissToast(id: number) {
  setState((s) => ({ toasts: s.toasts.filter((t) => t.id !== id) }));
}
export function errorToast(e: unknown) {
  toast(e instanceof Error ? e.message : String(e), "error");
}

// --- lifecycle ---

export async function init() {
  try {
    await bootstrap();
    await loadLocal();
    setState({ boot: "ready" });
  } catch (e) {
    setState({ boot: "error", bootError: e instanceof Error ? e.message : String(e) });
    return;
  }
  let wasDown = false;
  connectEvents(applyEvent, (up) => {
    setState({ coreConnected: up });
    if (up && wasDown) refreshAll();
    wasDown = !up;
  });
}

async function refreshAll() {
  await loadLocal().catch(() => {});
  const r = state.route;
  if (r.view === "thread") loadThread(r.id).catch(() => {});
}

export async function loadLocal() {
  const prevTeam = state.local?.active?.team_id;
  const l = await local<LocalState>("GET", "/state");
  const teamChanged = l.active?.team_id !== prevTeam;
  setState(teamChanged ? { local: l, threads: [], threadsLoaded: false, details: {}, streams: {}, scanRun: null, route: { view: "home" } } : { local: l });
  if (l.active && (teamChanged || !state.threadsLoaded) && l.connection === "connected") {
    await loadThreads().catch(() => {});
  }
}

export async function loadThreads() {
  const threads = await remote<Thread[]>("GET", "/threads");
  setState({ threads, threadsLoaded: true });
}

export async function loadThread(id: string) {
  const d = await remote<ThreadDetail>("GET", `/threads/${id}`);
  setState((s) => ({ details: { ...s.details, [id]: d }, threads: upsertThread(s.threads, d.thread) }));
  return d;
}

export function go(route: Route) {
  setState({ route });
  const threadId = route.view === "thread" ? route.id : "";
  local("POST", "/presence", { thread_id: threadId }).catch(() => {});
  if (route.view === "thread") loadThread(route.id).catch(errorToast);
}

export function selectStage(stage: Stage) {
  const r = state.route;
  if (r.view === "thread") setState({ route: { ...r, stage } });
}

// --- run output streams ---

function coalesce(base: RunOutput[], more: RunOutput[]): RunOutput[] {
  const out = base.slice();
  for (const it of more) {
    const last = out[out.length - 1];
    if (last && (it.kind === "text" || it.kind === "thinking") && last.kind === it.kind) {
      out[out.length - 1] = { ...last, text: (last.text || "") + (it.text || "") };
    } else out.push(it);
  }
  return out;
}

export async function ensureRunOutput(runId: string) {
  const cur = state.streams[runId];
  if (cur && (cur.loaded || cur.loading)) return;
  setStream(runId, { ...(cur || emptyStream()), loading: true });
  try {
    const res = await remote<{ items: RunOutput[]; seq: number }>("GET", `/runs/${runId}/output`);
    const s = state.streams[runId] || emptyStream();
    let items = coalesce([], res.items);
    let seq = res.seq;
    for (const p of s.pending) {
      if (p.seq > res.seq) {
        items = coalesce(items, p.items);
        seq = Math.max(seq, p.seq);
      }
    }
    setStream(runId, { items, seq, loaded: true, loading: false, pending: [] });
  } catch {
    setStream(runId, { ...(state.streams[runId] || emptyStream()), loading: false });
  }
}

const emptyStream = (): RunStream => ({ items: [], seq: 0, loaded: false, loading: false, pending: [] });

function setStream(id: string, s: RunStream) {
  setState((st) => ({ streams: { ...st.streams, [id]: s } }));
}

// --- realtime event reducer ---

function upsertThread(list: Thread[], t: Thread): Thread[] {
  const rest = list.filter((x) => x.id !== t.id);
  return [t, ...rest].sort((a, b) => b.updated_at.localeCompare(a.updated_at));
}

function upsertBy<T extends { id: string }>(list: T[], item: T): T[] {
  const i = list.findIndex((x) => x.id === item.id);
  if (i < 0) return [...list, item];
  const copy = list.slice();
  copy[i] = item;
  return copy;
}

function withDetail(threadId: string | undefined, seq: number, fn: (d: ThreadDetail) => Partial<ThreadDetail>) {
  if (!threadId) return;
  const d = state.details[threadId];
  if (!d || (seq && seq <= d.seq)) return;
  setState((s) => ({ details: { ...s.details, [threadId]: { ...d, ...fn(d), seq: Math.max(d.seq, seq) } } }));
}

function updateLocal(fn: (l: LocalState) => Partial<LocalState>) {
  if (!state.local) return;
  setState((s) => ({ local: { ...s.local!, ...fn(s.local!) } }));
}

export function applyEvent(ev: BraidEvent) {
  const d = ev.data;
  switch (ev.type) {
    case "local.state":
    case "local.runs":
      loadLocal().catch(() => {});
      return;
    case "local.connection":
      updateLocal(() => ({ connection: d.status, connection_error: d.error }));
      if (d.status === "connected") loadLocal().then(() => (state.threadsLoaded ? undefined : loadThreads())).catch(() => {});
      return;
    case "local.repo":
      updateLocal((l) => ({ repos: l.repos.map((r) => (r.name === d.name ? { ...r, ...d } : r)) }));
      return;
    case "local.resync":
      refreshAll();
      loadThreads().catch(() => {});
      return;
    case "presence":
      setState({ presence: d as PresenceEntry[] });
      return;
    case "thread.created":
    case "thread.updated":
      setState((s) => ({ threads: upsertThread(s.threads, d as Thread) }));
      withDetail(ev.thread_id, ev.seq, () => ({ thread: d as Thread }));
      return;
    case "message.created":
      setState((s) => ({
        threads: s.threads.map((t) => (t.id === ev.thread_id ? { ...t, updated_at: d.created_at } : t)),
      }));
      withDetail(ev.thread_id, ev.seq, (x) => ({ messages: upsertBy(x.messages, d) }));
      return;
    case "run.started": {
      const run = d as Run;
      if (!state.streams[run.id]) setStream(run.id, { ...emptyStream(), loaded: true, seq: ev.seq });
      if (run.kind === "scan") setState({ scanRun: run });
      withDetail(ev.thread_id, ev.seq, (x) => ({ runs: upsertBy(x.runs, run) }));
      return;
    }
    case "run.output": {
      const runId = d.run_id as string;
      const s = state.streams[runId] || emptyStream();
      if (s.loaded) {
        if (ev.seq > s.seq) setStream(runId, { ...s, items: coalesce(s.items, d.items), seq: ev.seq });
      } else {
        setStream(runId, { ...s, pending: [...s.pending, { seq: ev.seq, items: d.items }] });
      }
      return;
    }
    case "run.finished": {
      const run = d as Run;
      if (run.kind === "scan") setState({ scanRun: run });
      withDetail(ev.thread_id, ev.seq, (x) => ({ runs: upsertBy(x.runs, run) }));
      return;
    }
    case "artifact.updated": {
      const a = d as Artifact;
      withDetail(ev.thread_id, ev.seq, (x) => {
        const rest = x.artifacts.filter((y) => !(y.stage === a.stage && y.path === a.path));
        return { artifacts: a.content ? [...rest, a] : rest };
      });
      return;
    }
    case "export.created":
      withDetail(ev.thread_id, ev.seq, (x) => ({ exports: upsertBy(x.exports, d) }));
      return;
    case "team.updated":
      updateLocal(() => ({ team: d as Team }));
      return;
    case "member.joined":
    case "member.updated":
    case "member.removed": {
      const m = d as Member;
      updateLocal((l) => ({
        members: upsertBy(l.members, m),
        me: l.me && l.me.id === m.id ? m : l.me,
      }));
      return;
    }
  }
}

// --- actions ---

export async function startRun(threadId: string, stage: Stage, triggerMessageId?: string) {
  try {
    const run = await local<Run>("POST", "/runs", { thread_id: threadId, stage, trigger_message_id: triggerMessageId || "" });
    if (!state.streams[run.id]) setStream(run.id, { ...emptyStream(), loaded: true });
    withDetail(threadId, 0, (x) => ({ runs: upsertBy(x.runs, run) }));
    return run;
  } catch (e) {
    errorToast(e);
    return null;
  }
}

export async function startScan() {
  try {
    const run = await local<Run>("POST", "/scan");
    if (!state.streams[run.id]) setStream(run.id, { ...emptyStream(), loaded: true });
    setState({ scanRun: run });
    return run;
  } catch (e) {
    errorToast(e);
    return null;
  }
}

export async function loadLatestScan() {
  const runs = await remote<Run[]>("GET", "/runs?kind=scan&limit=1");
  if (runs[0] && (!state.scanRun || state.scanRun.id !== runs[0].id)) setState({ scanRun: runs[0] });
}

export function isApiError(e: unknown, status: number) {
  return e instanceof ApiError && e.status === status;
}
