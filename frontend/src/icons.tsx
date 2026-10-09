import type { ReactNode } from "react";

const P = (d: string) => <path d={d} />;

const icons: Record<string, ReactNode> = {
  plus: P("M12 5v14M5 12h14"),
  check: P("M5 12.5l4.5 4.5L19 7.5"),
  x: P("M6 6l12 12M18 6L6 18"),
  lock: (
    <>
      <rect x="5" y="11" width="14" height="9" rx="2" />
      {P("M8 11V8a4 4 0 0 1 8 0v3")}
    </>
  ),
  play: P("M8 5.5v13l10.5-6.5z"),
  stop: <rect x="6.5" y="6.5" width="11" height="11" rx="2" />,
  external: P("M14 4h6v6M20 4l-9 9M18 14v5a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1h5"),
  sparkle: P("M12 3.5l1.9 5.1 5.1 1.9-5.1 1.9L12 17.5l-1.9-5.1L5 10.5l5.1-1.9zM18.5 15.5l.8 2.2 2.2.8-2.2.8-.8 2.2-.8-2.2-2.2-.8 2.2-.8z"),
  file: P("M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8zM14 3v5h5M9 13h6M9 17h4"),
  code: P("M9 8l-4 4 4 4M15 8l4 4-4 4"),
  layout: (
    <>
      <rect x="3.5" y="4.5" width="17" height="15" rx="2" />
      {P("M3.5 9.5h17M9.5 9.5v10")}
    </>
  ),
  list: P("M9 6.5h11M9 12h11M9 17.5h11M4.5 6.5h.01M4.5 12h.01M4.5 17.5h.01"),
  message: P("M4 6a2 2 0 0 1 2-2h12a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2H9l-5 4z"),
  users: (
    <>
      <circle cx="9" cy="8" r="3.5" />
      {P("M2.5 20a6.5 6.5 0 0 1 13 0M16 4.6a3.5 3.5 0 0 1 0 6.8M18 14a6.5 6.5 0 0 1 3.5 6")}
    </>
  ),
  chevronRight: P("M9 6l6 6-6 6"),
  chevronDown: P("M6 9l6 6 6-6"),
  copy: (
    <>
      <rect x="8.5" y="8.5" width="12" height="12" rx="2" />
      {P("M15.5 8.5V5.5a2 2 0 0 0-2-2h-8a2 2 0 0 0-2 2v8a2 2 0 0 0 2 2h3")}
    </>
  ),
  refresh: P("M20 12a8 8 0 1 1-2.6-5.9M20 4v5h-5"),
  send: P("M5 12l15-7-5.5 15-2.5-6.5z"),
  bolt: P("M13 3L5 13.5h6L10 21l8-10.5h-6z"),
  terminal: P("M5 7l5 5-5 5M12 17h7"),
  edit: P("M4 20h4L19.5 8.5a2.1 2.1 0 0 0-3-3L5 17v3zM14.5 7.5l2 2"),
  history: P("M3.5 12a8.5 8.5 0 1 0 2.5-6M3.5 4v4h4M12 8v4.5l3 2"),
  commit: (
    <>
      <circle cx="12" cy="12" r="3.5" />
      {P("M3 12h5.5M15.5 12H21")}
    </>
  ),
  settings: (
    <>
      {P("M4 7h9M17 7h3M4 17h3M11 17h9")}
      <circle cx="15" cy="7" r="2" />
      <circle cx="9" cy="17" r="2" />
    </>
  ),
  archive: P("M4 5h16v4H4zM5.5 9v10h13V9M10 13h4"),
  eye: (
    <>
      {P("M2.5 12s3.5-7 9.5-7 9.5 7 9.5 7-3.5 7-9.5 7-9.5-7-9.5-7z")}
      <circle cx="12" cy="12" r="2.8" />
    </>
  ),
  trash: P("M4 7h16M10 11v6M14 11v6M5.5 7l1 13h11l1-13M9 7V4h6v3"),
  folder: P("M3.5 6.5a2 2 0 0 1 2-2h4l2 2.5h7a2 2 0 0 1 2 2v8.5a2 2 0 0 1-2 2h-13a2 2 0 0 1-2-2z"),
  link: P("M10 14a4 4 0 0 0 5.7 0l3-3a4 4 0 0 0-5.7-5.7l-1 1M14 10a4 4 0 0 0-5.7 0l-3 3a4 4 0 0 0 5.7 5.7l1-1"),
  maximize: P("M4 9V4h5M20 9V4h-5M4 15v5h5M20 15v5h-5"),
  dot: <circle cx="12" cy="12" r="4" fill="currentColor" stroke="none" />,
  alert: P("M12 4l9 16H3zM12 10v4M12 17h.01"),
  key: (
    <>
      <circle cx="8" cy="15" r="4" />
      {P("M11 12l8.5-8.5M16 7l2.5 2.5M14 9l2 2")}
    </>
  ),
  server: (
    <>
      <rect x="3.5" y="4" width="17" height="7" rx="2" />
      <rect x="3.5" y="13" width="17" height="7" rx="2" />
      {P("M7.5 7.5h.01M7.5 16.5h.01")}
    </>
  ),
};

export function Icon({ name, size = 16, className }: { name: keyof typeof icons | string; size?: number; className?: string }) {
  return (
    <svg
      className={className}
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={1.8}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      {icons[name] ?? null}
    </svg>
  );
}

/** The Braid mark: three strands crossing into one. */
export function Logo({ size = 28 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 32 32" aria-label="Braid">
      <defs>
        <linearGradient id="bg-l" x1="0" y1="0" x2="1" y2="1">
          <stop offset="0" stopColor="var(--accent)" />
          <stop offset="1" stopColor="var(--accent-2)" />
        </linearGradient>
      </defs>
      <rect width="32" height="32" rx="9" fill="url(#bg-l)" />
      <g fill="none" strokeWidth="2.4" strokeLinecap="round">
        <path d="M9 7c0 6 14 6 14 12s-7 6-7 6" stroke="#fff" opacity=".95" />
        <path d="M23 7c0 6-14 6-14 12s7 6 7 6" stroke="#fff" opacity=".6" />
        <path d="M16 7v18" stroke="#fff" opacity=".35" />
      </g>
    </svg>
  );
}
