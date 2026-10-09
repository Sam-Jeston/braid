import { useState } from "react";
import { remote } from "../api";
import { Icon } from "../icons";
import { errorToast, go, loadThread, setState, startRun, useStore } from "../store";
import type { ThreadDetail } from "../types";
import { Button, Field, Modal } from "../ui";
import { agentName } from "../util";

export function NewThread() {
  const l = useStore((s) => s.local)!;
  const [title, setTitle] = useState("");
  const [body, setBody] = useState("");
  const [spar, setSpar] = useState(true);
  const [busy, setBusy] = useState(false);
  const agent = l.agents.find((a) => a.id === l.agent && a.available);
  const close = () => setState({ newThreadOpen: false });

  async function create() {
    setBusy(true);
    try {
      const d = await remote<ThreadDetail>("POST", "/threads", { title, body });
      setState((s) => ({ details: { ...s.details, [d.thread.id]: d }, newThreadOpen: false }));
      go({ view: "thread", id: d.thread.id, stage: "sparring" });
      if (spar && agent) {
        await startRun(d.thread.id, "sparring", d.messages[0]?.id);
        loadThread(d.thread.id).catch(() => {});
      }
    } catch (e) {
      errorToast(e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal
      title="New thread"
      onClose={close}
      width={600}
      footer={
        <>
          <label className="check-row">
            <input type="checkbox" checked={spar && !!agent} disabled={!agent} onChange={(e) => setSpar(e.target.checked)} />
            <Icon name="bolt" size={13} /> Start sparring right away{agent ? ` with ${agentName(agent.id)}` : " (no agent available)"}
          </label>
          <span className="grow" />
          <Button variant="ghost" onClick={close}>
            Cancel
          </Button>
          <Button variant="primary" loading={busy} disabled={!title.trim() || !body.trim()} onClick={create}>
            Create thread
          </Button>
        </>
      }
    >
      <Field label="Title">
        <input autoFocus value={title} onChange={(e) => setTitle(e.target.value)} placeholder="Weekly digest emails for managers" />
      </Field>
      <Field label="The idea" hint="What problem, for whom, and why now? Rough is fine: the agent will push back and ask questions.">
        <textarea
          value={body}
          onChange={(e) => setBody(e.target.value)}
          rows={8}
          placeholder="Managers keep asking for a summary of what their team shipped. We could send a weekly digest email built from merged PRs and closed tickets…"
        />
      </Field>
    </Modal>
  );
}
