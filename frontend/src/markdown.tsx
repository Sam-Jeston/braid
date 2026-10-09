import DOMPurify from "dompurify";
import { marked } from "marked";
import { memo, useMemo, type MouseEvent } from "react";
import { openExternal } from "./api";
import { cx } from "./util";

marked.setOptions({ gfm: true, breaks: false });

DOMPurify.addHook("afterSanitizeAttributes", (node) => {
  if (node.tagName === "A") {
    node.setAttribute("rel", "noopener noreferrer");
  }
});

// Agent output and teammates' messages are untrusted: always sanitize.
export function renderMarkdown(text: string): string {
  const html = marked.parse(text || "", { async: false }) as string;
  return DOMPurify.sanitize(html, { FORBID_TAGS: ["style", "form", "input", "iframe"], FORBID_ATTR: ["style"] });
}

function onClick(e: MouseEvent<HTMLDivElement>) {
  const a = (e.target as HTMLElement).closest("a");
  if (!a) return;
  const href = a.getAttribute("href") || "";
  e.preventDefault();
  if (/^https?:\/\//.test(href)) openExternal(href);
}

export const Markdown = memo(function Markdown({ text, className }: { text: string; className?: string }) {
  const html = useMemo(() => renderMarkdown(text), [text]);
  return <div className={cx("md", className)} onClick={onClick} dangerouslySetInnerHTML={{ __html: html }} />;
});
