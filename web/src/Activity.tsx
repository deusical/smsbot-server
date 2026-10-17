import { useState } from "react";
import { api, type Conversation, type Job } from "./api";
import { Icon } from "./Icon";
import { Segmented, timeAgo } from "./ui";

export const pendingStates = ["queued", "generating", "awaiting_approval"],
  completedStates = ["submitted", "delivered", "read", "reaction_confirmed"];
const stateLabels: Record<string, string> = {
  queued: "Queued",
  generating: "Writing reply",
  awaiting_approval: "Needs approval",
  submitting: "Sending",
  submitted: "Submitted",
  delivered: "Delivered",
  read: "Displayed",
  reaction_confirmed: "Reaction synced",
  unknown_submission: "Uncertain",
  cancelled: "Cancelled",
};
export const stateLabel = (s: string) =>
  stateLabels[s] ?? s.replaceAll("_", " ");
export function stateTone(s: string) {
  if (s === "awaiting_approval") return "wait";
  if (pendingStates.includes(s) || s === "submitting") return "busy";
  if (completedStates.includes(s)) return "ok";
  if (s === "cancelled") return "quiet";
  return "bad";
}
type Filter = "all" | "review" | "pending" | "complete" | "attention";
const matches = (j: Job, id: Filter) =>
  id === "all" ||
  (id === "review" && j.state === "awaiting_approval") ||
  (id === "pending" && pendingStates.includes(j.state)) ||
  (id === "complete" && completedStates.includes(j.state)) ||
  (id === "attention" && stateTone(j.state) === "bad");

export function ActivityFeed({
  jobs,
  conversations,
  busy = false,
  act,
  compact = false,
}: {
  jobs: Job[];
  conversations: Conversation[];
  busy?: boolean;
  act?: (fn: () => Promise<unknown>) => Promise<void>;
  compact?: boolean;
}) {
  const [open, setOpen] = useState<string | null>(null);
  const label = (id: string) =>
    conversations.find((c) => c.id === id)?.label ?? "Unknown conversation";
  return (
    <ul className={"feed" + (compact ? " compact" : "")}>
      {jobs.map((j) => {
        const expanded = open === j.id;
        return (
          <li key={j.id} className={"feed-row tone-" + stateTone(j.state)}>
            <div className="feed-line">
              <button
                type="button"
                className="feed-main"
                aria-expanded={compact ? undefined : expanded}
                onClick={() => !compact && setOpen(expanded ? null : j.id)}
                tabIndex={compact ? -1 : 0}
              >
                <span className="state-dot" aria-hidden="true" />
                <code className="bang">!{j.command.replace(/^!/, "")}</code>
                <span className="feed-who">{label(j.conversation_id)}</span>
                <time
                  dateTime={j.created_at}
                  title={new Date(j.created_at).toLocaleString()}
                >
                  {timeAgo(j.created_at)}
                </time>
                <span className="status">{stateLabel(j.state)}</span>
              </button>
              {act && pendingStates.includes(j.state) && (
                <span className="feed-actions">
                  {j.state === "awaiting_approval" && (
                    <button
                      type="button"
                      className="small primary"
                      disabled={busy}
                      onClick={() =>
                        void act(() =>
                          api(
                            `/jobs/${encodeURIComponent(j.id)}/approve`,
                            "POST",
                          ),
                        )
                      }
                    >
                      <Icon name="check" size={15} />
                      Approve
                    </button>
                  )}
                  <button
                    type="button"
                    className="small ghost"
                    disabled={busy}
                    aria-label={`Cancel !${j.command.replace(/^!/, "")}`}
                    onClick={() =>
                      void act(() =>
                        api(`/jobs/${encodeURIComponent(j.id)}/cancel`, "POST"),
                      )
                    }
                  >
                    Cancel
                  </button>
                </span>
              )}
            </div>
            {expanded && (
              <div className="feed-detail">
                <dl>
                  <dt>Conversation</dt>
                  <dd>{label(j.conversation_id)}</dd>
                  <dt>Received</dt>
                  <dd>{new Date(j.created_at).toLocaleString()}</dd>
                  {j.code && (
                    <>
                      <dt>Result</dt>
                      <dd>{j.code.replaceAll("_", " ")}</dd>
                    </>
                  )}
                  {j.model && (
                    <>
                      <dt>Model</dt>
                      <dd>{j.model}</dd>
                      {j.requested_reasoning && (
                        <>
                          <dt>Reasoning</dt>
                          <dd>{j.requested_reasoning.replaceAll("_", " ")}</dd>
                        </>
                      )}
                      <dt>Usage</dt>
                      <dd>
                        {(
                          (j.prompt_tokens ?? 0) + (j.completion_tokens ?? 0)
                        ).toLocaleString()}{" "}
                        tokens, {(j.reasoning_tokens ?? 0).toLocaleString()}{" "}
                        reasoning tokens
                      </dd>
                      <dt>Cost</dt>
                      <dd>
                        {j.cost === undefined
                          ? "Cost unavailable"
                          : "$" + j.cost.toFixed(6)}
                      </dd>
                    </>
                  )}
                  <dt>Request ID</dt>
                  <dd className="mono">{j.id}</dd>
                </dl>
                {j.state === "unknown_submission" && (
                  <p className="note warn">
                    The phone may or may not have sent this. It won’t be retried
                    automatically. Check the conversation on your phone.
                  </p>
                )}
                {j.state === "submitted" && (
                  <p className="note">
                    Google Messages accepted the reply. Delivery is confirmed
                    separately.
                  </p>
                )}
              </div>
            )}
          </li>
        );
      })}
    </ul>
  );
}

export function ActivityPage({
  jobs,
  conversations,
  busy,
  act,
}: {
  jobs: Job[];
  conversations: Conversation[];
  busy: boolean;
  act: (fn: () => Promise<unknown>) => Promise<void>;
}) {
  const [filter, setFilter] = useState<Filter>("all"),
    [search, setSearch] = useState("");
  const label = (id: string) =>
    conversations.find((c) => c.id === id)?.label ?? "";
  const shown = jobs.filter(
    (j) =>
      matches(j, filter) &&
      `${j.command} ${label(j.conversation_id)} ${stateLabel(j.state)} ${j.code ?? ""}`
        .toLowerCase()
        .includes(search.toLowerCase()),
  );
  const known = jobs.filter((j) => j.cost !== undefined),
    unknown = jobs.filter((j) => j.model && j.cost === undefined).length;
  const count = (f: Filter) => jobs.filter((j) => matches(j, f)).length;
  return (
    <div className="page-stack">
      <div className="metric-strip activity-metrics">
        <div className={count("review") ? "metric hot" : "metric"}>
          <span>Needs approval</span>
          <strong>{count("review")}</strong>
        </div>
        <div className="metric">
          <span>Uncertain</span>
          <strong>{jobs.filter((j) => j.state === "unknown_submission").length}</strong>
        </div>
        <div className="metric">
          <span>Requests loaded</span>
          <strong>{jobs.length}</strong>
        </div>
        <div className="metric">
          <span>Reported spend</span>
          <strong>
            {known.length
              ? "$" + known.reduce((t, j) => t + j.cost!, 0).toFixed(4)
              : "—"}
          </strong>
          {unknown > 0 && (
            <small>
              {unknown} request{unknown === 1 ? "" : "s"} with unavailable cost
            </small>
          )}
        </div>
      </div>
      <section className="panel">
        <div className="toolbar">
          <Segmented
            label="Filter activity"
            value={filter}
            onChange={setFilter}
            options={[
              { value: "all", label: "All", count: count("all") },
              { value: "review", label: "Needs approval", count: count("review") },
              { value: "pending", label: "In progress", count: count("pending") },
              { value: "complete", label: "Sent", count: count("complete") },
              { value: "attention", label: "Problems", count: count("attention") },
            ]}
          />
          <label className="search">
            <Icon name="search" size={16} />
            <input
              aria-label="Search activity"
              type="search"
              placeholder="Search"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
            />
          </label>
        </div>
        {shown.length ? (
          <ActivityFeed
            jobs={shown}
            conversations={conversations}
            busy={busy}
            act={act}
          />
        ) : (
          <div className="empty">
            <strong>
              {jobs.length
                ? "Nothing matches this view"
                : "No requests yet"}
            </strong>
            <p>
              {filter === "review" && !search
                ? "Nothing is waiting for approval."
                : jobs.length
                  ? "Try a different filter or search."
                  : "When someone texts a command to an enrolled conversation, it shows up here."}
            </p>
            {(search || filter !== "all") && (
              <button
                type="button"
                className="ghost"
                onClick={() => {
                  setSearch("");
                  setFilter("all");
                }}
              >
                Clear filters
              </button>
            )}
          </div>
        )}
      </section>
    </div>
  );
}
