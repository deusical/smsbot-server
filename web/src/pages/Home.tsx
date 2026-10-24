import type { Conversation, InboxSettings, Job, State } from "../lib/api";
import { connectionSummary } from "../lib/connection";
import { ActivityFeed } from "./Activity";
import { Icon } from "../components/Icon";
import { Avatar } from "../components/ui";

type Destination = "Conversations" | "Commands" | "Activity";

export function Home({
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
  inbox: InboxSettings | null;
  jobs: Job[];
  conversations: Conversation[];
  commands: { name: string; enabled: boolean; type: string }[];
  busy: boolean;
  act: (fn: () => Promise<unknown>, refreshAfter?: boolean) => Promise<void>;
  onToggle: () => void;
  navigate: (p: Destination) => void;
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
        <section className="panel home-list">
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
              ].slice(0, 40)}
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
          <section className="panel home-list">
            <div className="panel-head">
              <h2>
                People
                {enrolled.length > 0 && (
                  <span className="count">{enrolled.length}</span>
                )}
              </h2>
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
                {enrolled.map((c) => (
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
        <span className="pulse-legend" aria-hidden="true">
          <span className="key ok" />
          Sent
          <span className="key other" />
          Other
        </span>
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
        {buckets.map((_, i) => (
          <span key={i}>
            {i === 23
              ? "now"
              : i % 6 === 0
                ? new Date(now - (23 - i) * 3600000).toLocaleTimeString([], { hour: "numeric" })
                : ""}
          </span>
        ))}
      </div>
    </figure>
  );
}
