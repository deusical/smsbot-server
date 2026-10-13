import { useState } from "react";
import { api, type Conversation, type Job } from "./api";
import { Icon } from "./Icon";
type Props = {
  jobs: Job[];
  conversations: Conversation[];
  busy?: boolean;
  act?: (fn: () => Promise<unknown>) => Promise<void>;
  compact?: boolean;
};
const pending = ["queued", "generating", "awaiting_approval"],
  completed = ["submitted", "delivered", "read"];
export function Activity({
  jobs,
  conversations,
  busy = false,
  act,
  compact = false,
}: Props) {
  const [filter, setFilter] = useState("all"),
    [search, setSearch] = useState("");
  const label = (id: string) =>
    conversations.find((c) => c.id === id)?.label ?? "Conversation unavailable";
  const matches = (j: Job, id: string) =>
    id === "all" ||
    (id === "review" && j.state === "awaiting_approval") ||
    (id === "pending" && pending.includes(j.state)) ||
    (id === "complete" && completed.includes(j.state)) ||
    (id === "attention" &&
      !pending.includes(j.state) &&
      !completed.includes(j.state) &&
      j.state !== "cancelled");
  const groups = [
    { id: "all", label: "All requests" },
    { id: "review", label: "Needs review" },
    { id: "pending", label: "In progress" },
    { id: "complete", label: "Completed" },
    { id: "attention", label: "Attention" },
  ];
  const shown = jobs.filter(
    (j) =>
      compact ||
      (matches(j, filter) &&
        `${j.command} ${label(j.conversation_id)} ${j.state} ${j.code ?? ""}`
          .toLowerCase()
          .includes(search.toLowerCase())),
  );
  const known = jobs.filter((j) => j.cost !== undefined),
    unknown = jobs.filter((j) => j.model && j.cost === undefined).length;
  return (
    <>
      {!compact && (
        <>
          <div className="activity-metrics">
            <div>
              <small>Awaiting your approval</small>
              <strong>
                {jobs.filter((j) => j.state === "awaiting_approval").length}
              </strong>
            </div>
            <div>
              <small>Recent requests loaded</small>
              <strong>{jobs.length}</strong>
            </div>
            <div>
              <small>Reported spend (recent requests)</small>
              <strong>
                {known.length
                  ? "$" +
                    known.reduce((total, j) => total + j.cost!, 0).toFixed(4)
                  : "—"}
              </strong>
              <small>
                {unknown
                  ? `${unknown} request${unknown === 1 ? "" : "s"} with unavailable cost`
                  : "Based on available provider reports"}
              </small>
            </div>
          </div>
          <div className="toolbar">
            <div
              className="filter-tabs"
              role="group"
              aria-label="Filter activity"
            >
              {groups.map((g) => (
                <button
                  key={g.id}
                  className={filter === g.id ? "selected" : ""}
                  aria-pressed={filter === g.id}
                  onClick={() => setFilter(g.id)}
                >
                  {g.label}
                  <span>{jobs.filter((j) => matches(j, g.id)).length}</span>
                </button>
              ))}
            </div>
            <label className="search-control">
              <Icon name="search" />
              <input
                aria-label="Search activity"
                type="search"
                placeholder="Search commands or conversations"
                value={search}
                onChange={(e) => setSearch(e.target.value)}
              />
            </label>
          </div>
        </>
      )}
      {shown.length === 0 ? (
        <div className="empty">
          <Icon name={filter === "review" ? "check" : "activity"} size={28} />
          <strong>
            {jobs.length
              ? "Nothing matches this view"
              : "Your request journal starts here"}
          </strong>
          <p>
            {filter === "review"
              ? "You’re all caught up."
              : "New command requests appear here with their progress and outcome."}
          </p>
          {(search || filter !== "all") && (
            <button
              className="secondary"
              onClick={() => {
                setSearch("");
                setFilter("all");
              }}
            >
              Clear activity filters
            </button>
          )}
        </div>
      ) : (
        <div className="journal">
          {shown.map((j) => (
            <details key={j.id}>
              <summary>
                <span className={"event-icon " + j.state}>
                  <Icon
                    name={
                      j.state === "awaiting_approval"
                        ? "clock"
                        : j.state === "unknown_submission"
                          ? "warning"
                          : completed.includes(j.state)
                            ? "check"
                            : "commands"
                    }
                  />
                </span>
                <span className="event-title">
                  <code>!{j.command.replace(/^!/, "")}</code>
                  <span>{label(j.conversation_id)}</span>
                </span>
                <time dateTime={j.created_at}>
                  {new Date(j.created_at).toLocaleTimeString([], {
                    hour: "2-digit",
                    minute: "2-digit",
                  })}
                </time>
                <span className={"status " + j.state}>
                  {j.state.replaceAll("_", " ")}
                </span>
              </summary>
              <div className="event-detail">
                <dl>
                  <dt>Request</dt>
                  <dd>{j.id}</dd>
                  <dt>Conversation</dt>
                  <dd>{label(j.conversation_id)}</dd>
                  <dt>Created</dt>
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
                          <dt>Requested reasoning</dt>
                          <dd>{j.requested_reasoning.replaceAll("_", " ")}</dd>
                        </>
                      )}
                      <dt>Usage</dt>
                      <dd>
                        {(
                          (j.prompt_tokens ?? 0) + (j.completion_tokens ?? 0)
                        ).toLocaleString()}{" "}
                        tokens · {(j.reasoning_tokens ?? 0).toLocaleString()}{" "}
                        reasoning tokens ·{" "}
                        {j.cost === undefined
                          ? "Cost unavailable"
                          : "$" + j.cost.toFixed(6)}
                      </dd>
                    </>
                  )}
                </dl>
                {j.state === "unknown_submission" && (
                  <p className="muted">
                    The submission outcome is uncertain. This request will not
                    be retried automatically.
                  </p>
                )}
                {act && (
                  <div className="actions">
                    {j.state === "awaiting_approval" && (
                      <button
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
                        <Icon name="check" />
                        Approve request
                      </button>
                    )}
                    {pending.includes(j.state) && (
                      <button
                        className="secondary"
                        disabled={busy}
                        onClick={() =>
                          void act(() =>
                            api(
                              `/jobs/${encodeURIComponent(j.id)}/cancel`,
                              "POST",
                            ),
                          )
                        }
                      >
                        Cancel request
                      </button>
                    )}
                  </div>
                )}
              </div>
            </details>
          ))}
        </div>
      )}
    </>
  );
}
