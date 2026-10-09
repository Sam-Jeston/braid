// Transport to the local Braid core. The core proxies /remote/* to the team
// server with this device's credentials, so the UI never holds a server token.

let base = "";
let token = "";

export class ApiError extends Error {
  constructor(
    message: string,
    public status: number,
    public code: string,
  ) {
    super(message);
  }
}

function storage(): Storage | null {
  try {
    return window.sessionStorage;
  } catch {
    return null;
  }
}

/**
 * Finds the core's address and token:
 *  - browser mode (`braid ui`): the token arrives in the URL fragment;
 *  - desktop mode (Wails): the webview asks the embedded asset server.
 */
export async function bootstrap(): Promise<void> {
  const m = location.hash.match(/token=([A-Za-z0-9_-]+)/);
  if (m) {
    token = m[1];
    storage()?.setItem("braid.token", token);
    history.replaceState(null, "", location.pathname + location.search);
    return;
  }
  const saved = storage()?.getItem("braid.token");
  if (saved) {
    token = saved;
    return;
  }
  const r = await fetch("/__braid/bootstrap").catch(() => null);
  if (r && r.ok) {
    const j = await r.json();
    base = j.api;
    token = j.token;
    return;
  }
  throw new Error("This window isn't connected to a Braid core. Start it with `braid` or open the desktop app.");
}

async function request<T>(method: string, url: string, body?: unknown): Promise<T> {
  let res: Response;
  try {
    res = await fetch(base + url, {
      method,
      headers: { Authorization: `Bearer ${token}`, ...(body !== undefined ? { "Content-Type": "application/json" } : {}) },
      body: body !== undefined ? JSON.stringify(body) : undefined,
    });
  } catch {
    throw new ApiError("Lost connection to the Braid core. Is the app still running?", 0, "core_unreachable");
  }
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  let data: any = undefined;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = undefined;
    }
  }
  if (!res.ok) {
    const msg = data?.error?.message || `Request failed (${res.status})`;
    throw new ApiError(msg, res.status, data?.error?.code || "error");
  }
  return data as T;
}

export const local = <T = unknown>(method: string, path: string, body?: unknown) => request<T>(method, `/local${path}`, body);
export const remote = <T = unknown>(method: string, path: string, body?: unknown) => request<T>(method, `/remote/v1${path}`, body);

/** Streams events from the core; reconnects automatically. Returns a closer. */
export function connectEvents(onEvent: (ev: any) => void, onStatus: (connected: boolean) => void): () => void {
  let ws: WebSocket | null = null;
  let closed = false;
  let retry = 500;
  const url = () => {
    if (base) return base.replace(/^http/, "ws") + `/local/ws?token=${token}`;
    const proto = location.protocol === "https:" ? "wss" : "ws";
    return `${proto}://${location.host}/local/ws?token=${token}`;
  };
  const open = () => {
    if (closed) return;
    ws = new WebSocket(url());
    ws.onopen = () => {
      retry = 500;
      onStatus(true);
    };
    ws.onmessage = (m) => {
      try {
        onEvent(JSON.parse(m.data));
      } catch {
        /* ignore malformed frames */
      }
    };
    ws.onclose = () => {
      onStatus(false);
      if (!closed) setTimeout(open, (retry = Math.min(retry * 2, 8000)));
    };
  };
  open();
  return () => {
    closed = true;
    ws?.close();
  };
}

/** Opens a URL in the system browser, or an exported folder in the file manager. */
export function openExternal(target: string, onError?: (e: unknown) => void) {
  local("POST", "/open", { target }).catch((e) => {
    if (/^https?:\/\//.test(target)) window.open(target, "_blank", "noopener");
    else onError?.(e);
  });
}
