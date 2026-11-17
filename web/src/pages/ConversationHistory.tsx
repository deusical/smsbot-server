import { useVirtualizer } from "@tanstack/react-virtual";
import {
  memo,
  useCallback,
  useEffect,
  useImperativeHandle,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
  type RefObject,
} from "react";
import { MessageActions } from "../components/MessageActions";
import { Composer } from "../components/Composer";
import { Icon } from "../components/Icon";
import { LoadingState } from "../components/Loading";
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
import { removePending, usePending, useSubmittedSignal, type Pending } from "../lib/pendingSends";
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

let dayStartMs = 0;
let dayEndMs = 0;
let currentYear = 0;
let currentTzOffset = 0;
const dayCache = new Map<string, string>();
function dayLabel(iso: string) {
  const now = Date.now();
  const tz = new Date(now).getTimezoneOffset();
  if (now < dayStartMs || now >= dayEndMs || tz !== currentTzOffset) {
    const today = new Date(now);
    currentYear = today.getFullYear();
    currentTzOffset = tz;
    dayStartMs = new Date(currentYear, today.getMonth(), today.getDate()).getTime();
    dayEndMs = new Date(currentYear, today.getMonth(), today.getDate() + 1).getTime();
    dayCache.clear();
  } else if (dayCache.size > 2000) {
    dayCache.clear();
  }
  const hit = dayCache.get(iso);
  if (hit !== undefined) return hit;
  const d = new Date(iso);
  const msgDayStart = new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
  const days = Math.round((dayStartMs - msgDayStart) / 86400000);
  const label =
    days === 0
      ? "Today"
      : days === 1
        ? "Yesterday"
        : d.toLocaleDateString([], {
            weekday: "short",
            month: "short",
            day: "numeric",
            year: d.getFullYear() === currentYear ? undefined : "numeric",
          });
  dayCache.set(iso, label);
  return label;
}
// Consecutive messages from the same sender read as one run: tighter spacing,
// with the small inner corner continuing down the run.
/** The people in a group, by name, for the header. */
const memberNames = (c: Conversation) => (c.members ?? []).map((p) => p.name || "Someone").join(", ");
/** Who sent an incoming message, for conversations with several people. */
const senderName = (c: Conversation, m: Message) =>
  c.group && m.direction === "incoming"
    ? (c.members?.find((p) => p.id === m.sender)?.name || "Someone")
    : undefined;
const joinsSameDay = (a: Message | undefined, b: Message) =>
  !!a &&
  a.direction === b.direction &&
  (a.origin ?? "") === (b.origin ?? "") &&
  (a.sender ?? "") === (b.sender ?? "") &&
  Math.abs(Date.parse(b.at) - Date.parse(a.at)) < 10 * 60000;

type ComposerBridge = {
  reply: (target: Message) => void;
  retry: (p: Pending) => void;
  edit: (p: Pending) => void;
};

/** Owns composer draft state so typing does not re-render the message list above it. */
function ThreadComposer({
  conversation,
  roster,
  inbox,
  canSend,
  canAttach,
  blocked,
  busy,
  fullInbox,
  textarea,
  bridgeRef,
  onResume,
  footer,
}: {
  conversation: Conversation;
  roster?: string;
  inbox?: InboxSettings | null;
  canSend: boolean;
  canAttach: boolean;
  blocked: string;
  busy: boolean;
  fullInbox: boolean;
  textarea: RefObject<HTMLTextAreaElement | null>;
  bridgeRef: RefObject<ComposerBridge | null>;
  onResume?: () => void;
  footer?: ReactNode;
}) {
  const composer = useComposer({
    conversation,
    roster,
    inbox,
    canSend,
    textarea,
    readInbox: async () => (fullInbox ? await api<InboxSettings>("/inbox/settings") : inbox),
  });
  useImperativeHandle(bridgeRef, () => ({
    reply: (target) => {
      composer.setReplyTarget(target);
      textarea.current?.focus();
    },
    retry: composer.retryPending,
    edit: composer.editPending,
  }));
  return (
    <Composer
      composer={composer}
      textarea={textarea}
      label={conversation.label}
      blocked={blocked}
      canSend={canSend}
      canAttach={canAttach}
      busy={busy}
      onResume={onResume}
      footer={footer}
    />
  );
}

const MessageRow = memo(function MessageRow({
  message,
  virtualIndex,
  virtualStart,
  measureElement,
  dividerDay,
  joinedPrev,
  senderLabel,
  highlighted,
  label,
  base,
  fileLimitMB,
  showActions,
  conversationId,
  writable,
  roster,
  eligibilityKey,
  onJump,
  onReply,
  onChanged,
}: {
  message: Message;
  virtualIndex?: number;
  virtualStart: number | null;
  measureElement?: (node: Element | null) => void;
  dividerDay: string;
  joinedPrev: boolean;
  senderLabel?: string;
  highlighted: boolean;
  label: string;
  base: string;
  fileLimitMB: number;
  showActions: boolean;
  conversationId: string;
  writable: boolean;
  roster?: string;
  eligibilityKey: string;
  onJump: (id: string) => void;
  onReply: (target: Message) => void;
  onChanged: () => void;
}) {
  const actions = useMemo(
    () =>
      showActions ? (
        <MessageActions
          message={message}
          conversation={conversationId}
          label={label}
          writable={writable}
          roster={roster}
          eligibilityKey={eligibilityKey}
          onReply={onReply}
          onChanged={onChanged}
        />
      ) : null,
    [showActions, message, conversationId, label, writable, roster, eligibilityKey, onReply, onChanged],
  );
  return (
    <div
      data-index={virtualIndex}
      ref={measureElement}
      style={
        virtualStart !== null
          ? {
              position: "absolute",
              top: 0,
              left: 0,
              width: "100%",
              transform: `translateY(${virtualStart}px)`,
            }
          : undefined
      }
      className="message-group"
    >
      {dividerDay && (
        <div className="day" role="separator">
          <span>{dividerDay}</span>
        </div>
      )}
      <MessageBubble
        message={message}
        label={label}
        base={base}
        fileLimitMB={fileLimitMB}
        joinedPrev={joinedPrev}
        senderName={senderLabel}
        highlighted={highlighted}
        onJump={onJump}
        actions={actions}
      />
    </div>
  );
});

/**
 * One conversation: composes message synchronization, the scroll owner, read
 * receipts and the composer. Scrolling, virtualization and the visible range
 * stay together here because they depend on the same layout.
 */
export const ConversationHistory = memo(function ConversationHistory({ conversation, state, busy, act, inbox, enrollControl, typing = false, onBack, onResume }: Props) {
  const base = `${inbox?.enabled || conversation.archived ? "/inbox" : ""}/conversations/${encodeURIComponent(conversation.id)}`;
  const scroll = useRef<HTMLDivElement>(null),
    rowsContainer = useRef<HTMLDivElement>(null),
    textarea = useRef<HTMLTextAreaElement>(null),
    composerBridge = useRef<ComposerBridge | null>(null);
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
  // For a group: who the owner has seen in it. When the people change, the
  // change has to be looked at before the next send.
  const [seenRoster, setSeenRoster] = useState(conversation.roster ?? "");
  useEffect(() => setSeenRoster(conversation.roster ?? ""), [conversation.id]);
  const groupChanged = conversation.group && (conversation.roster ?? "") !== seenRoster;
  const blocked = !state
    ? "Connecting…"
    : state.paused
      ? "paused"
      : !state.transport_ready
        ? "Your phone isn’t connected, so messages can’t be sent right now."
        : groupChanged
          ? "The people in this group changed. Check who is in it before sending."
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
  const virtualized = messages.length > 300;
  const virtualizer = useVirtualizer({
    count: messages.length,
    getScrollElement: () => scroll.current,
    estimateSize: () => 110,
    overscan: 12,
    getItemKey: (index) => messages[index].id,
    enabled: virtualized,
  });
  const virtualizerRef = useRef(virtualizer);
  virtualizerRef.current = virtualizer;

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
    const rows = messagesRef.current;
    let first = Math.max(0, rows.length - 50);
    let last = rows.length - 1;
    if (rows.length > 300) {
      const r = virtualizerRef.current.range;
      if (r) {
        first = Math.max(0, r.startIndex - 5);
        last = Math.min(rows.length - 1, r.endIndex + 5);
      }
    } else if (rowsContainer.current && rowsContainer.current.children.length > 0) {
      const bounds = el.getBoundingClientRect();
      const children = rowsContainer.current.children;
      // Binary search sorted message nodes instead of measuring every row on scroll.
      let lo = 0,
        hi = children.length - 1,
        firstHit = -1;
      while (lo <= hi) {
        const mid = (lo + hi) >> 1;
        if (children[mid].getBoundingClientRect().bottom >= bounds.top) {
          firstHit = mid;
          hi = mid - 1;
        } else {
          lo = mid + 1;
        }
      }
      if (firstHit >= 0) {
        lo = firstHit;
        hi = children.length - 1;
        let lastHit = firstHit;
        while (lo <= hi) {
          const mid = (lo + hi) >> 1;
          if (children[mid].getBoundingClientRect().top <= bounds.bottom) {
            lastHit = mid;
            lo = mid + 1;
          } else {
            hi = mid - 1;
          }
        }
        first = Math.max(0, firstHit - 5);
        last = Math.min(rows.length - 1, lastHit + 5);
      }
    }
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
  const savedIds = useMemo(() => new Set(messages.map((m) => m.id)), [messages]);
  const visibleRows = virtualized
    ? virtualizer.getVirtualItems().map((row) => ({ m: messages[row.index], index: row.index, start: row.start }))
    : messages.map((m, index) => ({ m, index, start: null as number | null }));

  const queryRef = useRef(query);
  queryRef.current = query;
  const threadRef = useRef(thread);
  threadRef.current = thread;
  const viewportRef = useRef(viewport);
  viewportRef.current = viewport;

  const handleJump = useCallback((id: string) => {
    const currentThread = threadRef.current;
    if (queryRef.current) {
      currentThread.setError("Clear search to jump to the original message.");
      return;
    }
    const generation = currentThread.generation.current;
    void currentThread
      .windowAround(id)
      .then((next) => {
        if (queryRef.current) {
          currentThread.setError("Clear search to jump to the original message.");
          return;
        }
        if (!next) return;
        const index = next.findIndex((m) => m.id === id);
        if (index < 0) throw new Error("unavailable");
        setHighlighted(id);
        viewportRef.current.setAway(true);
        if (next.length > 300) virtualizerRef.current.scrollToIndex(index, { align: "center" });
        requestAnimationFrame(() =>
          requestAnimationFrame(() =>
            scroll.current?.querySelector<HTMLElement>(`[data-message-id="${CSS.escape(id)}"]`)?.scrollIntoView({ block: "center", behavior: "auto" }),
          ),
        );
      })
      .catch(() => {
        if (generation === currentThread.generation.current) currentThread.setError("The original message is unavailable.");
      });
  }, []);

  const handleReply = useCallback((target: Message) => {
    composerBridge.current?.reply(target);
  }, []);

  const handleRetryPending = useCallback((p: Pending) => {
    composerBridge.current?.retry(p);
  }, []);

  const handleEditPending = useCallback((p: Pending) => {
    composerBridge.current?.edit(p);
  }, []);

  const handleDismissPending = useCallback(
    (key: string) => {
      removePending(conversation.id, key);
    },
    [conversation.id],
  );

  const handleChanged = useCallback(() => {
    threadRef.current.refreshNow();
  }, []);

  const showActions = !!state?.capabilities.includes("message_actions");
  const actionsWritable = inboxAllows(inbox, conversation) && !archiveReadOnly && !groupChanged;
  const actionsRoster = conversation.group ? seenRoster : undefined;
  const eligibilityKey = useMemo(
    () =>
      JSON.stringify([
        conversation.participant_id,
        conversation.protocol,
        state?.paused,
        state?.transport_ready,
        inbox?.revision,
        conversation.group ? seenRoster : "",
      ]),
    [
      conversation.participant_id,
      conversation.protocol,
      conversation.group,
      state?.paused,
      state?.transport_ready,
      inbox?.revision,
      seenRoster,
    ],
  );

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
                {conversation.group
                  ? `Group, ${conversation.members?.length ?? 0} people${memberNames(conversation) ? `: ${memberNames(conversation)}` : ""}`
                  : conversation.protocol}
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
      {groupChanged && (
        <div className="group-changed" role="alert">
          <span>The people in this group changed. It now has {memberNames(conversation) || "no one else"}.</span>
          <button type="button" onClick={() => setSeenRoster(conversation.roster ?? "")}>
            Send to these people
          </button>
        </div>
      )}
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
        {messages.length === 0 && loading && <LoadingState what="messages" rows={4} />}
        {messages.length === 0 && !loading && (
          <div className="thread-empty">
            <strong>{query ? `No messages match “${query}”.` : "No messages here yet."}</strong>
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
              <small>
                New texts appear as they arrive
                {!inbox?.enabled && state?.capabilities.includes("history_import") ? ". Use the history button to import older ones." : "."}
              </small>
            )}
          </div>
        )}
        <div
          ref={rowsContainer}
          style={virtualized ? { height: virtualizer.getTotalSize(), flexShrink: 0, position: "relative" } : { display: "contents" }}
        >
          {visibleRows.map(({ m, index, start }) => {
            const day = dayLabel(m.at);
            const dividerDay = index === 0 || day !== dayLabel(messages[index - 1].at) ? day : "";
            return (
              <MessageRow
                key={m.id}
                message={m}
                virtualIndex={start !== null ? index : undefined}
                virtualStart={start}
                measureElement={start !== null ? virtualizer.measureElement : undefined}
                dividerDay={dividerDay}
                joinedPrev={!dividerDay && joinsSameDay(messages[index - 1], m)}
                senderLabel={senderName(conversation, m)}
                highlighted={highlighted === m.id}
                label={conversation.label}
                base={base}
                fileLimitMB={fileLimits.max_file_mb}
                showActions={showActions}
                conversationId={conversation.id}
                writable={actionsWritable}
                roster={actionsRoster}
                eligibilityKey={eligibilityKey}
                onJump={handleJump}
                onReply={handleReply}
                onChanged={handleChanged}
              />
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
              onRetry={handleRetryPending}
              onEdit={handleEditPending}
              onDismiss={handleDismissPending}
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
      <ThreadComposer
        conversation={conversation}
        roster={seenRoster}
        inbox={inbox}
        canSend={canSend}
        canAttach={canAttach}
        blocked={blocked}
        busy={busy}
        fullInbox={!!state?.capabilities.includes("full_inbox")}
        textarea={textarea}
        bridgeRef={composerBridge}
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
});
