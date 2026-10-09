# frontend

React 19 + TypeScript UI. It's built by Bun into `dist/` and embedded into the Go binaries (`embed.go`). The same build runs in the Wails webview and in a browser served by `braid`.

## Commands

```sh
bun install
bun run build        # → dist/ (minified)
bun run dev          # rebuild on change; pair with `go run ./cmd/braid --ui-dir frontend/dist`
bun run typecheck    # tsc --noEmit; must pass
bun test             # unit tests (src/*.test.ts); DOM from jsdom via test/setup.ts
```

Use Bun for tooling. There is no Vite and no `Bun.serve`: the Go core serves the UI.

## Architecture

- The UI talks **only to the local core** (`src/api.ts`):
  - `local(...)` calls `/local/*`.
  - `remote(...)` calls `/remote/v1/*`, which the core proxies to braid-server with the device's auth.
  - Never call braid-server directly, and never store tokens beyond `sessionStorage` (browser mode only).
- `src/store.ts` holds all app state behind `useSyncExternalStore`. **Selectors must return existing references or primitives.** Building a new array or object inside a selector causes infinite re-renders: derive with `useMemo` in the component instead.
- Realtime: `applyEvent` reduces server and `local.*` events. Thread snapshots carry `seq`, and events with `seq <= snapshot.seq` are ignored, so reducers must stay idempotent.
- `src/types.ts` mirrors `internal/api/types.go`. Stage metadata in `src/util.ts` mirrors `internal/workflow/stages.go`.

## Security (agent output and teammates' messages are untrusted)

- Render markdown only through `<Markdown>` (`marked` → DOMPurify). No other `dangerouslySetInnerHTML`.
- `markdown.test.ts` pins the sanitizer's behaviour against known XSS vectors. Keep it passing, and test under jsdom: happy-dom mishandles DOMPurify and let `javascript:` links through, while real browsers and jsdom strip them.
- Mockup HTML is shown only in `<iframe sandbox="" srcDoc=...>`. Don't add `allow-scripts` or `allow-same-origin`.
- `index.html` has a strict CSP: no inline scripts and no external origins. Keep it that way. External links open via `openExternal` (the core asks the OS), not by navigating the webview.

## Styling

Plain CSS in `src/styles.css`, with design tokens as CSS variables on `:root` and a dark-mode override under `prefers-color-scheme`. Use the tokens (`--accent`, `--panel`, role colours `--product` / `--design` / `--engineering`) rather than literal colours. Icons are hand-written SVG paths in `src/icons.tsx`.
