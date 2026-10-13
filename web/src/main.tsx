import { useEffect, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import {
  api,
  ApiError,
  type State,
  type Conversation,
  type Job,
  type Client,
  type InboxSettings as InboxConfig,
  inboxAllows,
} from "./api";
import { ConfigurationEditor } from "./Configuration";
import { ConversationHistory } from "./ConversationHistory";
import { InboxSettings } from "./InboxSettings";
import { Activity } from "./Activity";
import { Icon } from "./Icon";
import { Confirmation } from "./Confirmation";
import "./style.css";

const pages = [
  "Home",
  "Conversations",
  "Commands",
  "Activity",
  "Settings",
] as const;
type Page = (typeof pages)[number];
const descriptions: Record<Page, string> = {
  Home: "A clear view of your bot and everything it’s working on.",
  Conversations:
    "Choose who your bot can reply to, and keep the conversation in view.",
  Commands: "Turn a message into something useful.",
  Activity: "Follow every request, from arrival to outcome.",
  Settings: "Your models, boundaries, and connected devices.",
};
function App() {
  const [page, setPage] = useState<Page>("Home"),
    [state, setState] = useState<State | null>(null);
  const [signedIn, setSignedIn] = useState(false),
    [token, setToken] = useState(""),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const [conversations, setConversations] = useState<Conversation[]>([]),
    [jobs, setJobs] = useState<Job[]>([]),
    [clients, setClients] = useState<Client[]>([]);
  const [selected, setSelected] = useState(""),
    [newClient, setNewClient] = useState<Client | null>(null),
    [clientLabel, setClientLabel] = useState("Android client");
  const [theme, setTheme] = useState<"dark" | "light">("dark"),
    [search, setSearch] = useState(""),
    [conversationFilter, setConversationFilter] = useState("all");
  const [configDirty, setDirty] = useState(false),
    [destination, setDestination] = useState<Page | null>(null),
    [lastUpdated, setLastUpdated] = useState<Date | null>(null);
  const [inbox, setInbox] = useState<InboxConfig | null>(null),
    [inboxDirty, setInboxDirty] = useState(false),
    [conversationCursor, setConversationCursor] = useState("");
  const [discoveryPartial, setDiscoveryPartial] = useState(false);
  const dirty = configDirty || inboxDirty;
  const paginatedConversations = useRef(false);
  const refreshing = useRef(false),
    stateRevision = useRef(0),
    sessionRevision = useRef(0);
  useEffect(() => {
    document.documentElement.dataset.theme = theme;
  }, [theme]);
  useEffect(() => {
    if (!dirty) return;
    const before = (event: BeforeUnloadEvent) => event.preventDefault();
    window.addEventListener("beforeunload", before);
    return () => window.removeEventListener("beforeunload", before);
  }, [dirty]);
  function navigate(next: Page) {
    if (next === page || busy) return;
    if (dirty) setDestination(next);
    else {
      setPage(next);
      window.scrollTo(0, 0);
    }
  }
  function clearSession() {
    sessionRevision.current++;
    setSignedIn(false);
    setState(null);
    setJobs([]);
    setConversations([]);
    setClients([]);
    setNewClient(null);
    setDirty(false);
    setPage("Home");
    setSelected("");
    setInbox(null);
    setInboxDirty(false);
    setConversationCursor("");
    paginatedConversations.current = false;
  }
  async function refresh() {
    if (refreshing.current) return;
    refreshing.current = true;
    let stage = "state";
    const session = sessionRevision.current;
    try {
      const revision = stateRevision.current;
      const s = await api<State>("/state");
      if (session !== sessionRevision.current) return;
      if (revision === stateRevision.current) setState(s);
      setSignedIn(true);
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
      if (s.transport_ready) {
        stage = "conversations";
        if (inboxConfig?.enabled) {
          const discovered = await api<{
            conversations: Conversation[];
            next_cursor: string;
            partial?: boolean;
          }>("/inbox/conversations");
          if (session !== sessionRevision.current) return;
          if (!paginatedConversations.current)
            setConversationCursor(discovered.next_cursor);
          setConversations((previous) => {
            const m = new Map(previous.map((c) => [c.id, c]));
            for (const c of discovered.conversations) m.set(c.id, c);
            return [...m.values()];
          });
          setDiscoveryPartial(!!discovered.partial);
        } else {
          const rows = await api<Conversation[]>("/conversations");
          if (session !== sessionRevision.current) return;
          setConversations(rows);
          setConversationCursor("");
          paginatedConversations.current = false;
          setDiscoveryPartial(false);
        }
        setError("");
      } else
        setError(
          "Server reachable; phone connection unavailable. Conversation data may be stale.",
        );
      setLastUpdated(new Date());
    } catch (e) {
      if (session !== sessionRevision.current) return;
      if (e instanceof ApiError && e.status === 401) clearSession();
      else if (stage === "conversations")
        setError(
          "Could not refresh conversations. Displayed conversation data may be stale.",
        );
      else if (e instanceof ApiError)
        setError("Could not refresh server data. Displayed data may be stale.");
      else setError("Connection lost. Displayed data may be stale.");
    } finally {
      refreshing.current = false;
    }
  }
  useEffect(() => {
    void refresh();
    const timer = setInterval(() => void refresh(), 3000);
    return () => clearInterval(timer);
  }, []);
  useEffect(() => {
    if (!signedIn) return;
    let active = true;
    if (page === "Settings")
      api<Client[]>("/clients")
        .then((x) => {
          if (active) setClients(x);
        })
        .catch(() => {
          if (active) setError("Could not load client access.");
        });
    return () => {
      active = false;
    };
  }, [page, signedIn]);
  async function act(fn: () => Promise<unknown>, refreshAfter = true) {
    setBusy(true);
    setError("");
    try {
      await fn();
      if (refreshAfter) await refresh();
    } catch (e) {
      setError(
        e instanceof ApiError
          ? e.message.replaceAll("_", " ")
          : "Could not connect to the server.",
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
  const reviewCount = jobs.filter(
    (j) => j.state === "awaiting_approval",
  ).length;
  const enrolled = conversations.filter((c) => c.enrolled);
  const selectedConversation = conversations.find((c) => c.id === selected);
  const visibleConversations = conversations.filter(
    (c) =>
      (conversationFilter === "all" ||
        (conversationFilter === "enrolled" && c.enrolled) ||
        (conversationFilter === "available" &&
          !c.enrolled &&
          !c.group &&
          c.writable)) &&
      `${c.label} ${c.protocol}`.toLowerCase().includes(search.toLowerCase()),
  );
  if (!signedIn)
    return (
      <div className="login-layout">
        <section className="login-story">
          <div className="brand">
            <span className="mark">
              <Icon name="commands" size={23} />
            </span>
            SmsBot<span className="brand-tag">PERSONAL WORKSPACE</span>
          </div>
          <div>
            <p className="eyebrow">A LITTLE HELP. JUST A TEXT AWAY.</p>
            <h1>
              Your messages.
              <br />
              Your rules.
              <br />
              <em>Your bot.</em>
            </h1>
            <p>
              A quiet place to manage commands, trusted conversations, and the
              small things you’d like to automate.
            </p>
            <div className="login-flow">
              <Icon name="conversations" />
              <span>Message</span>
              <Icon name="arrow" />
              <Icon name="commands" />
              <span>Command</span>
              <Icon name="arrow" />
              <Icon name="spark" />
              <span>Reply</span>
            </div>
          </div>
          <small>Self-hosted, with you in control.</small>
        </section>
        <main className="login">
          <span className="pill">
            <Icon name="shield" size={14} />
            PRIVATE WORKSPACE
          </span>
          <h2>Welcome back.</h2>
          <p>
            Connect to your personal control room using the owner credential
            from local setup.
          </p>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              void act(async () => {
                await api("/login", "POST", { token });
                sessionRevision.current++;
                setToken("");
              });
            }}
          >
            <label htmlFor="owner">Owner credential</label>
            <input
              id="owner"
              type="password"
              autoComplete="off"
              placeholder="Enter your owner credential"
              value={token}
              onChange={(e) => setToken(e.target.value)}
              required
            />
            <button disabled={busy || !token}>
              {busy ? "Connecting…" : "Open dashboard"}
              <Icon name="arrow" />
            </button>
          </form>
          {error && (
            <p role="alert" className="error">
              {error}
            </p>
          )}
          <div className="login-note">
            <Icon name="shield" />
            <small>
              Your credential stays out of browser storage. Sessions use a
              server-managed cookie.
            </small>
          </div>
        </main>
      </div>
    );
  return (
    <div className="shell">
      <a className="skip-link" href="#workspace">
        Skip to content
      </a>
      <aside>
        <div className="brand">
          <span className="mark">
            <Icon name="commands" size={22} />
          </span>
          SmsBot
        </div>
        <div className="workspace-label">
          <span className="workspace-avatar">P</span>
          <div>
            <strong>Personal workspace</strong>
            <small>
              {state?.fixture ? "Offline demo" : "Self-hosted service"}
            </small>
          </div>
        </div>
        <p className="nav-label">WORKSPACE</p>
        <nav aria-label="Main navigation">
          {pages.map((p) => (
            <button
              key={p}
              className={page === p ? "selected" : ""}
              aria-current={page === p ? "page" : undefined}
              onClick={() => navigate(p)}
            >
              <Icon name={p.toLowerCase()} />
              <span>{p}</span>
              {p === "Activity" && reviewCount > 0 && (
                <b aria-hidden="true" className="nav-count">
                  {reviewCount}
                </b>
              )}
            </button>
          ))}
        </nav>
        <div className="aside-foot">
          <div className="connection-card">
            <span
              className={
                "dot " + (state?.transport_ready ? "ready" : "offline")
              }
            />
            <div>
              <strong>
                {state?.transport_ready
                  ? "Connection ready"
                  : "Connection unavailable"}
              </strong>
              <small>
                {state?.fixture ? "Synthetic fixture" : "Google Messages"}
              </small>
            </div>
          </div>
          <small>Made for your conversations.</small>
        </div>
      </aside>
      <main className="workspace" id="workspace">
        <header>
          <div>
            <p className="eyebrow">
              PERSONAL WORKSPACE <span>/ {page.toUpperCase()}</span>
            </p>
            <h1>{page}</h1>
            <p className="page-description">{descriptions[page]}</p>
          </div>
          <div className="header-actions">
            <button
              className="icon-button secondary"
              aria-label={
                theme === "dark"
                  ? "Switch to light theme"
                  : "Switch to dark theme"
              }
              title="Change theme"
              onClick={() => setTheme(theme === "dark" ? "light" : "dark")}
            >
              <Icon name={theme === "dark" ? "sun" : "moon"} />
            </button>
            <button
              className={
                state?.paused ? "enable-button" : "secondary pause-button"
              }
              disabled={
                busy || !state || (state.paused && !state.transport_ready)
              }
              onClick={() => void togglePaused()}
            >
              <Icon name={state?.paused ? "play" : "pause"} size={16} />
              {inbox?.enabled
                ? state?.paused
                  ? "Resume sending"
                  : "Pause all sending"
                : state?.paused
                  ? "Enable bot"
                  : "Pause bot"}
            </button>
          </div>
        </header>
        {state?.fixture && (
          <div className="banner fixture-banner">
            <Icon name="spark" />
            <span>
              Offline demo{" "}
              <small>
                All conversations are synthetic. No Google connection, SMS sends
                or AI charges.
              </small>
            </span>
          </div>
        )}
        {error && (
          <div className="error" role="alert">
            <Icon name="warning" />
            {error}
          </div>
        )}
        {page === "Home" && (
          <>
            <section className="hero">
              <div>
                <span className="pill">
                  <span
                    className={"dot " + (state?.paused ? "paused" : "ready")}
                  />
                  {state?.paused
                    ? "PAUSED"
                    : inbox?.automatic_replies === false
                      ? "MANUAL REPLIES"
                      : "ACTIVE"}
                </span>
                <h2>
                  {state?.paused
                    ? "A moment of quiet."
                    : "A little less on your plate."}
                </h2>
                <p>
                  {state?.paused
                    ? inbox?.enabled
                      ? "All sending is paused. Your opted-in inbox continues receiving messages."
                      : "Your workspace is ready. Enable your bot when you want it to respond to new commands."
                    : inbox?.automatic_replies === false
                      ? "Automatic command replies are off. You can reply manually in accessible conversations."
                      : "Your bot is listening to trusted conversations. Follow requests here, even when your other interfaces are closed."}
                </p>
                <button
                  className="secondary"
                  onClick={() => navigate("Conversations")}
                >
                  Manage conversations
                  <Icon name="arrow" size={17} />
                </button>
              </div>
              <div className="flow-preview" aria-hidden="true">
                {[
                  {
                    icon: "conversations",
                    title: "A message arrives",
                    detail: "From a trusted conversation",
                  },
                  {
                    icon: "commands",
                    title: "Your command runs",
                    detail: "With your rules and model",
                  },
                  {
                    icon: "check",
                    title: "A helpful reply",
                    detail: "You stay in control",
                  },
                ].map((step) => (
                  <div key={step.icon}>
                    <span>
                      <Icon name={step.icon} />
                    </span>
                    <strong>{step.title}</strong>
                    <small>{step.detail}</small>
                  </div>
                ))}
              </div>
            </section>
            <div className="stats">
              <article>
                <span className="stat-label">
                  Phone connection
                  <Icon name="phone" />
                </span>
                <strong>
                  {state?.transport_ready ? "Ready" : "Unavailable"}
                </strong>
                <p>
                  {state?.fixture
                    ? "Offline simulator"
                    : "Paired Google Messages phone"}
                </p>
              </article>
              <article>
                <span className="stat-label">
                  Trusted conversations
                  <Icon name="shield" />
                </span>
                <strong>{enrolled.length.toString().padStart(2, "0")}</strong>
                <p>Explicitly enrolled participants</p>
              </article>
              <article>
                <span className="stat-label">
                  Requests in view
                  <Icon name="activity" />
                </span>
                <strong>{jobs.length.toString().padStart(2, "0")}</strong>
                <p>
                  {jobs.filter((j) => j.state === "unknown_submission").length}{" "}
                  uncertain · {reviewCount} awaiting approval
                </p>
              </article>
            </div>
            <div className="overview-grid">
              <section className="card">
                <div className="section-heading">
                  <div>
                    <p className="eyebrow">THE LATEST</p>
                    <h2>Recent activity</h2>
                  </div>
                  <button
                    className="text-button"
                    onClick={() => navigate("Activity")}
                  >
                    View journal
                    <Icon name="arrow" size={16} />
                  </button>
                </div>
                <Activity
                  jobs={jobs.slice(0, 5)}
                  conversations={conversations}
                  compact
                />
              </section>
              <section className="card quick-start">
                <p className="eyebrow">YOUR NEXT MOVE</p>
                <h2>Make it your own.</h2>
                <p className="muted">
                  Start with a useful command. Decide where it works and when
                  you want to approve a reply.
                </p>
                <button
                  className="quick-action"
                  onClick={() => navigate("Commands")}
                >
                  <span className="quick-icon">
                    <Icon name="commands" />
                  </span>
                  <span>
                    <strong>Build a command</strong>
                    <small>Fixed replies or AI instructions</small>
                  </span>
                  <Icon name="arrow" size={17} />
                </button>
                <button
                  className="quick-action"
                  onClick={() =>
                    navigate(reviewCount ? "Activity" : "Settings")
                  }
                >
                  <span className="quick-icon">
                    <Icon name={reviewCount ? "clock" : "settings"} />
                  </span>
                  <span>
                    <strong>
                      {reviewCount
                        ? "Review pending requests"
                        : "Tune your workspace"}
                    </strong>
                    <small>
                      {reviewCount
                        ? `${reviewCount} waiting for your approval`
                        : "Models, limits, and connected devices"}
                    </small>
                  </span>
                  <Icon name="arrow" size={17} />
                </button>
                <div className="privacy-note">
                  <Icon name="shield" size={18} />
                  <small>
                    Only enrolled individual conversations can run commands.
                  </small>
                </div>
              </section>
            </div>
          </>
        )}
        {page === "Conversations" && (
          <>
            {inbox?.enabled && state?.inbox_capture_gap && (
              <p role="status" className="banner">
                Some messages could not be captured. Load older phone history to
                recover available text.
              </p>
            )}
            <div className="conversation-grid">
              <section className="card thread-list">
                <div className="section-heading">
                  <h2>Your conversations</h2>
                  <span className="count-badge">{conversations.length}</span>
                </div>
                <label className="search-control">
                  <Icon name="search" />
                  <input
                    aria-label="Search conversations"
                    type="search"
                    placeholder="Find a conversation"
                    value={search}
                    onChange={(e) => setSearch(e.target.value)}
                  />
                </label>
                <div
                  className="filter-tabs"
                  role="group"
                  aria-label="Filter conversations"
                >
                  {[
                    { id: "all", label: "All" },
                    { id: "enrolled", label: "Enrolled" },
                    { id: "available", label: "Available" },
                  ].map((f) => (
                    <button
                      key={f.id}
                      className={conversationFilter === f.id ? "selected" : ""}
                      aria-pressed={conversationFilter === f.id}
                      onClick={() => setConversationFilter(f.id)}
                    >
                      {f.label}
                    </button>
                  ))}
                </div>
                <div className="conversation-list">
                  {visibleConversations.map((c) => (
                    <article
                      className={
                        "conversation " + (selected === c.id ? "selected" : "")
                      }
                      key={c.id}
                    >
                      <span className="avatar" aria-hidden="true">
                        {c.label.slice(0, 1).toUpperCase()}
                      </span>
                      <button
                        className="thread-button"
                        disabled={!c.enrolled && !inboxAllows(inbox, c)}
                        onClick={() => setSelected(c.id)}
                      >
                        <strong>{c.label}</strong>
                        <small>
                          {c.protocol} ·{" "}
                          {c.group
                            ? "Group excluded"
                            : c.enrolled
                              ? "Enrolled"
                              : "Not enrolled"}
                        </small>
                      </button>
                      <button
                        className="secondary"
                        disabled={busy || c.group || !c.writable}
                        onClick={() =>
                          void act(() =>
                            c.enrolled
                              ? api(
                                  `/enrollment/${encodeURIComponent(c.id)}`,
                                  "DELETE",
                                )
                              : api("/enrollment", "POST", {
                                  conversation_id: c.id,
                                  participant_id: c.participant_id,
                                }),
                          )
                        }
                      >
                        {c.enrolled ? "Remove" : "Enroll"}
                      </button>
                    </article>
                  ))}
                </div>
                {!visibleConversations.length && (
                  <div className="empty">
                    <Icon name="search" />
                    <strong>No conversations found</strong>
                    <p>
                      {search
                        ? "Try another name or clear the filter."
                        : "Available conversations appear when the paired phone is ready."}
                    </p>
                  </div>
                )}
                {discoveryPartial && (
                  <p role="status" className="banner">
                    Some conversations could not be refreshed. The available
                    page is shown; try again later.
                  </p>
                )}
                <p className="list-note">
                  <Icon name="shield" size={15} />
                  Enrollment authorizes a stable participant. Names are labels.
                </p>
                {inbox?.enabled && conversationCursor && (
                  <button
                    className="secondary"
                    disabled={busy}
                    onClick={() =>
                      void act(async () => {
                        const p = await api<{
                          conversations: Conversation[];
                          next_cursor: string;
                          partial?: boolean;
                        }>(
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
                          p.next_cursor === conversationCursor
                            ? ""
                            : p.next_cursor,
                        );
                      }, false)
                    }
                  >
                    Load more conversations
                  </button>
                )}
              </section>
              <section className="card inbox-card">
                <div className="section-heading">
                  <div>
                    <p className="eyebrow">RETAINED HISTORY</p>
                    <h2>
                      {selectedConversation?.label ??
                        "A conversation, in context."}
                    </h2>
                  </div>
                  {selectedConversation?.enrolled && (
                    <span className="pill">
                      <Icon name="shield" size={13} />
                      ENROLLED
                    </span>
                  )}
                </div>
                {!selected ? (
                  <div className="empty inbox-empty">
                    <div className="empty-art">
                      <Icon name="conversations" size={36} />
                    </div>
                    <h3>Your conversations live here.</h3>
                    <p>
                      Select an accessible conversation to browse retained
                      history, import available text, or compose a reply.
                    </p>
                  </div>
                ) : selectedConversation &&
                  (selectedConversation.enrolled ||
                    inboxAllows(inbox, selectedConversation)) ? (
                  <ConversationHistory
                    key={
                      selected +
                      String(inboxAllows(inbox, selectedConversation))
                    }
                    conversation={selectedConversation}
                    inbox={
                      inboxAllows(inbox, selectedConversation)
                        ? inbox
                        : undefined
                    }
                    state={state}
                    busy={busy}
                    act={act}
                  />
                ) : (
                  <div className="empty">
                    This conversation is no longer enrolled.
                  </div>
                )}
              </section>
            </div>
          </>
        )}
        {page === "Commands" && (
          <ConfigurationEditor
            key="commands"
            page="Commands"
            conversations={conversations}
            busy={busy}
            act={act}
            onDirtyChange={setDirty}
          />
        )}
        {page === "Activity" && (
          <section className="card">
            <div className="section-heading">
              <div>
                <p className="eyebrow">REQUEST JOURNAL</p>
                <h2>Every request has a story.</h2>
              </div>
              <span className="pill">
                <Icon name="activity" size={14} />
                LIVE VIEW
              </span>
            </div>
            <p className="muted">
              Submitted means the transport accepted the request. Delivery and
              read status require separate evidence.
            </p>
            <Activity
              jobs={jobs}
              conversations={conversations}
              busy={busy}
              act={act}
            />
          </section>
        )}
        {page === "Settings" && (
          <>
            {inbox && (
              <InboxSettings
                saved={inbox}
                conversations={conversations}
                onSaved={setInbox}
                onDirtyChange={setInboxDirty}
              />
            )}
            <ConfigurationEditor
              key="settings"
              page="Settings"
              conversations={conversations}
              busy={busy}
              act={act}
              onDirtyChange={setDirty}
            />
            <section className="card">
              <div className="section-heading">
                <div>
                  <p className="eyebrow">CONNECTED DEVICES</p>
                  <h2>Connection & access</h2>
                </div>
                <Icon name="phone" size={24} />
              </div>
              <div className="connection-summary">
                <span
                  className={
                    "dot " + (state?.transport_ready ? "ready" : "offline")
                  }
                />
                <strong>
                  {state?.transport_ready
                    ? "Connection ready"
                    : "Connection unavailable"}
                </strong>
                <span>
                  API v{state?.api_version} ·{" "}
                  {state?.transport.replaceAll("_", " ")}
                </span>
              </div>
              <p className="muted">
                Create a revocable credential for a native Android client.
                Client credentials can control the bot but cannot administer
                credentials or provider keys.
              </p>
              <div className="access-form">
                <label className="field">
                  Device label
                  <input
                    aria-label="Device label"
                    maxLength={80}
                    value={clientLabel}
                    onChange={(e) => setClientLabel(e.target.value)}
                    placeholder="e.g. Personal phone"
                  />
                </label>
                <button
                  disabled={busy || !clientLabel.trim()}
                  onClick={() =>
                    void act(async () => {
                      const client = await api<Client>("/clients", "POST", {
                        label: clientLabel.trim(),
                      });
                      setNewClient(client);
                      setClients(await api<Client[]>("/clients"));
                    })
                  }
                >
                  <Icon name="plus" size={17} />
                  Authorize Android client
                </button>
              </div>
              {newClient && (
                <div className="credential">
                  <strong>Copy this credential into Android once</strong>
                  <p className="muted">
                    It will not be shown again after dismissal.
                  </p>
                  <input
                    aria-label="New client credential"
                    readOnly
                    type="password"
                    value={newClient.token ?? ""}
                  />
                  <div className="actions">
                    <button
                      className="secondary"
                      onClick={() =>
                        void navigator.clipboard
                          .writeText(newClient.token ?? "")
                          .catch(() =>
                            setError(
                              "Clipboard unavailable; select the credential field locally.",
                            ),
                          )
                      }
                    >
                      <Icon name="copy" size={16} />
                      Copy credential
                    </button>
                    <button
                      className="secondary"
                      onClick={() => setNewClient(null)}
                    >
                      Dismiss
                    </button>
                  </div>
                </div>
              )}
              {clients.map((c) => (
                <div className="client" key={c.id}>
                  <span className="avatar">
                    <Icon name="phone" />
                  </span>
                  <span>
                    <strong>{c.label}</strong>
                    <small>Revocable client access</small>
                  </span>
                  <button
                    className="secondary"
                    disabled={busy}
                    onClick={() =>
                      void act(async () => {
                        await api(`/clients/${c.id}`, "DELETE");
                        setClients(await api<Client[]>("/clients"));
                        if (newClient?.id === c.id) setNewClient(null);
                      })
                    }
                  >
                    Revoke
                  </button>
                </div>
              ))}
            </section>
            <section className="card privacy-card">
              <Icon name="shield" size={26} />
              <div>
                <h2>Private by design.</h2>
                <p>
                  Message bodies and provider credentials are encrypted on disk.
                  Browser storage contains no credentials. Remote access
                  requires your configured private HTTPS origin.
                </p>
                <button
                  className="secondary"
                  disabled={busy || dirty}
                  onClick={() =>
                    void act(async () => {
                      await api("/logout", "POST");
                      clearSession();
                    }, false)
                  }
                >
                  <Icon name="logout" size={16} />
                  Sign out
                </button>
                {dirty && (
                  <p className="muted">
                    Save or discard your configuration draft before signing out.
                  </p>
                )}
              </div>
            </section>
          </>
        )}
        <footer>
          <span>
            SmsBot <span className="footer-separator">/</span>{" "}
            {state?.fixture ? "Synthetic environment" : "Personal workspace"}
          </span>
          <span>
            <span
              className={
                "dot " + (state?.transport_ready ? "ready" : "offline")
              }
            />
            {lastUpdated
              ? "Updated " +
                lastUpdated.toLocaleTimeString([], {
                  hour: "2-digit",
                  minute: "2-digit",
                })
              : "Connecting"}{" "}
            · {theme === "dark" ? "Dark" : "Light"} theme
          </span>
        </footer>
      </main>
      {destination && (
        <Confirmation
          title="Leave without saving?"
          description="Your configuration draft has unsaved changes. Save it first, or discard the draft to continue."
          confirmLabel="Discard changes"
          onCancel={() => setDestination(null)}
          onConfirm={() => {
            setDirty(false);
            setPage(destination);
            setDestination(null);
            window.scrollTo(0, 0);
          }}
        />
      )}
    </div>
  );
}
createRoot(document.getElementById("root")!).render(<App />);
