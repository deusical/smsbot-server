import { useCallback, useEffect, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import {
  api,
  ApiError,
  type State,
  type Conversation,
  type Job,
  type InboxSettings as InboxConfig,
} from "./lib/api";
import { ActivityFeed, ActivityPage } from "./pages/Activity";
import { CommandsPage } from "./pages/Commands";
import { Confirmation } from "./components/Confirmation";
import { ArchivePage } from "./pages/ArchivePage";
import { ConversationsPage } from "./pages/Conversations";
import { clearSeen } from "./lib/conversationOrder";
import { clearPending, usePendingFollower } from "./lib/pendingSends";
import { Icon } from "./components/Icon";
import { SettingsPage } from "./pages/Settings";
import { Avatar, Switch } from "./components/ui";
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
type ConversationPage = {
  conversations: Conversation[];
  next_cursor: string;
  partial?: boolean;
  stale?: boolean;
};

function connectionLabel(status?: string): string {
  switch (status) {
    case "reconnecting": return "Reconnecting to phone";
    case "phone_unreachable": return "Waiting for phone";
    case "login_required": return "Phone login required";
    case "session_conflict": return "Messages session conflict";
    case "account_changed": return "Phone account changed";
    case "storage_error": return "Session could not be saved";
    case "connection_error": return "Phone connection needs attention";
    case "receive_overload": return "Phone event queue overloaded";
    default: return "Phone offline";
  }
}
function connectionSummary(state: State): string {
  const status = state.connection_state;
  if (status && !["reconnecting", "phone_unreachable", "connected"].includes(status)) {
    return connectionLabel(status) + ". Sending is blocked. Resolve the issue and restart the service; review before going live.";
  }
  return state.paused
    ? "Reconnecting to your phone. Sending will stay paused."
    : "Reconnecting to your phone. Sending is blocked and will resume after verification. You can pause to prevent resuming.";
}

function App() {
  const [page, setPage] = useState<Page>("Home"),
    [state, setState] = useState<State | null>(null);
  const [signedIn, setSignedIn] = useState(false),
    [token, setToken] = useState(""),
    // An installed home-screen app should stay signed in like any phone app.
    [remember, setRemember] = useState(
      () => !!window.matchMedia?.("(display-mode: standalone)")?.matches,
    ),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const [conversations, setConversations] = useState<Conversation[]>([]),
    [jobs, setJobs] = useState<Job[]>([]);
  const [selected, setSelected] = useState("");
  const [theme, setTheme] = useState<"dark" | "light">(() =>
    window.matchMedia?.("(prefers-color-scheme: light)")?.matches
      ? "light"
      : "dark",
  );
  const [lastUpdated, setLastUpdated] = useState<Date | null>(null);
  const [inbox, setInbox] = useState<InboxConfig | null>(null),
    [conversationCursor, setConversationCursor] = useState("");
  const [discoveryPartial, setDiscoveryPartial] = useState(false);
  const [toasts, setToasts] = useState<Toast[]>([]),
    [confirmSignOut, setConfirmSignOut] = useState(false);
  const paginatedConversations = useRef(false);
  // The list is refetched when the server reports activity. Opening the app or
  // returning to it also asks the server for one fresh read from the phone.
  const listFetched = useRef({ revision: "", ready: false, partial: false, at: 0 });
  const forceList = useRef(true);
  const refreshing = useRef(false),
    refreshingDetails = useRef(false),
    stateRevision = useRef(0),
    sessionRevision = useRef(0);
  const workspace = useWorkspace(signedIn, inbox, setInbox);
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
  }
  function clearSession() {
    sessionRevision.current++;
    clearSeen();
    listFetched.current = { revision: "", ready: false, partial: false, at: 0 };
    clearPending();
    setSignedIn(false);
    setState(null);
    setJobs([]);
    setConversations([]);
    setPage("Home");
    setSelected("");
    setInbox(null);
    setConversationCursor("");
    paginatedConversations.current = false;
  }
  async function refresh() {
    if (refreshing.current) return;
    refreshing.current = true;
    const session = sessionRevision.current;
    try {
      const revision = stateRevision.current;
      const s = await api<State>("/state");
      if (session !== sessionRevision.current) return;
      if (revision === stateRevision.current) setState(s);
      setSignedIn(true);
      // Startup discovery can be slow. It must not hold login busy or stop
      // readiness/pause polling while the phone is connecting.
      void refreshDetails(s, session);
    } catch (e) {
      if (session !== sessionRevision.current) return;
      if (e instanceof ApiError && e.status === 401) clearSession();
      else setError("Could not refresh from the server. Retrying…");
    } finally {
      refreshing.current = false;
    }
  }
  async function refreshDetails(s: State, session: number) {
    if (refreshingDetails.current) return;
    refreshingDetails.current = true;
    let stage = "settings";
    try {
      let inboxConfig: InboxConfig | null = null;
      if (s.capabilities.includes("full_inbox")) {
        inboxConfig = await api<InboxConfig>("/inbox/settings");
        if (session !== sessionRevision.current) return;
        setInbox((previous) =>
          previous && previous.revision > inboxConfig!.revision
            ? previous
            : inboxConfig,
        );
      }
      stage = "activity";
      const activity = await api<Job[]>("/activity");
      if (session !== sessionRevision.current) return;
      setJobs(activity);
      if (s.transport_ready || s.capabilities.includes("archive_history")) {
        stage = "conversations";
        const force = forceList.current;
        const fresh =
          !force &&
          !!s.conversations_revision &&
          listFetched.current.revision === s.conversations_revision &&
          // A list read while the phone was unavailable holds archived rows only.
          listFetched.current.ready === s.transport_ready &&
          // A partial list is retried, spaced so a failing lookup cannot keep waking the phone.
          !(listFetched.current.partial && Date.now() - listFetched.current.at >= 30000);
        if (fresh) {
          /* nothing changed since the last fetch */
        } else if (inboxConfig?.enabled || s.capabilities.includes("archive_history")) {
          // A return to the app during this read keeps its own forced read.
          forceList.current = false;
          const discovered = await api<ConversationPage>(
            force ? "/inbox/conversations?refresh=1" : "/inbox/conversations",
          ).catch((e: unknown) => {
            if (force) forceList.current = true;
            throw e;
          });
          if (session !== sessionRevision.current) return;
          if (!paginatedConversations.current)
            setConversationCursor(discovered.next_cursor);
          setConversations((previous) => {
            const m = new Map(previous.map((c) => [c.id, c]));
            for (const c of discovered.conversations) m.set(c.id, c);
            return [...m.values()];
          });
          setDiscoveryPartial(!!discovered.partial);
          // A stale page predates recent activity; ask again on the next poll.
          listFetched.current = {
            revision: discovered.stale ? "" : (s.conversations_revision ?? ""),
            ready: s.transport_ready,
            partial: !!discovered.partial,
            at: Date.now(),
          };
        }
        setError("");
      } else
        setError(
          "Your phone isn’t connected. Conversations may be out of date.",
        );
      setLastUpdated(new Date());
    } catch (e) {
      if (session !== sessionRevision.current) return;
      if (e instanceof ApiError && e.status === 401) clearSession();
      else if (stage === "conversations")
        setError(
          "Could not refresh conversations. The list may be out of date.",
        );
      else if (e instanceof ApiError)
        setError("Could not refresh from the server. Retrying…");
      else setError("Connection lost. Retrying…");
    } finally {
      refreshingDetails.current = false;
    }
  }
  useEffect(() => {
    void refresh();
    // A backgrounded phone app shouldn't poll; catch up as soon as it returns.
    const timer = setInterval(() => {
      if (document.visibilityState !== "hidden") void refresh();
    }, 3000);
    const visible = () => {
      if (document.visibilityState !== "visible") return;
      forceList.current = true;
      void refresh();
    };
    document.addEventListener("visibilitychange", visible);
    return () => {
      clearInterval(timer);
      document.removeEventListener("visibilitychange", visible);
    };
  }, []);
  async function act(fn: () => Promise<unknown>, refreshAfter = true) {
    setBusy(true);
    setError("");
    try {
      await fn();
      if (refreshAfter) {
        listFetched.current = { revision: "", ready: false, partial: false, at: 0 };
        await refresh();
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
    await act(async () => {
      stateRevision.current++;
      const updated = await api<State>("/pause", "POST", {
        paused: !state.paused,
      });
      stateRevision.current++;
      setState(updated);
    }, false);
  }
  async function signOut() {
    await act(async () => {
      await api("/logout", "POST");
      clearSession();
    }, false);
  }
  function loadMoreConversations() {
    void act(async () => {
      const p = await api<ConversationPage>(
        `/inbox/conversations?cursor=${encodeURIComponent(conversationCursor)}`,
      );
      setConversations((old) => {
        const m = new Map(old.map((c) => [c.id, c]));
        for (const c of p.conversations) m.set(c.id, c);
        return [...m.values()];
      });
      setDiscoveryPartial(!!p.partial);
      paginatedConversations.current = true;
      setConversationCursor(
        p.next_cursor === conversationCursor ? "" : p.next_cursor,
      );
    }, false);
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
                await api("/login", "POST", { token, remember });
                sessionRevision.current++;
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
              checked={remember}
              onChange={setRemember}
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
            partial={discoveryPartial}
            canLoadMore={!!inbox?.enabled && !!conversationCursor}
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

function Home({
  state,
  inbox,
  jobs,
  conversations,
  commands,
  busy,
  act,
  onToggle,
  navigate,
  openConversation,
}: {
  state: State | null;
  inbox: InboxConfig | null;
  jobs: Job[];
  conversations: Conversation[];
  commands: { name: string; enabled: boolean; type: string }[];
  busy: boolean;
  act: (fn: () => Promise<unknown>, refreshAfter?: boolean) => Promise<void>;
  onToggle: () => void;
  navigate: (p: Page) => void;
  openConversation: (id: string) => void;
}) {
  const enrolled = conversations.filter((c) => c.enrolled);
  const live = !!state && !state.paused;
  const manual = inbox?.enabled && inbox.automatic_replies === false;
  const waiting = jobs.filter((j) => j.state === "awaiting_approval");
  const uncertain = jobs.filter((j) => j.state === "unknown_submission").length;
  const enabled = commands.filter((c) => c.enabled);
  const status = !state
    ? "Connecting"
    : state.paused
      ? "Paused"
      : manual
        ? "Manual"
        : "Live";
  const summary = !state
    ? "Checking the server…"
    : !state.transport_ready
      ? connectionSummary(state)
      : state.paused
        ? inbox?.enabled
          ? "Nothing will be sent. Incoming messages are still saved."
          : "Nothing will be sent until you go live."
        : manual
          ? "Automatic replies are off. You can still text by hand."
          : enrolled.length
            ? `Answering commands from ${enrolled.length} ${enrolled.length === 1 ? "person" : "people"}.`
            : "Live, but no one can use commands yet. Turn on bot replies for a conversation.";
  return (
    <div className="home">
      <section className={"hero-status " + status.toLowerCase()}>
        <div className="hero-copy">
          <span className="hero-state">
            <span className="pulse" aria-hidden="true" />
            <span className="hero-pill">{status.toUpperCase()}</span>
          </span>
          <p className="hero-line">{summary}</p>
          <div className="hero-actions">
            <button
              type="button"
              className={live ? "ghost" : "primary"}
              disabled={busy || !state || (state.paused && !state.transport_ready)}
              onClick={onToggle}
            >
              <Icon name={live ? "pause" : "play"} size={15} />
              {live ? "Pause sending" : "Go live"}
            </button>
            {!enrolled.length && (
              <button
                type="button"
                className="ghost"
                onClick={() => navigate("Conversations")}
              >
                Choose conversations
              </button>
            )}
          </div>
        </div>
        <Pulse jobs={jobs} />
      </section>
      <div className="metric-strip">
        <button
          type="button"
          className={"metric" + (waiting.length ? " hot" : "")}
          onClick={() => navigate("Activity")}
        >
          <span>Needs approval</span>
          <strong>{waiting.length}</strong>
        </button>
        <button
          type="button"
          className={"metric" + (uncertain ? " warn" : "")}
          onClick={() => navigate("Activity")}
        >
          <span>Uncertain sends</span>
          <strong>{uncertain}</strong>
        </button>
        <button
          type="button"
          className="metric"
          onClick={() => navigate("Conversations")}
        >
          <span>Bot replies on</span>
          <strong>{enrolled.length}</strong>
        </button>
        <button
          type="button"
          className="metric"
          onClick={() => navigate("Commands")}
        >
          <span>Commands enabled</span>
          <strong>{enabled.length}</strong>
        </button>
      </div>
      <div className="home-grid">
        <section className="panel">
          <div className="panel-head">
            <h2>
              {waiting.length ? "Waiting for you" : "Recent requests"}
            </h2>
            <button
              type="button"
              className="link"
              onClick={() => navigate("Activity")}
            >
              All activity
            </button>
          </div>
          {jobs.length ? (
            <ActivityFeed
              jobs={[
                ...waiting,
                ...jobs.filter((j) => j.state !== "awaiting_approval"),
              ].slice(0, 6)}
              conversations={conversations}
              busy={busy}
              act={act}
              compact
            />
          ) : (
            <div className="empty small">
              <strong>No requests yet</strong>
              <p>
                Text <code className="bang">!ping</code> from a conversation
                with bot replies on to check everything works.
              </p>
            </div>
          )}
        </section>
        <div className="home-side">
          <section className="panel">
            <div className="panel-head">
              <h2>People</h2>
              <button
                type="button"
                className="link"
                onClick={() => navigate("Conversations")}
              >
                Manage
              </button>
            </div>
            {enrolled.length ? (
              <ul className="people">
                {enrolled.slice(0, 6).map((c) => (
                  <li key={c.id}>
                    <button type="button" onClick={() => openConversation(c.id)}>
                      <Avatar label={c.label} />
                      <span>{c.label}</span>
                      <small>{c.protocol}</small>
                    </button>
                  </li>
                ))}
              </ul>
            ) : (
              <p className="muted">No one has bot replies on.</p>
            )}
          </section>
          <section className="panel">
            <div className="panel-head">
              <h2>Commands</h2>
              <button
                type="button"
                className="link"
                onClick={() => navigate("Commands")}
              >
                Edit
              </button>
            </div>
            <div className="bang-cloud">
              {["ping", "help", "status"].map((n) => (
                <code key={n} className="bang quiet">
                  !{n}
                </code>
              ))}
              {enabled.map((c) => (
                <code key={c.name} className={"bang " + c.type}>
                  !{c.name}
                </code>
              ))}
              {commands.length > enabled.length && (
                <small className="muted">
                  {commands.length - enabled.length} off
                </small>
              )}
            </div>
          </section>
        </div>
      </div>
    </div>
  );
}

/** Requests per hour over the last 24 hours, from the loaded activity window. */
function Pulse({ jobs }: { jobs: Job[] }) {
  const now = Date.now();
  const buckets = Array.from({ length: 24 }, () => ({ ok: 0, other: 0 }));
  for (const j of jobs) {
    const age = (now - Date.parse(j.created_at)) / 3600000;
    if (age < 0 || age >= 24) continue;
    const b = buckets[23 - Math.floor(age)];
    if (["submitted", "delivered", "read"].includes(j.state)) b.ok++;
    else b.other++;
  }
  const max = Math.max(1, ...buckets.map((b) => b.ok + b.other));
  const total = buckets.reduce((t, b) => t + b.ok + b.other, 0);
  // Activity returns the latest 200 requests; if all of them fall inside the
  // window, the real count may be higher.
  const oldest = jobs.length ? Date.parse(jobs[jobs.length - 1].created_at) : now;
  const label = jobs.length >= 200 && now - oldest < 86400000 ? `${total}+` : `${total}`;
  return (
    <figure className="pulse-chart">
      <figcaption>
        <strong>{label}</strong>
        <span>request{total === 1 ? "" : "s"} in the last 24 hours</span>
      </figcaption>
      <div className="bars" role="img" aria-label={`${label} requests in the last 24 hours`}>
        {buckets.map((b, i) => {
          const hour = new Date(now - (23 - i) * 3600000);
          return (
            <span
              key={i}
              className="bar"
              title={`${hour.toLocaleTimeString([], { hour: "numeric" })}: ${b.ok + b.other}`}
            >
              <span
                className="fill other"
                style={{ height: `${((b.ok + b.other) / max) * 100}%` }}
              />
              <span
                className="fill ok"
                style={{ height: `${(b.ok / max) * 100}%` }}
              />
            </span>
          );
        })}
      </div>
      <div className="bars-axis" aria-hidden="true">
        <span>24h ago</span>
        <span>now</span>
      </div>
    </figure>
  );
}

createRoot(document.getElementById("root")!).render(<App />);

// Makes the dashboard installable and lets it open offline. Browsers only
// allow this on HTTPS or localhost; elsewhere it is skipped.
if ("serviceWorker" in navigator && window.isSecureContext && import.meta.env.PROD)
  window.addEventListener("load", () => {
    void navigator.serviceWorker.register("/sw.js").catch(() => {});
  });
