import { openExternal } from "../api";
import { errorToast, useStore } from "../store";
import { Button, Empty } from "../ui";

/** Shown instead of the workspace when this app and the team's server don't share a protocol version. */
export function UpdateRequired() {
  const l = useStore((s) => s.local)!;
  const u = l.update;
  const server = u?.server_version && u.server_version !== "dev" ? u.server_version : "";
  return (
    <div className="update-required card">
      <Empty icon="alert" title={u?.app === false ? "Your team's server needs an update" : "Update Braid"}>
        <p className="muted">{l.connection_error}</p>
        <p className="muted versions">
          This app: <code>{l.version}</code>
          {server && (
            <>
              {" · "}Server: <code>{server}</code>
            </>
          )}
        </p>
        {u?.app && u.download_url && (
          <Button variant="primary" icon="external" onClick={() => openExternal(u.download_url!, errorToast)}>
            Download Braid{server ? ` ${server}` : ""}
          </Button>
        )}
        {u?.app === false && <p className="muted small">Braid checks again every minute and reconnects once the server is updated.</p>}
      </Empty>
    </div>
  );
}
