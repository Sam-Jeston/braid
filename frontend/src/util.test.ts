import { expect, test } from "bun:test";
import type { LocalState, Member, RepoStatus, Thread } from "./types";
import { checkoutLabel, deliveryOf, duration, fileLabel, initials, memberName, phaseLabel, possessive, shortRevision, stageState, timeAgo } from "./util";

test("shortRevision shortens one SHA or several name@sha pairs", () => {
  expect(shortRevision("0123456789abcdef")).toBe("01234567");
  expect(shortRevision("web@0123456789 api@abcdef0123", 7)).toBe("web@0123456 api@abcdef0");
  expect(shortRevision("")).toBe("");
});

test("checkoutLabel describes each checkout state", () => {
  const base: RepoStatus = { name: "web", state: "ready", dir: "/r", commit: "0123456789", url: "u", branch: "main" };
  expect(checkoutLabel(undefined)).toBe("syncs on first agent run");
  expect(checkoutLabel(base)).toBe("01234567 · main");
  expect(checkoutLabel({ ...base, state: "override" })).toBe("01234567 · local folder");
  expect(checkoutLabel({ ...base, state: "error", error: "couldn't clone" })).toBe("couldn't clone");
  expect(checkoutLabel({ ...base, state: "cloning" })).toBe("cloning…");
});

test("names use 'You' for the current member", () => {
  const me = { id: "m1", display_name: "Priya Shah" } as Member;
  const other = { id: "m2", display_name: "Dev" } as Member;
  const l = { me, members: [me, other] } as unknown as LocalState;
  expect(memberName(l, "m1")).toBe("You");
  expect(memberName(l, "m2")).toBe("Dev");
  expect(memberName(l, "gone")).toBe("Someone");
  expect(possessive(l, "m1")).toBe("your");
  expect(possessive(l, "m2")).toBe("Dev's");
  expect(initials("Priya  Shah")).toBe("PS");
  expect(initials("")).toBe("?");
});

test("time formatting", () => {
  const ago = (s: number) => new Date(Date.now() - s * 1000).toISOString();
  expect(timeAgo(ago(10))).toBe("just now");
  expect(timeAgo(ago(600))).toBe("10m ago");
  expect(timeAgo(ago(7200))).toBe("2h ago");
  expect(timeAgo(null)).toBe("");
  expect(duration("2026-01-01T00:00:00Z", "2026-01-01T00:01:05Z")).toBe("1m 05s");
  expect(duration("2026-01-01T00:00:00Z", "2026-01-01T00:00:09Z")).toBe("9s");
});

test("stageState treats unknown stages as locked", () => {
  const t = { stages: [{ stage: "sparring", status: "open" }] } as Thread;
  expect(stageState(t, "sparring").status).toBe("open");
  expect(stageState(t, "plan").status).toBe("locked");
  expect(fileLabel("mockups/compact_list-view.html")).toBe("compact list view");
});

test("thread lists show the stage while active, then what became of the plan", () => {
  const t = (over: Partial<Thread>) => ({ status: "active", current_stage: "design", stages: [], ...over }) as Thread;
  expect(phaseLabel(t({}))).toBe("Design");
  expect(phaseLabel(t({ status: "completed" }))).toBe("planned"); // published before delivery tracking
  expect(phaseLabel(t({ status: "completed", delivery: "built" }))).toBe("built");
  expect(deliveryOf(t({ status: "completed", delivery: "cancelled" }))).toBe("cancelled");
});
