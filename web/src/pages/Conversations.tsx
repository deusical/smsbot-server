import { useEffect, useRef, useState } from "react";
import {
  api,
  inboxAllows,
  type Conversation,
  type InboxSettings,
  type State,
} from "../lib/api";
import { ConversationHistory } from "./ConversationHistory";
import { formatListTime, sortConversations, useSeen } from "../lib/conversationOrder";
import { useTyping } from "../lib/typing";
import { Icon } from "../components/Icon";
import { Avatar, Segmented, Switch } from "../components/ui";

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
  const searchBox = useRef<HTMLInputElement>(null);
  const { markSeen, isUnread } = useSeen();
  const typing = useTyping(!!state?.capabilities.includes("typing") && !!inbox?.enabled);
  const visible = sortConversations(conversations).filter(
    (c) =>
      (filter === "all" ||
        (filter === "enrolled" && c.enrolled) ||
        (filter === "available" && !c.enrolled && !c.group && c.writable)) &&
      `${c.label} ${c.protocol}`.toLowerCase().includes(search.toLowerCase()),
  );
  const current = conversations.find((c) => c.id === selected);
  const open = (c: Conversation) => c.archived || c.enrolled || inboxAllows(inbox, c);
  // An open conversation counts as read, including messages that arrive while it is open.
  useEffect(() => markSeen(current), [current?.id, current?.last_message_at]);
  // Keyboard shortcuts: Ctrl/Cmd+K focuses search; Alt+Up/Down switches conversation.
  const shortcut = useRef<(event: KeyboardEvent) => void>(() => {});
  shortcut.current = (event) => {
    if (event.isComposing) return;
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "k") {
      event.preventDefault();
      searchBox.current?.focus();
      searchBox.current?.select();
    } else if (
      event.altKey &&
      !event.ctrlKey &&
      !event.metaKey &&
      (event.key === "ArrowUp" || event.key === "ArrowDown")
    ) {
      // On macOS, Option+Up/Down moves the caret inside text fields; leave it alone there.
      const target = event.target as HTMLElement | null;
      const editing =
        !!target &&
        (target.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(target.tagName));
      if (editing && /Mac|iPhone|iPad/.test(navigator.platform)) return;
      const choices = visible.filter(open);
      if (!choices.length) return;
      event.preventDefault();
      const at = choices.findIndex((c) => c.id === selected);
      const step = event.key === "ArrowDown" ? 1 : -1;
      const next = at < 0 ? (step > 0 ? 0 : choices.length - 1) : at + step;
      if (choices[next]) onSelect(choices[next].id);
    }
  };
  useEffect(() => {
    const handler = (event: KeyboardEvent) => shortcut.current(event);
    window.addEventListener("keydown", handler);
    return () => window.removeEventListener("keydown", handler);
  }, []);
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
              ref={searchBox}
              aria-label="Search conversations"
              aria-keyshortcuts="Control+K Meta+K"
              type="search"
              placeholder="Search (Ctrl+K)"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Escape") {
                  setSearch("");
                  e.currentTarget.blur();
                }
              }}
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
                (c.enrolled ? " enrolled" : "") +
                (isUnread(c) ? " unread" : "")
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
                      ? "Archive this group in Settings to open it"
                      : "Turn on bot replies to open this conversation"
                }
                onClick={() => onSelect(c.id)}
              >
                <Avatar label={c.label} />
                <span className="conversation-text">
                  <strong>
                    {c.label}
                    {isUnread(c) && <span className="sr-only"> (unread)</span>}
                    {c.pinned && <span className="sr-only"> (pinned)</span>}
                  </strong>
                  <small>
                    {typing.has(c.id) ? (
                      <span className="typing-text">typing…</span>
                    ) : c.preview ? (
                      <span className="preview-text">
                        {c.preview_from_me ? "You: " : ""}
                        {c.preview}
                      </span>
                    ) : (
                      <>
                        {c.group ? `Group, ${c.members?.length ?? 0} people` : c.protocol}
                        {c.enrolled && <span className="on-tag">Bot on</span>}
                      </>
                    )}
                  </small>
                </span>
                <span className="conversation-meta">
                  {c.last_message_at && (
                    <time dateTime={c.last_message_at}>
                      {formatListTime(c.last_message_at)}
                    </time>
                  )}
                  <span className="conversation-flags" aria-hidden="true">
                    {c.pinned && <Icon name="pin" size={13} />}
                    {isUnread(c) && <span className="unread-dot" />}
                  </span>
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
            typing={typing.has(current.id)}
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
                : "Turn on bot replies for a person to let them use your commands. Bot replies don't work in groups yet."}
            </p>
          </div>
        )}
      </section>
    </div>
  );
}
