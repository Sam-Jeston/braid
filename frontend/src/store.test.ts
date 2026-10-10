import { beforeEach, expect, test } from "bun:test";
import { applyEvent, getState, setState } from "./store";
import type { Artifact, LocalState, Message, Thread, ThreadDetail } from "./types";

const thread = (over: Partial<Thread> = {}): Thread =>
  ({
    id: "thr_1",
    title: "Idea",
    status: "active",
    current_stage: "sparring",
    stages: [{ stage: "sparring", status: "open" }],
    updated_at: "2026-01-01T00:00:00Z",
    ...over,
  }) as Thread;

const detail = (seq: number): ThreadDetail => ({ thread: thread(), messages: [], runs: [], artifacts: [], exports: [], seq });
const msg = (id: string): Message => ({ id, thread_id: "thr_1", stage: "sparring", body: id, created_at: "2026-01-01T00:00:01Z" }) as Message;

beforeEach(() => {
  setState({ threads: [], details: { thr_1: detail(10) }, streams: {}, local: null, presence: [] });
});

test("events at or before a snapshot's seq are ignored, so replays are harmless", () => {
  applyEvent({ seq: 9, type: "message.created", thread_id: "thr_1", data: msg("old"), created_at: "" });
  expect(getState().details.thr_1.messages).toHaveLength(0);
  applyEvent({ seq: 11, type: "message.created", thread_id: "thr_1", data: msg("new"), created_at: "" });
  applyEvent({ seq: 11, type: "message.created", thread_id: "thr_1", data: msg("new"), created_at: "" });
  expect(getState().details.thr_1.messages.map((m) => m.id)).toEqual(["new"]);
  expect(getState().details.thr_1.seq).toBe(11);
});

test("thread events upsert the list, newest first", () => {
  applyEvent({ seq: 1, type: "thread.created", data: thread({ id: "a", updated_at: "2026-01-01T00:00:00Z" }), created_at: "" });
  applyEvent({ seq: 2, type: "thread.created", data: thread({ id: "b", updated_at: "2026-01-02T00:00:00Z" }), created_at: "" });
  applyEvent({ seq: 3, type: "thread.updated", data: thread({ id: "a", title: "Renamed", updated_at: "2026-01-03T00:00:00Z" }), created_at: "" });
  expect(getState().threads.map((t) => [t.id, t.title])).toEqual([
    ["a", "Renamed"],
    ["b", "Idea"],
  ]);
});

test("an empty artifact is a tombstone that removes the document", () => {
  const art = (content: string) => ({ id: "x", stage: "design", path: "mockups/a.html", content }) as Artifact;
  applyEvent({ seq: 11, type: "artifact.updated", thread_id: "thr_1", data: art("<h1>A</h1>"), created_at: "" });
  expect(getState().details.thr_1.artifacts).toHaveLength(1);
  applyEvent({ seq: 12, type: "artifact.updated", thread_id: "thr_1", data: art(""), created_at: "" });
  expect(getState().details.thr_1.artifacts).toHaveLength(0);
});

test("run output is buffered until the stream loads, then text is coalesced", () => {
  const out = (seq: number, text: string) => ({ seq, type: "run.output", run_id: "run_1", data: { run_id: "run_1", items: [{ kind: "text", text }] }, created_at: "" });
  applyEvent(out(5, "Hel"));
  expect(getState().streams.run_1.pending).toHaveLength(1);

  applyEvent({ seq: 20, type: "run.started", thread_id: "thr_1", data: { id: "run_2", kind: "stage" }, created_at: "" });
  applyEvent({ ...out(21, "Hel"), run_id: "run_2", data: { run_id: "run_2", items: [{ kind: "text", text: "Hel" }] } });
  applyEvent({ ...out(22, "lo"), run_id: "run_2", data: { run_id: "run_2", items: [{ kind: "text", text: "lo" }] } });
  applyEvent({ ...out(22, "lo"), run_id: "run_2", data: { run_id: "run_2", items: [{ kind: "text", text: "lo" }] } }); // replayed
  expect(getState().streams.run_2.items).toEqual([{ kind: "text", text: "Hello" }]);
});

test("repo status updates merge by name and keep this member's override", () => {
  setState({
    local: {
      repos: [
        { name: "web", state: "none", dir: "", commit: "", url: "u", branch: "", override: "/code/web" },
        { name: "api", state: "none", dir: "", commit: "", url: "v", branch: "" },
      ],
      members: [],
      me: null,
    } as unknown as LocalState,
  });
  applyEvent({ seq: 0, type: "local.repo", data: { name: "web", state: "override", dir: "/code/web", commit: "abc", url: "u", branch: "" }, created_at: "" });
  const [web, api] = getState().local!.repos;
  expect(web.state).toBe("override");
  expect(web.override).toBe("/code/web");
  expect(api.state).toBe("none");
});

test("member updates refresh the current member", () => {
  const me = { id: "m1", display_name: "Priya", roles: ["product"] };
  setState({ local: { me, members: [me], repos: [] } as unknown as LocalState });
  applyEvent({ seq: 5, type: "member.updated", data: { ...me, roles: ["product", "design"] }, created_at: "" });
  expect(getState().local!.me!.roles).toEqual(["product", "design"]);
  applyEvent({ seq: 6, type: "member.joined", data: { id: "m2", display_name: "Dev", roles: [] }, created_at: "" });
  expect(getState().local!.members).toHaveLength(2);
});

test("an incompatible connection carries what to update, and clears once connected", () => {
  setState({ local: { connection: "connected", members: [], repos: [], me: null } as unknown as LocalState });
  const update = { app: true, server_version: "v0.5.0", download_url: "https://example.com/v0.5.0" };
  applyEvent({ seq: 0, type: "local.connection", data: { status: "incompatible", error: "Update Braid", update }, created_at: "" });
  expect(getState().local!.connection).toBe("incompatible");
  expect(getState().local!.update).toEqual(update);
  applyEvent({ seq: 0, type: "local.connection", data: { status: "offline", error: "down", update: null }, created_at: "" });
  expect(getState().local!.update).toBeNull();
});

test("tracker states arrive as export.updated and replace the export", () => {
  const exp = (state?: string) => ({ id: "exp_1", thread_id: "thr_1", target: "linear", items: [{ ref: "epic", key: "ENG-1", title: "E", url: "u", state }] });
  applyEvent({ seq: 11, type: "export.created", thread_id: "thr_1", data: exp(), created_at: "" });
  applyEvent({ seq: 12, type: "export.updated", thread_id: "thr_1", data: exp("in_progress"), created_at: "" });
  const exports = getState().details.thr_1.exports;
  expect(exports).toHaveLength(1);
  expect(exports[0].items[0].state).toBe("in_progress");
});
