import { createRoot } from "react-dom/client";
import { Onboarding } from "./components/Onboarding";
import { Home } from "./components/Home";
import { NewThread } from "./components/NewThread";
import { Settings } from "./components/Settings";
import { Sidebar } from "./components/Sidebar";
import { ThreadView } from "./components/ThreadView";
import { UpdateRequired } from "./components/UpdateRequired";
import { Icon, Logo } from "./icons";
import { init, useStore } from "./store";
import { Spinner, Toasts } from "./ui";

function App() {
  const boot = useStore((s) => s.boot);
  const bootError = useStore((s) => s.bootError);
  const active = useStore((s) => !!s.local?.active);
  const coreConnected = useStore((s) => s.coreConnected);

  if (boot === "loading") {
    return (
      <div className="splash">
        <Logo size={44} />
        <Spinner size={18} />
      </div>
    );
  }
  if (boot === "error") {
    return (
      <div className="splash">
        <Logo size={44} />
        <p className="err-text">{bootError}</p>
      </div>
    );
  }
  return (
    <>
      {!coreConnected && (
        <div className="core-banner">
          <Icon name="alert" size={14} /> Reconnecting to the Braid core…
        </div>
      )}
      {active ? <Shell /> : <Onboarding />}
      <Toasts />
    </>
  );
}

function Shell() {
  const route = useStore((s) => s.route);
  const newThread = useStore((s) => s.newThreadOpen);
  const connection = useStore((s) => s.local?.connection);
  const connError = useStore((s) => s.local?.connection_error);
  return (
    <div className="shell">
      <Sidebar />
      <main className="main">
        {connection === "revoked" && (
          <div className="offline-banner err">
            <Icon name="alert" size={14} /> {connError}
          </div>
        )}
        {connection === "offline" && (
          <div className="offline-banner">
            <Icon name="alert" size={14} /> Can't reach your team's server{connError ? `: ${connError}` : ""}. Retrying…
          </div>
        )}
        {connection === "incompatible" ? (
          <UpdateRequired />
        ) : (
          <>
            {route.view === "home" && <Home />}
            {route.view === "thread" && <ThreadView key={route.id} id={route.id} stage={route.stage} />}
            {route.view === "settings" && <Settings tab={route.tab} />}
          </>
        )}
      </main>
      {newThread && <NewThread />}
    </div>
  );
}

createRoot(document.getElementById("root")!).render(<App />);
init();
