import { useCallback, useEffect, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import { ApiError } from "./lib/api";
import { connectionLabel } from "./lib/connection";
import { useDashboard } from "./lib/useDashboard";
import { useSession } from "./lib/useSession";
import { ActivityPage } from "./pages/Activity";
import { Home } from "./pages/Home";
import { CommandsPage } from "./pages/Commands";
import { Confirmation } from "./components/Confirmation";
import { ArchivePage } from "./pages/ArchivePage";
import { ConversationsPage } from "./pages/Conversations";
import { usePendingFollower } from "./lib/pendingSends";
import { Icon } from "./components/Icon";
import { SettingsPage } from "./pages/Settings";
import { Switch } from "./components/ui";
import { useWorkspace } from "./lib/workspace";
import "./styles/index.css";

const pages = [
  "Home",
  "Conversations",
  "Commands",
  "Activity",
  "Archive",
  "Settings",
] as const;
type Page = (typeof pages)[number];
type Toast = { id: number; text: string };

function App() {
  const [page, setPage] = useState<Page>("Home");
  const [token, setToken] = useState(""),
    [busy, setBusy] = useState(false);
  const [selected, setSelected] = useState("");
  const [theme, setTheme] = useState<"dark" | "light">(() =>
    window.matchMedia?.("(prefers-color-scheme: light)")?.matches
      ? "light"
      : "dark",
  );
  const [toasts, setToasts] = useState<Toast[]>([]),
    [confirmSignOut, setConfirmSignOut] = useState(false);
  const session = useSession();
  const signedIn = session.signedIn;
  // Expiry and sign-out end the session, clear its data and return home.
  const endSession = useCallback(() => {
    session.end();
    dashboardReset.current();
    setPage("Home");
    setSelected("");
  }, [session.end]);
  const dashboardReset = useRef<() => void>(() => {});
  const dashboard = useDashboard(session, endSession);
  dashboardReset.current = dashboard.reset;
  const { state, jobs, conversations, inbox, error, setError, lastUpdated } = dashboard;
  const workspace = useWorkspace(signedIn, inbox, dashboard.setInbox);
  usePendingFollower(signedIn);
  const dirty = workspace.dirty;
  const notify = useCallback((text: string) => {
    const id = Date.now() + Math.random();
    setToasts((t) => [...t.slice(-2), { id, text }]);
    setTimeout(() => setToasts((t) => t.filter((x) => x.id !== id)), 3800);
  }, []);
  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    // Tints the phone's status bar to match.
    document
      .querySelector('meta[name="theme-color"]')
      ?.setAttribute("content", theme === "dark" ? "#17141f" : "#f5f3fa");
  }, [theme]);
  useEffect(() => {
    if (!dirty) return;
    const before = (event: BeforeUnloadEvent) => event.preventDefault();
    window.addEventListener("beforeunload", before);
    return () => window.removeEventListener("beforeunload", before);
  }, [dirty]);
  async function saveAll() {
    if (await workspace.save()) notify("Changes saved.");
  }
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "s" && dirty) {
        e.preventDefault();
        void saveAll();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  });
  function navigate(next: Page) {
    if (next === page) return;
    if ((next === "Commands" || next === "Settings") && !dirty)
      workspace.reload();
    setPage(next);
    window.scrollTo(0, 0);
    // Phones scroll the workspace rather than the document.
    document.getElementById("workspace")?.scrollTo(0, 0);
  }
  async function act(fn: () => Promise<unknown>, refreshAfter = true) {
    setBusy(true);
    setError("");
    try {
      await fn();
      if (refreshAfter) {
        dashboard.invalidateList();
        await dashboard.refresh();
      }
    } catch (e) {
      setError(
        e instanceof ApiError
          ? e.message.replaceAll("_", " ")
          : "Could not reach the server.",
      );
    } finally {
      setBusy(false);
    }
  }
  async function togglePaused() {
    if (!state) return;
    await act(() => dashboard.setPaused(!state.paused), false);
  }
  async function signOut() {
    await act(async () => {
      await session.signOut();
      endSession();
    }, false);
  }
  function loadMoreConversations() {
    void act(dashboard.loadMoreConversations, false);
  }
  const reviewCount = jobs.filter(
    (j) => j.state === "awaiting_approval",
  ).length;
  if (!signedIn)
    return (
      <div className="login">
        <main className="login-card">
          <span className="logo big" aria-hidden="true">
            <Icon name="commands" size={30} />
          </span>
          <h1>SmsBot</h1>
          <p className="muted">
            Paste the owner credential from your server’s setup folder.
          </p>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              void act(async () => {
                await session.signIn(token);
                setToken("");
              });
            }}
          >
            {/* Lets password managers file the credential under a name. */}
            <input
              type="text"
              name="username"
              autoComplete="username"
              value="SmsBot owner"
              readOnly
              hidden
            />
            <label htmlFor="owner" className="sr-only">
              Owner credential
            </label>
            <input
              id="owner"
              name="password"
              type="password"
              autoComplete="current-password"
              autoFocus
              placeholder="Owner credential"
              value={token}
              onChange={(e) => setToken(e.target.value)}
              required
            />
            <Switch
              label="Keep me signed in"
              checked={session.remember}
              onChange={session.setRemember}
            />
            <button className="primary wide" disabled={busy || !token}>
              {busy ? "Signing in…" : "Sign in"}
            </button>
          </form>
          {error && (
            <p role="alert" className="inline-alert">
              {error}
            </p>
          )}
          <small className="muted login-foot">
            <Icon name="shield" size={13} />
            Only a revocable session is kept, never the credential.
          </small>
        </main>
      </div>
    );
  const live = !!state && !state.paused;
  return (
    <div className={"shell page-" + page.toLowerCase()}>
      <a className="skip-link" href="#workspace">
        Skip to content
      </a>
      <aside className="rail">
        <div className="brand">
          <span className="logo" aria-hidden="true">
            <Icon name="commands" size={18} />
          </span>
          <span>SmsBot</span>
        </div>
        <nav aria-label="Main navigation">
          {pages.map((p) => (
            <button
              key={p}
              className={page === p ? "selected" : ""}
              aria-current={page === p ? "page" : undefined}
              aria-label={p}
              onClick={() => navigate(p)}
            >
              <Icon name={p.toLowerCase()} />
              <span className="nav-label" data-short={p === "Conversations" ? "Chats" : undefined}>{p}</span>
              {p === "Activity" && reviewCount > 0 && (
                <b aria-hidden="true" className="nav-count">
                  {reviewCount}
                </b>
              )}
            </button>
          ))}
        </nav>
        <div className="rail-foot">
          <span
            className={
              "link-state " + (state?.transport_ready ? "ready" : "down")
            }
            title={
              lastUpdated
                ? "Last checked " + lastUpdated.toLocaleTimeString()
                : undefined
            }
          >
            <span className="dot" />
            {state?.transport_ready ? "Phone connected" : connectionLabel(state?.connection_state)}
          </span>
        </div>
      </aside>
      <main className="workspace" id="workspace">
        <header className="topbar">
          <h1>{page}</h1>
          {state?.fixture && (
            <span
              className="pill demo"
              title="All conversations are synthetic. Nothing is sent and no AI is called."
            >
              Demo data
            </span>
          )}
          <span className="topbar-gap" />
          <button
            className="icon ghost"
            aria-label={
              theme === "dark" ? "Switch to light theme" : "Switch to dark theme"
            }
            title="Change theme"
            onClick={() => setTheme(theme === "dark" ? "light" : "dark")}
          >
            <Icon name={theme === "dark" ? "sun" : "moon"} size={18} />
          </button>
          <button
            type="button"
            role="switch"
            aria-checked={live}
            aria-label="Bot sending"
            title={
              live
                ? "Pause all sending. Queued and waiting replies are cancelled."
                : "Resume sending"
            }
            className={"power " + (live ? "on" : "off")}
            disabled={busy || !state || (state.paused && !state.transport_ready)}
            onClick={() => void togglePaused()}
          >
            <span className="power-track" aria-hidden="true">
              <span className="power-knob" />
            </span>
            <span className="power-label">{live ? "Live" : "Paused"}</span>
          </button>
        </header>
        {error && (
          <div className="banner" role="alert">
            <Icon name="warning" size={17} />
            {error}
          </div>
        )}
        {page === "Home" && (
          <Home
            state={state}
            inbox={inbox}
            jobs={jobs}
            conversations={conversations}
            commands={workspace.config?.commands ?? []}
            busy={busy}
            act={act}
            onToggle={() => void togglePaused()}
            navigate={navigate}
            openConversation={(id) => {
              setSelected(id);
              navigate("Conversations");
            }}
          />
        )}
        {page === "Conversations" && (
          <ConversationsPage
            conversations={conversations}
            inbox={inbox}
            state={state}
            busy={busy}
            act={act}
            selected={selected}
            onSelect={setSelected}
            partial={dashboard.discoveryPartial}
            canLoadMore={!!inbox?.enabled && !!dashboard.conversationCursor}
            onLoadMore={loadMoreConversations}
            onResume={() => void togglePaused()}
          />
        )}
        {page === "Commands" && (
          <CommandsPage
            workspace={workspace}
            conversations={conversations}
            busy={busy}
          />
        )}
        {page === "Activity" && (
          <ActivityPage
            jobs={jobs}
            conversations={conversations}
            busy={busy}
            act={act}
          />
        )}
        {page === "Archive" && <ArchivePage conversations={conversations} inbox={inbox} state={state} workspace={workspace} />}
        {page === "Settings" && (
          <SettingsPage
            workspace={workspace}
            conversations={conversations}
            state={state}
            busy={busy}
            act={act}
            notify={notify}
            onSignOut={() =>
              dirty ? setConfirmSignOut(true) : void signOut()
            }
          />
        )}
      </main>
      {dirty && (
        <div className="savebar" role="region" aria-label="Unsaved changes">
          <span className="savebar-text">
            {workspace.saveError ? (
              <span role="alert" className="savebar-error">
                <Icon name="warning" size={15} />
                {workspace.saveError}
              </span>
            ) : (
              "Unsaved changes"
            )}
          </span>
          <button
            type="button"
            className="ghost"
            disabled={workspace.saving}
            onClick={workspace.discard}
          >
            Discard
          </button>
          <button
            type="button"
            className="primary"
            disabled={workspace.saving}
            onClick={() => void saveAll()}
          >
            {workspace.saving ? "Saving…" : "Save changes"}
          </button>
        </div>
      )}
      <div className="toasts" aria-live="polite">
        {toasts.map((t) => (
          <div key={t.id} className="toast" role="status">
            <Icon name="check" size={15} />
            {t.text}
          </div>
        ))}
      </div>
      {confirmSignOut && (
        <Confirmation
          title="Sign out and discard changes?"
          description="You have unsaved changes. Signing out throws them away."
          confirmLabel="Sign out"
          cancelLabel="Cancel"
          onCancel={() => setConfirmSignOut(false)}
          onConfirm={() => {
            setConfirmSignOut(false);
            workspace.discard();
            void signOut();
          }}
        />
      )}
    </div>
  );
}

createRoot(document.getElementById("root")!).render(<App />);

// Makes the dashboard installable and lets it open offline. Browsers only
// allow this on HTTPS or localhost; elsewhere it is skipped.
if ("serviceWorker" in navigator && window.isSecureContext && import.meta.env.PROD)
  window.addEventListener("load", () => {
    void navigator.serviceWorker.register("/sw.js").catch(() => {});
  });
