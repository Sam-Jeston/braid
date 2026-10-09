import { Icon } from "../icons";
import type { RepoRef } from "../types";
import { Button } from "../ui";
import { cx } from "../util";

// Mirrors workflow.MaxRepos.
const MAX_REPOS = 10;

export const emptyRepo = (): RepoRef => ({ name: "", url: "", branch: "" });

/** Editable list of team repositories. One row is the norm; more are a click away. */
export function RepoList({ value, onChange, disabled, hint }: { value: RepoRef[]; onChange: (v: RepoRef[]) => void; disabled?: boolean; hint?: string }) {
  const rows = value.length ? value : [emptyRepo()];
  const set = (i: number, patch: Partial<RepoRef>) => onChange(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  return (
    <div className="field">
      <span className="field-label">{rows.length > 1 ? "Repositories" : "Repository"}</span>
      {rows.map((r, i) => (
        <div key={i} className={cx("repo-row", rows.length > 1 && !disabled && "removable")}>
          <input
            className="mono"
            value={r.url}
            disabled={disabled}
            aria-label="Repository"
            placeholder="github.com/acme/app or git@…"
            // A new URL gets a fresh name derived from it.
            onChange={(e) => set(i, { url: e.target.value, name: "" })}
          />
          <input className="mono" value={r.branch} disabled={disabled} aria-label="Branch" placeholder="default branch" onChange={(e) => set(i, { branch: e.target.value })} />
          {rows.length > 1 && !disabled && (
            <Button type="button" size="sm" variant="ghost" icon="x" title="Remove repository" onClick={() => onChange(rows.filter((_, j) => j !== i))} />
          )}
          {rows.length > 1 && r.name && <span className="repo-name mono">{r.name}</span>}
        </div>
      ))}
      {!disabled && rows.length < MAX_REPOS && (
        <button type="button" className="link-btn add-repo" onClick={() => onChange([...rows, emptyRepo()])}>
          <Icon name="plus" size={12} /> Add another repository
        </button>
      )}
      {hint && <span className="field-hint">{hint}</span>}
    </div>
  );
}
