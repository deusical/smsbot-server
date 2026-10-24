import { useVirtualizer } from "@tanstack/react-virtual";
import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { MessageActions } from "../components/MessageActions";
import { Composer } from "../components/Composer";
import { Icon } from "../components/Icon";
import { MessageBubble, PendingBubble } from "../components/MessageBubble";
import { Avatar } from "../components/ui";
import {
  api,
  defaultAttachmentLimits,
  inboxAllows,
  type Conversation,
  type InboxSettings,
  type Message,
  type State,
} from "../lib/api";
import { removePending, usePending, useSubmittedSignal } from "../lib/pendingSends";
import { useComposer } from "../lib/useComposer";
import { useConversationMessages } from "../lib/useConversationMessages";
import { useReadReceipts } from "../lib/useReadReceipts";
import { useThreadScroll } from "../lib/useThreadScroll";
import { useViewedHistoryRefresh } from "../lib/useViewedHistoryRefresh";

type Props = {
  conversation: Conversation;
  state: State | null;
  busy: boolean;
  act: (fn: () => Promise<unknown>, refreshAfter?: boolean) => Promise<void>;
  inbox?: InboxSettings | null;
  enrollControl?: ReactNode;
  typing?: boolean;
  onBack?: () => void;
  onResume?: () => void;
};
function dayLabel(iso: string) {
  const d = new Date(iso),
    today = new Date();
  const days = Math.round(
    (new Date(today.toDateString()).getTime() - new Date(d.toDateString()).getTime()) / 86400000,
  );
  if (days === 0) return "Today";
  if (days === 1) return "Yesterday";
  return d.toLocaleDateString([], {
    weekday: "short",
    month: "short",
    day: "numeric",
    year: d.getFullYear() === today.getFullYear() ? undefined : "numeric",
  });
}
// Consecutive messages from the same sender read as one run: tighter spacing,
// with the small inner corner continuing down the run.
const joins = (a: Message | undefined, b: Message) =>
  !!a &&
  a.direction === b.direction &&
  (a.origin ?? "") === (b.origin ?? "") &&
  dayLabel(a.at) === dayLabel(b.at) &&
  Math.abs(Date.parse(b.at) - Date.parse(a.at)) < 10 * 60000;

/**
 * One conversation: composes message synchronization, the scroll owner, read
 * receipts and the composer. Scrolling, virtualization and the visible range
 * stay together here because they depend on the same layout.
 */
export function ConversationHistory({ conversation, state, busy, act, inbox, enrollControl, typing = false, onBack, onResume }: Props) {
  const base = `${inbox?.enabled || conversation.archived ? "/inbox" : ""}/conversations/${encodeURIComponent(conversation.id)}`;
  const scroll = useRef<HTMLDivElement>(null),
    textarea = useRef<HTMLTextAreaElement>(null);
  const [search, setSearch] = useState(""),
    [searchOpen, setSearchOpen] = useState(false),
    [query, setQuery] = useState(""),
    [highlighted, setHighlighted] = useState("");
  useEffect(() => setHighlighted(""), [conversation.id]);
  // Search as you type; the request generation discards older results.
  useEffect(() => {
    const timer = setTimeout(() => setQuery(search.trim()), 300);
    return () => clearTimeout(timer);
  }, [search]);

  const pending = usePending(conversation.id);
  const viewport = useThreadScroll(scroll);
  const thread = useConversationMessages({
    conversationId: conversation.id,
    base,
    query,
    changeFeed: !!state?.capabilities.includes("archive_history"),
    loadOlderFromPhone: !!inbox?.load_older,
    viewport,
  });
  const { messages, cursor, error, loading, importFinished, olderNotice } = thread;
  // The scroll owner restores position after each message update renders,
  // then follows a message the owner just sent.
  useLayoutEffect(viewport.restore, [messages]);
  useLayoutEffect(() => viewport.followSend(pending.length), [pending.length]);

  const archiveReadOnly = !conversation.enrolled && base.startsWith("/inbox") && !inbox?.enabled;
  const blocked = !state
    ? "Connecting…"
    : state.paused
      ? "paused"
      : !state.transport_ready
        ? "Your phone isn’t connected, so messages can’t be sent right now."
        : !conversation.writable
          ? "This conversation can’t receive replies."
          : archiveReadOnly
            ? "Sending is disabled for this conversation."
            : !(conversation.enrolled || inbox?.enabled)
              ? "Turn on bot replies to message this conversation."
              : "";
  const canSend = !archiveReadOnly && !blocked && !busy;
  const canAttach = !!inbox?.enabled && !!state?.capabilities.includes("attachments");
  const fileLimits = inbox?.attachment_limits ?? defaultAttachmentLimits;
  const composer = useComposer({
    conversation,
    inbox,
    canSend,
    textarea,
    readInbox: async () => (state?.capabilities.includes("full_inbox") ? await api<InboxSettings>("/inbox/settings") : inbox),
  });

  // The visible time span drives viewed-section refresh and original-file priority.
  const [viewRange, setViewRange] = useState<{ since: string; until: string } | null>(null);
  useViewedHistoryRefresh(
    conversation.id,
    !!state?.transport_ready && inboxAllows(inbox, conversation),
    !!state?.capabilities.includes("archive_refresh") && (!!inbox?.enabled || !!conversation.archived),
    viewRange ?? undefined,
  );
  const openedAt = useRef(new Date().toISOString());
  const rangeFrame = useRef(0);
  const messagesRef = useRef(messages);
  messagesRef.current = messages;
  function updateViewedRange() {
    const el = scroll.current;
    if (!el) return;
    const bounds = el.getBoundingClientRect();
    const indices = Array.from(el.querySelectorAll<HTMLElement>("[data-message-index]"))
      .filter((node) => {
        const r = node.getBoundingClientRect();
        return r.bottom >= bounds.top && r.top <= bounds.bottom;
      })
      .map((node) => Number(node.dataset.messageIndex));
    const rows = messagesRef.current;
    const first = indices.length ? Math.max(0, Math.min(...indices) - 5) : Math.max(0, rows.length - 50);
    const last = indices.length ? Math.min(rows.length - 1, Math.max(...indices) + 5) : rows.length - 1;
    const since = rows[first]?.at ?? new Date(Date.parse(openedAt.current) - 30 * 60 * 1000).toISOString();
    const until = el.scrollHeight - el.scrollTop - el.clientHeight < 80 ? "" : (rows[last]?.at ?? "");
    setViewRange((previous) => (previous?.since === since && previous.until === until ? previous : { since, until }));
  }
  function scheduleViewedRange() {
    cancelAnimationFrame(rangeFrame.current);
    rangeFrame.current = requestAnimationFrame(updateViewedRange);
  }
  useEffect(() => {
    openedAt.current = new Date().toISOString();
    setViewRange(null);
  }, [conversation.id, query, base]);
  useEffect(() => {
    scheduleViewedRange();
    return () => cancelAnimationFrame(rangeFrame.current);
  }, [messages, conversation.id, query]);

  useReadReceipts({
    conversation,
    messages,
    loading,
    away: viewport.away,
    enabled:
      !!state?.capabilities.includes("read_receipts") &&
      !!inbox?.enabled &&
      inbox.mark_read_when_viewed !== false &&
      inboxAllows(inbox, conversation),
    generation: thread.generation,
  });

  // A send reaching the phone refreshes the open thread at once (see usePendingFollower).
  const submittedSignal = useSubmittedSignal();
  const seenSignal = useRef(submittedSignal);
  useEffect(() => {
    if (submittedSignal === seenSignal.current) return;
    seenSignal.current = submittedSignal;
    thread.refreshNow();
  }, [submittedSignal]);
  // Drop a sent bubble as soon as the saved message with its transaction appears.
  useEffect(() => {
    for (const p of pending)
      if (p.phase === "sent" && p.transaction && messages.some((m) => m.id === p.transaction))
        removePending(conversation.id, p.key);
  }, [messages, pending, conversation.id]);
  const savedIds = new Set(messages.map((m) => m.id));

  const virtualized = messages.length > 300;
  const virtualizer = useVirtualizer({
    count: messages.length,
    getScrollElement: () => scroll.current,
    estimateSize: () => 110,
    overscan: 12,
    getItemKey: (index) => messages[index].id,
    enabled: virtualized,
  });
  const visibleRows = virtualized
    ? virtualizer.getVirtualItems().map((row) => ({ m: messages[row.index], row }))
    : messages.map((m) => ({ m, row: null }));

  async function jumpToMessage(id: string) {
    const generation = thread.generation.current;
    try {
      if (query) {
        thread.setError("Clear search to jump to the original message.");
        return;
      }
      const next = await thread.windowAround(id);
      if (!next) return;
      const index = next.findIndex((m) => m.id === id);
      if (index < 0) throw new Error("unavailable");
      setHighlighted(id);
      viewport.setAway(true);
      if (next.length > 300) virtualizer.scrollToIndex(index, { align: "center" });
      requestAnimationFrame(() =>
        requestAnimationFrame(() =>
          scroll.current?.querySelector<HTMLElement>(`[data-message-id="${CSS.escape(id)}"]`)?.scrollIntoView({ block: "center", behavior: "auto" }),
        ),
      );
    } catch {
      if (generation === thread.generation.current) thread.setError("The original message is unavailable.");
    }
  }

  const showOlder = inbox?.enabled || conversation.archived ? !!cursor || (!!inbox?.load_older && !importFinished) : !!cursor;
  return (
    <div className="thread">
      <header className="thread-head">
        {onBack && (
          <button type="button" className="icon ghost only-mobile" aria-label="Back to conversations" onClick={onBack}>
            <Icon name="back" />
          </button>
        )}
        <Avatar label={conversation.label} />
        <div className="thread-title">
          <h2>{conversation.label}</h2>
          <small>
            {typing ? (
              <span className="typing-text" role="status">typing…</span>
            ) : (
              <>
                {conversation.protocol}
                {conversation.enrolled ? ", bot replies on" : ""}
              </>
            )}
          </small>
        </div>
        <div className="thread-tools">
          <button
            type="button"
            className={"icon ghost" + (searchOpen ? " on" : "")}
            aria-label="Search this conversation"
            aria-expanded={searchOpen}
            onClick={() => {
              if (searchOpen) setSearch("");
              setSearchOpen(!searchOpen);
            }}
          >
            <Icon name="search" size={18} />
          </button>
          {enrollControl}
        </div>
      </header>
      {searchOpen && (
        <label className="search thread-search">
          <Icon name="search" size={16} />
          <input
            autoFocus
            type="search"
            aria-label="Search messages"
            placeholder="Search messages"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape") {
                setSearch("");
                setSearchOpen(false);
              }
            }}
          />
        </label>
      )}
      {error && (
        <p role="alert" className="inline-alert">
          {error}
        </p>
      )}
      {showOlder && (
        <button type="button" className="older" disabled={loading || (!!inbox?.enabled && busy)} onClick={() => void thread.loadOlder()}>
          Load older messages
        </button>
      )}
      <div
        className="messages"
        ref={scroll}
        onScroll={() => {
          scheduleViewedRange();
          if (viewport.onScroll() && (inbox?.enabled || conversation.archived)) void thread.loadOlder();
        }}
      >
        {inbox?.enabled && !cursor && importFinished && (
          <p className="history-end">That’s everything your phone returned. Older messages may still exist.</p>
        )}
        {inbox?.enabled && olderNotice && (
          <p role="status" className="sr-only">
            {loading ? "Loading messages…" : olderNotice}
          </p>
        )}
        {messages.length === 0 && (
          <div className="thread-empty">
            <strong>{loading ? "Loading messages…" : query ? `No messages match “${query}”.` : "No messages here yet."}</strong>
            {query ? (
              <button
                type="button"
                className="ghost small"
                onClick={() => {
                  setSearch("");
                  setQuery("");
                }}
              >
                Clear search
              </button>
            ) : (
              !loading && (
                <small>
                  New texts appear as they arrive
                  {!inbox?.enabled && state?.capabilities.includes("history_import") ? ". Use the history button to import older ones." : "."}
                </small>
              )
            )}
          </div>
        )}
        <div style={virtualized ? { height: virtualizer.getTotalSize(), flexShrink: 0, position: "relative" } : { display: "contents" }}>
          {visibleRows.map(({ m, row }) => {
            const day = dayLabel(m.at);
            const index = row?.index ?? messages.indexOf(m);
            const divider = index === 0 || day !== dayLabel(messages[index - 1].at);
            return (
              <div
                key={m.id}
                data-message-index={index}
                data-index={row?.index}
                ref={row ? virtualizer.measureElement : undefined}
                style={row ? { position: "absolute", top: 0, left: 0, width: "100%", transform: `translateY(${row.start}px)` } : undefined}
                className="message-group"
              >
                {divider && (
                  <div className="day" role="separator">
                    <span>{day}</span>
                  </div>
                )}
                <MessageBubble
                  message={m}
                  label={conversation.label}
                  base={base}
                  fileLimitMB={fileLimits.max_file_mb}
                  joinedPrev={joins(messages[index - 1], m)}
                  highlighted={highlighted === m.id}
                  onJump={(id) => void jumpToMessage(id)}
                  actions={
                    state?.capabilities.includes("message_actions") && (
                      <MessageActions
                        message={m}
                        conversation={conversation.id}
                        label={conversation.label}
                        writable={inboxAllows(inbox, conversation) && !archiveReadOnly}
                        eligibilityKey={JSON.stringify([conversation.participant_id, conversation.protocol, state?.paused, state?.transport_ready, inbox?.revision])}
                        onReply={(target) => {
                          composer.setReplyTarget(target);
                          textarea.current?.focus();
                        }}
                        onChanged={thread.refreshNow}
                      />
                    )
                  }
                />
              </div>
            );
          })}
        </div>
        {pending
          .filter((p) => !(p.phase === "sent" && p.transaction && savedIds.has(p.transaction)))
          .map((p) => (
            <PendingBubble
              key={p.key}
              pending={p}
              label={conversation.label}
              canSend={canSend}
              onRetry={() => composer.retryPending(p)}
              onEdit={() => composer.editPending(p)}
              onDismiss={() => removePending(conversation.id, p.key)}
            />
          ))}
        {typing && (
          <div className="message-group typing-row" aria-hidden="true">
            <div className="typing-bubble">
              <span />
              <span />
              <span />
            </div>
          </div>
        )}
      </div>
      {viewport.away && !viewport.hasNew && (
        <button
          type="button"
          className="jump-latest icon"
          aria-label="Jump to latest"
          title="Jump to latest"
          onClick={() => viewport.jumpToLatest(!window.matchMedia?.("(prefers-reduced-motion: reduce)").matches)}
        >
          <Icon name="arrowdown" size={18} />
        </button>
      )}
      {viewport.hasNew && (
        <button type="button" className="new-pill" onClick={() => viewport.jumpToLatest(false)}>
          New messages ↓
        </button>
      )}
      <Composer
        composer={composer}
        textarea={textarea}
        label={conversation.label}
        blocked={blocked}
        canSend={canSend}
        canAttach={canAttach}
        busy={busy}
        onResume={onResume}
        footer={
          state?.fixture && (
            <button
              type="button"
              className="link"
              disabled={busy || state.paused}
              onClick={() =>
                void act(async () => {
                  await api("/fixture/incoming", "POST", {
                    conversation_id: conversation.id,
                    participant_id: conversation.participant_id,
                    text: "!ping",
                  });
                  await thread.load("", true);
                })
              }
            >
              Simulate !ping
            </button>
          )
        }
      />
    </div>
  );
}
