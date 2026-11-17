import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  api,
  inboxAllows,
  type Conversation,
  type InboxSettings,
  type State,
} from "../lib/api";
import { GroupBot } from "../components/GroupBot";
import { ConversationHistory } from "./ConversationHistory";
import { formatListTime, sortConversations, useSeen } from "../lib/conversationOrder";
import { useTyping } from "../lib/typing";
import { Icon } from "../components/Icon";
import { LoadingState } from "../components/Loading";
import { Avatar, Segmented, Switch } from "../components/ui";

type Filter = "all" | "enrolled" | "available";
export function ConversationsPage({
  conversations,
  inbox,
  state,
  busy,
  act,
  onPatchConversation,
  onRefresh,
  onError,
  selected,
  onSelect,
  partial,
  canLoadMore,
  onLoadMore,
  onResume,
  loaded = true,
  loadError = "",
}: {
  /** False until the first read of the conversation list has finished. */
  loaded?: boolean;
  loadError?: string;
  conversations: Conversation[];
  inbox: InboxSettings | null;
  state: State | null;
  busy: boolean;
  act: (fn: () => Promise<unknown>, refreshAfter?: boolean) => Promise<void>;
  onPatchConversation: (id: string, patch: Partial<Conversation>) => void;
  onRefresh: () => void;
  onError: (e: unknown) => void;
  selected: string;
  onSelect: (id: string) => void;
  partial: boolean;
  canLoadMore: boolean;
  onLoadMore: () => void;
  onResume: () => void;
}) {
  const [search, setSearch] = useState(""),
    [filter, setFilter] = useState<Filter>("all"),
    [toggling, setToggling] = useState<Record<string, boolean>>({});
  const togglingRef = useRef<Set<string>>(new Set());
  const searchBox = useRef<HTMLInputElement>(null);
  const { markSeen, isUnread } = useSeen();
  const typing = useTyping(!!state?.capabilities.includes("typing") && !!inbox?.enabled);
  const sorted = useMemo(() => sortConversations(conversations), [conversations]);
  const enrolledCount = useMemo(
    () => conversations.reduce((n, c) => n + (c.enrolled ? 1 : 0), 0),
    [conversations],
  );
  const visible = useMemo(() => {
    const q = search.toLowerCase();
    return sorted.filter(
      (c) =>
        (filter === "all" ||
          (filter === "enrolled" && c.enrolled) ||
          (filter === "available" && !c.enrolled && !c.group && c.writable)) &&
        (!q || `${c.label} ${c.protocol}`.toLowerCase().includes(q)),
    );
  }, [sorted, filter, search]);
  const current = useMemo(
    () => conversations.find((c) => c.id === selected),
    [conversations, selected],
  );
  const handleBack = useCallback(() => onSelect(""), [onSelect]);
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
  const toggle = useCallback(
    async (c: Conversation) => {
      if (togglingRef.current.has(c.id)) return;
      togglingRef.current.add(c.id);
      const next = !c.enrolled;
      setToggling((prev) => ({ ...prev, [c.id]: true }));
      try {
        if (next) {
          await api("/enrollment", "POST", {
            conversation_id: c.id,
            participant_id: c.participant_id,
          });
        } else {
          await api(`/enrollment/${encodeURIComponent(c.id)}`, "DELETE");
        }
        onPatchConversation(c.id, { enrolled: next });
      } catch (e) {
        onError(e);
        onRefresh();
      } finally {
        togglingRef.current.delete(c.id);
        setToggling((prev) => {
          const copy = { ...prev };
          delete copy[c.id];
          return copy;
        });
      }
    },
    [onPatchConversation, onError, onRefresh],
  );
  const enrollSwitch = (c: Conversation, compact: boolean) => (
    <Switch
      label={compact ? `Bot replies for ${c.label}` : "Bot replies"}
      hideLabel={compact}
      checked={c.enrolled}
      disabled={!!toggling[c.id] || c.group || !c.writable}
      onChange={() => void toggle(c)}
    />
  );
  const currentToggling = current ? !!toggling[current.id] : false;
  const threadEnrollControl = useMemo(() => {
    if (!current) return undefined;
    if (current.group) {
      return (
        <GroupBot
          conversation={current}
          onPatchConversation={onPatchConversation}
          onRefresh={onRefresh}
          onError={onError}
        />
      );
    }
    return (
      <Switch
        label="Bot replies"
        checked={current.enrolled}
        disabled={currentToggling || current.group || !current.writable}
        onChange={() => void toggle(current)}
      />
    );
  }, [current, currentToggling, onPatchConversation, onRefresh, onError, toggle]);
  const threadInbox = useMemo(
    () => (current && inboxAllows(inbox, current) ? inbox : undefined),
    [current, inbox],
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
                count: enrolledCount,
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
        {!visible.length && !loaded && <LoadingState what="conversations" error={loadError} rows={6} />}
        {!visible.length && loaded && (
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
            inbox={threadInbox}
            state={state}
            busy={busy}
            act={act}
            enrollControl={threadEnrollControl}
            typing={typing.has(current.id)}
            onBack={handleBack}
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
                : "Turn on bot replies for a person to let them use your commands. In a group, choose who can use the bot from inside the group."}
            </p>
          </div>
        )}
      </section>
    </div>
  );
}
