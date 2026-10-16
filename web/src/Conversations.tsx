import { useState } from "react";
import {
  api,
  inboxAllows,
  type Conversation,
  type InboxSettings,
  type State,
} from "./api";
import { ConversationHistory } from "./ConversationHistory";
import { Icon } from "./Icon";
import { Avatar, Segmented, Switch } from "./ui";

type Filter = "all" | "enrolled" | "available";
export function ConversationsPage({
  conversations,
  inbox,
  state,
  busy,
  act,
  selected,
  onSelect,
  partial,
  canLoadMore,
  onLoadMore,
  onResume,
}: {
  conversations: Conversation[];
  inbox: InboxSettings | null;
  state: State | null;
  busy: boolean;
  act: (fn: () => Promise<unknown>, refreshAfter?: boolean) => Promise<void>;
  selected: string;
  onSelect: (id: string) => void;
  partial: boolean;
  canLoadMore: boolean;
  onLoadMore: () => void;
  onResume: () => void;
}) {
  const [search, setSearch] = useState(""),
    [filter, setFilter] = useState<Filter>("all");
  const visible = conversations.filter(
    (c) =>
      (filter === "all" ||
        (filter === "enrolled" && c.enrolled) ||
        (filter === "available" && !c.enrolled && !c.group && c.writable)) &&
      `${c.label} ${c.protocol}`.toLowerCase().includes(search.toLowerCase()),
  );
  const current = conversations.find((c) => c.id === selected);
  const open = (c: Conversation) => c.archived || c.enrolled || inboxAllows(inbox, c);
  const toggle = (c: Conversation) =>
    act(() =>
      c.enrolled
        ? api(`/enrollment/${encodeURIComponent(c.id)}`, "DELETE")
        : api("/enrollment", "POST", {
            conversation_id: c.id,
            participant_id: c.participant_id,
          }),
    );
  const enrollSwitch = (c: Conversation, compact: boolean) => (
    <Switch
      label={compact ? `Bot replies for ${c.label}` : "Bot replies"}
      hideLabel={compact}
      checked={c.enrolled}
      disabled={busy || c.group || !c.writable}
      onChange={() => void toggle(c)}
    />
  );
  return (
    <div className={"messenger" + (current ? " has-thread" : "")}>
      <section className="thread-list" aria-label="Conversations">
        <div className="list-head">
          <label className="search">
            <Icon name="search" size={16} />
            <input
              aria-label="Search conversations"
              type="search"
              placeholder="Search"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
            />
          </label>
          <Segmented
            label="Filter conversations"
            value={filter}
            onChange={setFilter}
            options={[
              { value: "all", label: "All" },
              {
                value: "enrolled",
                label: "Bot on",
                count: conversations.filter((c) => c.enrolled).length,
              },
              { value: "available", label: "Bot off" },
            ]}
          />
        </div>
        <ul className="conversation-list">
          {visible.map((c) => (
            <li
              key={c.id}
              className={
                "conversation" +
                (selected === c.id ? " selected" : "") +
                (c.enrolled ? " enrolled" : "")
              }
            >
              <button
                type="button"
                className="thread-button"
                disabled={!open(c)}
                title={
                  open(c)
                    ? undefined
                    : c.group
                      ? "Group conversations are never used by the bot"
                      : "Turn on bot replies to open this conversation"
                }
                onClick={() => onSelect(c.id)}
              >
                <Avatar label={c.label} />
                <span className="conversation-text">
                  <strong>{c.label}</strong>
                  <small>
                    {c.group ? (
                      "Group, not supported"
                    ) : (
                      <>
                        {c.protocol}
                        {c.enrolled && <span className="on-tag">Bot on</span>}
                      </>
                    )}
                  </small>
                </span>
              </button>
              {enrollSwitch(c, true)}
            </li>
          ))}
        </ul>
        {!visible.length && (
          <div className="empty small">
            <strong>
              {conversations.length
                ? "No conversations found"
                : "No conversations yet"}
            </strong>
            <p>
              {conversations.length
                ? "Try another name or filter."
                : "They appear once your phone is connected."}
            </p>
          </div>
        )}
        {partial && (
          <p role="status" className="inline-note">
            Some conversations didn’t load. They’ll retry automatically.
          </p>
        )}
        {canLoadMore && (
          <button
            type="button"
            className="ghost load-more"
            disabled={busy}
            onClick={onLoadMore}
          >
            Load more conversations
          </button>
        )}
      </section>
      <section className="thread-pane" aria-label="Messages">
        {current && open(current) ? (
          <ConversationHistory
            key={current.id}
            conversation={current}
            inbox={inboxAllows(inbox, current) ? inbox : undefined}
            state={state}
            busy={busy}
            act={act}
            enrollControl={enrollSwitch(current, false)}
            onBack={() => onSelect("")}
            onResume={onResume}
          />
        ) : (
          <div className="thread-placeholder">
            <Icon name="conversations" size={30} />
            <strong>
              {current ? "Bot replies are off here" : "Pick a conversation"}
            </strong>
            <p>
              {current
                ? "Turn bot replies back on to see this conversation."
                : "Turn on bot replies for a person to let them use your commands. Groups are never included."}
            </p>
          </div>
        )}
      </section>
    </div>
  );
}
