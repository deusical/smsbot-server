import { useVirtualizer } from "@tanstack/react-virtual";
import { useViewedHistoryRefresh } from "./useViewedHistoryRefresh";
import {
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import {
  api,
  defaultAttachmentLimits,
  inboxAllows,
  ApiError,
  type Conversation,
  type History,
  type Message,
  type State,
  type InboxSettings,
} from "./api";
import { Icon } from "./Icon";
import { markSeen } from "./conversationOrder";
import { Avatar } from "./ui";
import {
  MessageAttachment,
  PendingFile,
  SelectedAttachment,
  encodeFile,
} from "./Attachments";
import {
  addPending,
  enqueue,
  getPending,
  patchPending,
  removePending,
  usePending,
  useSubmittedSignal,
  type Pending,
} from "./pendingSends";
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
const senders: Record<string, string> = {
  bot: "Bot reply",
  web: "Sent from web",
  phone: "Sent from phone",
};
function dayLabel(iso: string) {
  const d = new Date(iso),
    today = new Date();
  const days = Math.round(
    (new Date(today.toDateString()).getTime() -
      new Date(d.toDateString()).getTime()) /
      86400000,
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
export function ConversationHistory({
  conversation,
  state,
  busy,
  act,
  inbox,
  enrollControl,
  typing = false,
  onBack,
  onResume,
}: Props) {
  const base = `${(inbox?.enabled || conversation.archived) ? "/inbox" : ""}/conversations/${encodeURIComponent(conversation.id)}`;
  const scroll = useRef<HTMLDivElement>(null),
    scrollSnapshot = useRef<{
      top: number;
      height: number;
      bottom: boolean;
      prepend: boolean;
    } | null>(null),
    composer = useRef<HTMLTextAreaElement>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const [files, setFiles] = useState<File[]>([]);
  const attachmentRevision = useRef(0);
  const fileLimits = inbox?.attachment_limits ?? defaultAttachmentLimits;
  function selectFiles(selected: File[]) {
    const next = [...files, ...selected];
    if (
      (fileLimits.max_files > 0 && next.length > fileLimits.max_files) ||
      next.some(
        (f) =>
          f.size === 0 ||
          (fileLimits.max_file_mb > 0 &&
            f.size > fileLimits.max_file_mb * 1024 * 1024),
      ) ||
      (fileLimits.max_total_mb > 0 &&
        next.reduce((sum, f) => sum + f.size, 0) >
          fileLimits.max_total_mb * 1024 * 1024)
    ) {
      setSendError(
        "Choose nonempty files within your attachment limits in Settings ? Messages & privacy.",
      );
      return;
    }
    attachmentRevision.current++;
    setFiles(next);
    setSendError("");
  }
  const [away, setAway] = useState(false),
    [dragging, setDragging] = useState(false);
  const [hasNew, setHasNew] = useState(false),
    [olderNotice, setOlderNotice] = useState("");
  const [messages, setMessages] = useState<Message[]>([]),
    [cursor, setCursor] = useState(""),
    [search, setSearch] = useState(""),
    [searchOpen, setSearchOpen] = useState(false),
    [query, setQuery] = useState(""),
    [text, setText] = useState(""),
    [error, setError] = useState(""),
    [sendError, setSendError] = useState(""),
    [loading, setLoading] = useState(false);
  const canAttach =
    !!inbox?.enabled && !!state?.capabilities.includes("attachments");
  const changeCursor = useRef<number | null>(null);
  const changesBusy = useRef(false);
  const changesDone = useRef<Promise<void> | null>(null);
  const messagesRef = useRef<Message[]>([]);
  messagesRef.current = messages;
  const [viewRange, setViewRange] = useState<{since: string; until: string} | null>(null);
  useViewedHistoryRefresh(conversation.id, !!state?.transport_ready && inboxAllows(inbox, conversation),
    !!state?.capabilities.includes("archive_refresh") && (!!inbox?.enabled || !!conversation.archived), viewRange ?? undefined);
  const openedAt = useRef(new Date().toISOString());
  const rangeFrame = useRef(0);
  function updateViewedRange() {
    const el = scroll.current;
    if (!el) return;
    const bounds = el.getBoundingClientRect();
    const indices = Array.from(el.querySelectorAll<HTMLElement>("[data-message-index]"))
      .filter(node => { const r = node.getBoundingClientRect(); return r.bottom >= bounds.top && r.top <= bounds.bottom; })
      .map(node => Number(node.dataset.messageIndex));
    const rows = messagesRef.current;
    const first = indices.length ? Math.max(0, Math.min(...indices) - 5) : Math.max(0, rows.length - 50);
    const last = indices.length ? Math.min(rows.length - 1, Math.max(...indices) + 5) : rows.length - 1;
    const since = rows[first]?.at ?? new Date(Date.parse(openedAt.current) - 30 * 60 * 1000).toISOString();
    const until = el.scrollHeight - el.scrollTop - el.clientHeight < 80 ? "" : rows[last]?.at ?? "";
    setViewRange(previous => previous?.since === since && previous.until === until ? previous : {since, until});
  }
  function scheduleViewedRange() {
    cancelAnimationFrame(rangeFrame.current);
    rangeFrame.current = requestAnimationFrame(updateViewedRange);
  }
  useEffect(() => {
    scheduleViewedRange();
    return () => cancelAnimationFrame(rangeFrame.current);
  }, [messages, conversation.id, query]);
  const request = useRef(1),
    inFlight = useRef(0),
    inFlightDone = useRef<{ generation: number; done: Promise<void> } | null>(
      null,
    ),
    foreground = useRef({ generation: 1, count: 0 });
  function beginForeground(generation: number) {
    if (foreground.current.generation !== generation)
      foreground.current = { generation, count: 0 };
    foreground.current.count++;
    setLoading(true);
  }
  function endForeground(generation: number) {
    if (foreground.current.generation === generation) {
      foreground.current.count--;
      if (generation === request.current && foreground.current.count === 0)
        setLoading(false);
    }
  }
  const [importCursor, setImportCursor] = useState(""),
    [importFinished, setImportFinished] = useState(false);
  async function importPage() {
    const generation = request.current;
    beginForeground(generation);
    try {
      const result = await api<{ imported: number; next_cursor: string }>(
        `${base}/history/import`,
        "POST",
        { cursor: importCursor },
      );
      if (generation !== request.current) return false;
      setImportCursor(
        result.next_cursor === importCursor ? "" : result.next_cursor,
      );
      setImportFinished(
        !result.next_cursor || result.next_cursor === importCursor,
      );
      const first = messagesRef.current[0];
      const before = first
        ? btoa(JSON.stringify({ at: first.at, id: first.id }))
            .replaceAll("+", "-")
            .replaceAll("/", "_")
            .replaceAll("=", "")
        : "";
      await load(before, true);
      await refreshChanges();
      return result.imported > 0;
    } catch (e) {
      if (generation === request.current)
        setError(
          e instanceof Error
            ? e.message.replaceAll("_", " ")
            : "Could not import phone history",
        );
      return false;
    } finally {
      endForeground(generation);
    }
  }
  useEffect(() => {
    setImportCursor("");
    setImportFinished(false);
  }, [conversation.id]);
  async function load(
    next = "",
    append = false,
    generation = request.current,
    background = false,
  ) {
    if (generation !== request.current) return;
    if (
      background &&
      foreground.current.generation === generation &&
      foreground.current.count > 0
    )
      return;
    if (!background) beginForeground(generation);
    let resolveDone: (() => void) | undefined;
    try {
      while (inFlight.current === generation || changesBusy.current) {
        if (background) return;
        await (changesBusy.current ? changesDone.current : inFlightDone.current?.done);
        if (generation !== request.current) return;
      }
      if (generation !== request.current) return;
      inFlight.current = generation;
      inFlightDone.current = {
        generation,
        done: new Promise<void>((resolve) => {
          resolveDone = resolve;
        }),
      };
      const params = new URLSearchParams({ limit: "50", search: query });
      if (next) params.set("cursor", next);
      const result = await api<History>(`${base}/history?${params}`);
      if (generation === request.current && !next && changeCursor.current === null && result.change_cursor !== undefined) changeCursor.current = result.change_cursor;
      if (generation === request.current) {
        const el = scroll.current;
        scrollSnapshot.current = el
          ? {
              top: el.scrollTop,
              height: el.scrollHeight,
              bottom:
                !append ||
                el.scrollHeight - el.scrollTop - el.clientHeight < 80,
              prepend: !!next,
            }
          : null;
        if (
          append &&
          !next &&
          el &&
          el.scrollHeight - el.scrollTop - el.clientHeight >= 80 &&
          result.messages.some(
            (m) => !messagesRef.current.some((old) => old.id === m.id),
          )
        )
          setHasNew(true);
        setMessages((previous) => {
          const map = new Map((append ? previous : []).map((m) => [m.id, m]));
          for (const m of result.messages) map.set(m.id, m);
          return [...map.values()].sort(
            (a, b) =>
              Date.parse(a.at) - Date.parse(b.at) || a.id.localeCompare(b.id),
          );
        });
        setCursor((previous) =>
          append && !next ? previous : result.next_cursor || "",
        );
        setError("");
      }
    } catch (e) {
      if (generation === request.current)
        setError(
          e instanceof ApiError
            ? e.message.replaceAll("_", " ")
            : "Could not load messages.",
        );
    } finally {
      if (resolveDone) {
        if (inFlight.current === generation) inFlight.current = 0;
        resolveDone();
      }
      if (!background) endForeground(generation);
    }
  }
  async function refreshChanges() {
    if (!base.startsWith("/inbox") || !state?.capabilities.includes("archive_history") || changeCursor.current === null || changesBusy.current || inFlight.current !== 0) return;
    const generation = request.current;
    changesBusy.current = true;
    let finish!: () => void;
    changesDone.current = new Promise<void>((resolve) => { finish = resolve; });
    try {
      for (let i = 0; i < 10; i++) {
        const result: {messages: Message[]; deleted?: string[]; cursor: number; more: boolean} = await api<{messages: Message[]; deleted?: string[]; cursor: number; more: boolean}>(`${base}/changes?after=${changeCursor.current}`);
        if (generation !== request.current) return;
        const el = scroll.current;
        if (el && result.messages.length) {
          const bottom = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
          scrollSnapshot.current = { top: el.scrollTop, height: el.scrollHeight, bottom, prepend: false };
          if (!bottom && result.messages.some((m) => !messagesRef.current.some((old) => old.id === m.id))) setHasNew(true);
        }
        if (result.messages.length || result.deleted?.length) setMessages((old) => {
          const map = new Map(old.map((m) => [m.id, m]));
          for (const id of result.deleted ?? []) map.delete(id);
          for (const m of result.messages) {
            const matches = !query || (m.text + "\n" + (m.attachments ?? []).map((a) => a.name).join("\n")).toLowerCase().includes(query.toLowerCase());
            if (matches) map.set(m.id, m); else map.delete(m.id);
          }
          return [...map.values()].sort((a,b) => Date.parse(a.at)-Date.parse(b.at) || a.id.localeCompare(b.id));
        });
        changeCursor.current = result.cursor;
        setError("");
        if (!result.more) break;
      }
    } catch {
      if (generation === request.current) setError("Could not refresh messages. Retrying automatically.");
    } finally { changesBusy.current = false; finish(); }
  }
  useLayoutEffect(() => {
    const el = scroll.current,
      s = scrollSnapshot.current;
    if (!el) return;
    if (!s) return;
    if (s.bottom) el.scrollTop = el.scrollHeight;
    else el.scrollTop = s.top + (s.prepend ? el.scrollHeight - s.height : 0);
    scrollSnapshot.current = null;
  }, [messages]);
  const loadingOlder = useRef(false);
  const previousScrollTop = useRef(0);
  async function loadOlder() {
    if (loadingOlder.current || loading) return;
    loadingOlder.current = true;
    try {
      if (cursor) await load(cursor, true);
      else if (inbox?.load_older && !importFinished) {
        const imported = await importPage();
        setOlderNotice(imported ? "Loaded older messages from your phone." : "No additional messages are available yet.");
      }
    } finally {
      loadingOlder.current = false;
    }
  }
  useEffect(() => {
    request.current++;
    openedAt.current = new Date().toISOString();
    setViewRange(null);
    inFlight.current = 0;
    setMessages([]);
    changeCursor.current = null;
    setCursor("");
    setError("");
    void load();
    return () => {
      request.current++;
    };
  }, [conversation.id, query, base]);
  useEffect(() => {
    const generation = request.current;
    const timer = setInterval(
      () => {
        if (state?.capabilities.includes("archive_history") && base.startsWith("/inbox") && changeCursor.current !== null) void refreshChanges();
        else void load("", true, generation, true);
      },
      3000,
    );
    return () => clearInterval(timer);
  }, [conversation.id, query, base, state?.capabilities.includes("archive_history")]);
  // Search as you type; the request guard discards results from older queries.
  useEffect(() => {
    const timer = setTimeout(() => setQuery(search.trim()), 300);
    return () => clearTimeout(timer);
  }, [search]);
  useEffect(() => {
    const el = composer.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = Math.min(el.scrollHeight, 200) + "px";
  }, [text]);
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
  const pending = usePending(conversation.id);
  const readInbox = async () =>
    state?.capabilities.includes("full_inbox")
      ? await api<InboxSettings>("/inbox/settings")
      : inbox;
  // The draft clears on the next render; until then a second Enter or click would
  // read the same text and enqueue a duplicate with a new key.
  const draftTaken = useRef(false);
  useEffect(() => {
    draftTaken.current = false;
  }, [text, files]);
  function send() {
    const outgoing = text.trim();
    if (!canSend || draftTaken.current || (!outgoing && files.length === 0)) return;
    draftTaken.current = true;
    // Clear the box at once; the pending bubble carries the message from here.
    const item: Pending = {
      key: crypto.randomUUID(),
      text: outgoing,
      files,
      at: new Date().toISOString(),
      route: null,
      phase: "posting",
    };
    setText("");
    setFiles([]);
    attachmentRevision.current++;
    setSendError("");
    addPending(conversation.id, item);
    void enqueue(() => postPending(conversation, item.key, readInbox));
    composer.current?.focus();
  }
  function retryPending(p: Pending) {
    patchPending(conversation.id, p.key, { phase: "posting", failure: undefined, error: undefined });
    void enqueue(() => postPending(conversation, p.key, readInbox));
  }
  function editPending(p: Pending) {
    removePending(conversation.id, p.key);
    setText((current) => (current.trim() ? `${p.text}
${current}` : p.text));
    if (p.files.length) selectFiles(p.files);
    composer.current?.focus();
  }
  function refreshNow() {
    if (base.startsWith("/inbox") && state?.capabilities.includes("archive_history") && changeCursor.current !== null)
      void refreshChanges();
    else void load("", true, request.current, true);
  }
  // Mark the conversation read on the phone when the owner is actually looking at
  // its newest message: tab visible and focused, scrolled to the bottom, and
  // either the phone reports it unread or a new message arrived while open.
  const [attention, setAttention] = useState(0);
  useEffect(() => {
    const bump = () => setAttention((n) => n + 1);
    window.addEventListener("focus", bump);
    window.addEventListener("blur", bump);
    document.addEventListener("visibilitychange", bump);
    return () => {
      window.removeEventListener("focus", bump);
      window.removeEventListener("blur", bump);
      document.removeEventListener("visibilitychange", bump);
    };
  }, []);
  const readBaseline = useRef<number | null>(null),
    readThrough = useRef(0),
    readTried = useRef(0);
  const readEnabled =
    !!state?.capabilities.includes("read_receipts") &&
    !!inbox?.enabled &&
    inbox.mark_read_when_viewed !== false &&
    inboxAllows(inbox, conversation);
  useEffect(() => {
    readBaseline.current = null;
    readThrough.current = 0;
    readTried.current = 0;
  }, [conversation.id]);
  useEffect(() => {
    if (loading || !messages.length) return;
    let newest = 0,
      newestIncoming = 0;
    for (const m of messages) {
      const at = Date.parse(m.at);
      if (!Number.isFinite(at)) continue;
      newest = Math.max(newest, at);
      if (m.direction === "incoming") newestIncoming = Math.max(newestIncoming, at);
    }
    if (readBaseline.current === null) readBaseline.current = newestIncoming;
    if (!readEnabled || away || !newest || newest <= readThrough.current) return;
    if (document.visibilityState !== "visible" || !document.hasFocus()) return;
    const arrivedWhileOpen = newestIncoming > (readBaseline.current ?? 0);
    if (!conversation.unread && !arrivedWhileOpen) return;
    if (Date.now() - readTried.current < 5000) return;
    readTried.current = Date.now();
    const through = newest;
    const generation = request.current;
    void api(`/inbox/conversations/${encodeURIComponent(conversation.id)}/read`, "POST", {
      through: new Date(through).toISOString(),
    })
      .then(() => {
        if (generation !== request.current) return;
        readThrough.current = through;
        markSeen(conversation);
      })
      .catch(() => {
        /* a newer message or a transient failure; the next change retries */
      });
  }, [messages, away, attention, readEnabled, conversation.unread, conversation.id, loading]);
  // A send reaching the phone refreshes the open thread at once (see usePendingFollower).
  const submittedSignal = useSubmittedSignal();
  const seenSignal = useRef(submittedSignal);
  useEffect(() => {
    if (submittedSignal === seenSignal.current) return;
    seenSignal.current = submittedSignal;
    refreshNow();
  }, [submittedSignal]);
  // Drop a sent bubble as soon as the saved message with its transaction appears.
  useEffect(() => {
    for (const p of pending)
      if (p.phase === "sent" && p.transaction && messages.some((m) => m.id === p.transaction))
        removePending(conversation.id, p.key);
  }, [messages, pending, conversation.id]);
  const savedIds = new Set(messages.map((m) => m.id));
  const pendingCount = pending.length,
    previousPending = useRef(pendingCount);
  useLayoutEffect(() => {
    const el = scroll.current;
    if (el && pendingCount > previousPending.current) el.scrollTop = el.scrollHeight;
    previousPending.current = pendingCount;
  }, [pendingCount]);

  const showOlder = (inbox?.enabled || conversation.archived)
    ? !!cursor || (!!inbox?.load_older && !importFinished)
    : !!cursor;
  const virtualized = messages.length > 300;
  const virtualizer = useVirtualizer({ count: messages.length, getScrollElement: () => scroll.current, estimateSize: () => 110, overscan: 12, getItemKey: (index) => messages[index].id, enabled: virtualized });
  const visibleRows = virtualized ? virtualizer.getVirtualItems().map((row) => ({m: messages[row.index], row})) : messages.map((m) => ({m, row: null}));
  return (
    <div className="thread">
      <header className="thread-head">
        {onBack && (
          <button
            type="button"
            className="icon ghost only-mobile"
            aria-label="Back to conversations"
            onClick={onBack}
          >
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
        <button
          type="button"
          className="older"
          disabled={loading || (!!inbox?.enabled && busy)}
          onClick={() => void loadOlder()}
        >
          Load older messages
        </button>
      )}
      <div
        className="messages"
        ref={scroll}
        onScroll={() => {
          scheduleViewedRange();
          const el = scroll.current;
          if (!el) return;
          const fromBottom = el.scrollHeight - el.scrollTop - el.clientHeight;
          if (fromBottom < 80) setHasNew(false);
          setAway(fromBottom > 400);
          const upwards = el.scrollTop < previousScrollTop.current;
          previousScrollTop.current = el.scrollTop;
          if (upwards && (inbox?.enabled || conversation.archived) && el.scrollTop < 250) void loadOlder();
        }}
      >
        {inbox?.enabled && !cursor && importFinished && (
          <p className="history-end">
            That’s everything your phone returned. Older messages may still
            exist.
          </p>
        )}
        {inbox?.enabled && olderNotice && (
          <p role="status" className="sr-only">
            {loading ? "Loading messages…" : olderNotice}
          </p>
        )}
        {messages.length === 0 && (
          <div className="thread-empty">
            <strong>
              {loading
                ? "Loading messages…"
                : query
                  ? `No messages match “${query}”.`
                  : "No messages here yet."}
            </strong>
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
                  {!inbox?.enabled &&
                  state?.capabilities.includes("history_import")
                    ? ". Use the history button to import older ones."
                    : "."}
                </small>
              )
            )}
          </div>
        )}
        <div style={virtualized ? {height: virtualizer.getTotalSize(), flexShrink: 0, position: "relative"} : {display: "contents"}}>
        {visibleRows.map(({m, row}) => {
          const day = dayLabel(m.at);
          const index = row?.index ?? messages.indexOf(m);
          const divider = index === 0 || day !== dayLabel(messages[index - 1].at);
          const at = new Date(m.at);
          return (
            <div key={m.id} data-message-index={index} data-index={row?.index} ref={row ? virtualizer.measureElement : undefined} style={row ? {position: "absolute", top: 0, left: 0, width: "100%", transform: `translateY(${row.start}px)`} : undefined} className="message-group">
              {divider && (
                <div className="day" role="separator">
                  <span>{day}</span>
                </div>
              )}
              <div
                data-message-id={m.id}
                className={
                  "bubble " +
                  m.direction +
                  (m.direction === "outgoing"
                    ? " from-" + (m.origin ?? "bot")
                    : "")
                }
              >
                {m.attachments?.map((attachment) => (
                  <MessageAttachment
                    key={attachment.id}
                    attachment={attachment}
                    fileLimitMB={fileLimits.max_file_mb}
                    base={base}
                    message={m.id}
                  />
                ))}
                {m.text && <p>{m.text}</p>}
                <small>
                  {m.direction === "outgoing" && (
                    <span className="sender">
                      {senders[m.origin ?? ""] ?? "Sent"}
                    </span>
                  )}
                  <time dateTime={m.at} title={at.toLocaleString()}>
                    {at.toLocaleTimeString([], {
                      hour: "numeric",
                      minute: "2-digit",
                    })}
                  </time>
                  {m.revision ? <span title="Previously observed content is retained in the encrypted archive">Edited</span> : null}
                  {m.status && (
                    <span
                      className="msg-status"
                      title={m.status === "read" ? "Google reports this message was displayed" : undefined}
                    >
                      {statusLabel(m.status)}
                    </span>
                  )}
                </small>
              </div>
            </div>
          );
        })}
        </div>
        {pending.filter((p) => !(p.phase === "sent" && p.transaction && savedIds.has(p.transaction))).map((p) => (
          <div key={p.key} className="message-group pending-send">
            <div
              className={"bubble outgoing from-web pending" + (p.phase === "failed" ? " send-failed" : "")}
              data-pending={p.phase}
            >
              {p.files.map((file, index) => (
                <PendingFile key={`${index}-${file.name}`} file={file} />
              ))}
              {p.text && <p>{p.text}</p>}
              <small>
                {p.phase === "failed" ? (
                  <span className="pending-error" role="alert">{p.error}</span>
                ) : (
                  <span className="pending-state msg-status" role="status">
                    {p.phase === "sent" ? "Sent" : (<><span className="spinner" aria-hidden="true" />Sending…</>)}
                  </span>
                )}
              </small>
              {p.phase === "failed" && (
                <div className="pending-actions">
                  {p.failure === "unconfirmed" && (
                    <button type="button" className="small primary" disabled={!canSend} title={canSend ? undefined : "Sending is unavailable right now"} onClick={() => retryPending(p)}>Retry sending</button>
                  )}
                  {p.failure === "not_sent" && (
                    <button type="button" className="small" onClick={() => editPending(p)}>Edit and resend</button>
                  )}
                  <button type="button" className="small ghost" onClick={() => removePending(conversation.id, p.key)}>Dismiss</button>
                </div>
              )}
            </div>
          </div>
        ))}
        {typing && (
          <div className="message-group typing-row" aria-hidden="true">
            <div className="typing-bubble"><span /><span /><span /></div>
          </div>
        )}
      </div>
      {away && !hasNew && (
        <button
          type="button"
          className="jump-latest icon"
          aria-label="Jump to latest"
          title="Jump to latest"
          onClick={() => {
            const el = scroll.current;
            if (el)
              el.scrollTo({
                top: el.scrollHeight,
                behavior: window.matchMedia?.("(prefers-reduced-motion: reduce)")
                  .matches
                  ? "auto"
                  : "smooth",
              });
          }}
        >
          <Icon name="arrowdown" size={18} />
        </button>
      )}
      {hasNew && (
        <button
          type="button"
          className="new-pill"
          onClick={() => {
            if (scroll.current)
              scroll.current.scrollTop = scroll.current.scrollHeight;
            setHasNew(false);
          }}
        >
          New messages ↓
        </button>
      )}
      <form
        className="composer"
        onSubmit={(e) => {
          e.preventDefault();
          send();
        }}
      >
        {blocked && (
          <div className="composer-blocked" role="status">
            {blocked === "paused" ? (
              <>
                <span>Sending is paused.</span>
                {onResume && (
                  <button
                    type="button"
                    className="small primary"
                    disabled={busy}
                    onClick={onResume}
                  >
                    <Icon name="play" size={13} />
                    Resume sending
                  </button>
                )}
              </>
            ) : (
              <span>{blocked}</span>
            )}
          </div>
        )}
        {sendError && (
          <p role="alert" className="inline-alert">
            {sendError}
          </p>
        )}
        {files.length > 0 && (
          <div className="selected-attachments" aria-label="Selected files">
            {files.map((file, index) => (
              <SelectedAttachment
                key={`${index}-${file.name}`}
                file={file}
                disabled={false}
                remove={() => {
                  attachmentRevision.current++;
                  setFiles(files.filter((_, i) => i !== index));
                }}
              />
            ))}
          </div>
        )}
        <div
          className={"composer-box" + (dragging ? " dragging" : "")}
          onDragOver={(e) => {
            if (!canAttach || !e.dataTransfer.types.includes("Files")) return;
            e.preventDefault();
            setDragging(true);
          }}
          onDragLeave={(e) => {
            if (!e.currentTarget.contains(e.relatedTarget as Node | null))
              setDragging(false);
          }}
          onDrop={(e) => {
            setDragging(false);
            if (!canAttach || !e.dataTransfer.files.length) return;
            e.preventDefault();
            selectFiles(Array.from(e.dataTransfer.files));
          }}
        >
          {inbox?.enabled && state?.capabilities.includes("attachments") && (
            <>
              <input
                ref={fileInput}
                className="sr-only"
                type="file"
                multiple
                aria-label="Choose files"
                onChange={(event) => {
                  selectFiles(Array.from(event.target.files ?? []));
                  event.target.value = "";
                }}
              />
              <button
                type="button"
                className="icon ghost"
                aria-label="Attach files"
                title="Attach files ? limits are configurable in Messages & privacy"
                onClick={() => fileInput.current?.click()}
              >
                <Icon name="attach" size={20} />
              </button>
            </>
          )}
          <textarea
            ref={composer}
            onPaste={(e) => {
              // Pasted screenshots/images become attachments. Text pastes are untouched.
              if (!canAttach) return;
              const images = Array.from(e.clipboardData.files).filter((f) =>
                f.type.startsWith("image/"),
              );
              if (!images.length) return;
              e.preventDefault();
              selectFiles(images.map(pastedName));
            }}
            rows={1}
            maxLength={24000}
            aria-label="Message"
            enterKeyHint="enter"
            placeholder={`Text ${conversation.label}`}
            value={text}
            onChange={(e) => {
              setText(Array.from(e.target.value).slice(0, 12000).join(""));
              setSendError("");
            }}
            onKeyDown={(e) => {
              // On touch keyboards Enter adds a line; the send button sends.
              if (
                e.key === "Enter" &&
                !e.shiftKey &&
                !e.nativeEvent.isComposing &&
                !window.matchMedia?.("(pointer: coarse)")?.matches
              ) {
                e.preventDefault();
                if (!e.repeat) send();
              }
            }}
          />
          <button
            className="send"
            aria-label="Send message"
            disabled={!canSend || (!text.trim() && files.length === 0)}
          >
            <Icon name="send" size={18} />
          </button>
        </div>
        <div className="composer-meta">
          <small className="enter-hint">
            Enter to send, Shift+Enter for a new line
          </small>
          {text.length > 600 && (
            <small>{Array.from(text).length.toLocaleString()} / 12,000</small>
          )}
          {state?.fixture && (
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
                  await load("", true);
                })
              }
            >
              Simulate !ping
            </button>
          )}
        </div>
      </form>
    </div>
  );
}

// Browsers name every pasted screenshot "image.png"; give each a distinct name.
let pasteCounter = 0;
function pastedName(file: File): File {
  if (file.name && file.name !== "image.png") return file;
  const now = new Date(),
    pad = (n: number) => String(n).padStart(2, "0");
  const stamp = `${now.getFullYear()}${pad(now.getMonth() + 1)}${pad(now.getDate())}-${pad(now.getHours())}${pad(now.getMinutes())}${pad(now.getSeconds())}`;
  const extension = file.type.split("/")[1]?.replace("jpeg", "jpg").replace(/[^a-z0-9]/gi, "") || "png";
  return new File([file], `image-${stamp}-${++pasteCounter}.${extension}`, { type: file.type, lastModified: file.lastModified });
}

const statusLabels: Record<string, string> = { submitted: "Sent", delivered: "Delivered", read: "Read" };
const statusLabel = (status: string) => statusLabels[status] ?? status;

// Posts one pending message. A retry reuses the same key and API route, so a lost
// response can never create a second message.
async function postPending(
  conversation: Conversation,
  key: string,
  readInbox: () => Promise<InboxSettings | null | undefined>,
) {
  const item = getPending(conversation.id, key);
  if (!item || item.phase !== "posting") return;
  try {
    const attachments = await Promise.all(
      item.files.map(async (f) => ({ name: f.name, mime: f.type, data: await encodeFile(f) })),
    );
    // Archive reads can be ready before the permission poll catches up. Read
    // current collection permission before selecting the write API/body.
    const useInbox = item.route
      ? item.route === "inbox"
      : inboxAllows(await readInbox(), conversation);
    patchPending(conversation.id, key, { route: useInbox ? "inbox" : "legacy" });
    if (!useInbox && attachments.length) throw new ApiError(409, "inbox_access_disabled");
    const composeBase = `${useInbox ? "/inbox" : ""}/conversations/${encodeURIComponent(conversation.id)}`;
    const result = await api<{ id?: string }>(
      `${composeBase}/compose`,
      "POST",
      useInbox ? { text: item.text, request_id: key, attachments } : { text: item.text },
    );
    patchPending(conversation.id, key, { phase: "queued", job: result?.id, queuedAt: Date.now() });
  } catch (e) {
    // The inbox route deduplicates by key, so retrying can never send twice. The
    // legacy route has no key: only a definite server rejection is safe to retry.
    const legacy = getPending(conversation.id, key)?.route === "legacy";
    const safeRetry = !legacy || e instanceof ApiError;
    patchPending(conversation.id, key, {
      phase: "failed",
      failure: safeRetry ? "unconfirmed" : "uncertain",
      error: !safeRetry
        ? "Not confirmed. Check your phone before sending this again."
        : e instanceof ApiError
          ? `Message not confirmed: ${e.message.replaceAll("_", " ")}. Retrying is safe and will not send twice.`
          : "The server did not confirm this message. Retrying is safe and will not send twice.",
    });
  }
}
