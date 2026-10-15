import { useVirtualizer } from "@tanstack/react-virtual";
import { HistorySync } from "./HistorySync";
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
import { Avatar } from "./ui";
import {
  MessageAttachment,
  SelectedAttachment,
  encodeFile,
} from "./Attachments";
type Props = {
  conversation: Conversation;
  state: State | null;
  busy: boolean;
  act: (fn: () => Promise<unknown>, refreshAfter?: boolean) => Promise<void>;
  inbox?: InboxSettings | null;
  enrollControl?: ReactNode;
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
    sendKey = useRef(""),
    sendRoute = useRef<"inbox" | "legacy" | null>(null),
    sendIntentText = useRef(""),
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
    [sending, setSending] = useState(false),
    [loading, setLoading] = useState(false),
    [notice, setNotice] = useState("");
  const changeCursor = useRef<number | null>(null);
  const changesBusy = useRef(false);
  const changesDone = useRef<Promise<void> | null>(null);
  const messagesRef = useRef<Message[]>([]);
  messagesRef.current = messages;
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
      setNotice(
        `Imported ${result.imported} message${result.imported === 1 ? "" : "s"} from your phone. Commands in old messages never run.`,
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
        setOlderNotice(imported ? "Loaded older messages from your phone." : "No additional messages were saved. Check history sync progress.");
      }
    } finally {
      loadingOlder.current = false;
    }
  }
  useEffect(() => {
    request.current++;
    inFlight.current = 0;
    setMessages([]);
    changeCursor.current = null;
    setCursor("");
    setError("");
    setNotice("");
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
            ? "Sync is off. This archive is read-only."
          : !(conversation.enrolled || inbox?.enabled)
            ? "Turn on bot replies to message this conversation."
            : "";
  const canSend = !archiveReadOnly && !blocked && !busy && !sending;
  async function send() {
    const outgoing = text.trim();
    if (!canSend || (!outgoing && files.length === 0)) return;
    const intent = JSON.stringify([outgoing, attachmentRevision.current]);
    // Reuse the idempotency key when retrying the same text after a lost response.
    if (!sendKey.current || sendIntentText.current !== intent) {
      sendKey.current = crypto.randomUUID();
      sendRoute.current = null;
      sendIntentText.current = intent;
    }
    const requestId = sendKey.current;
    setSending(true);
    setSendError("");
    try {
      const attachments = await Promise.all(
        files.map(async (f) => ({
          name: f.name,
          mime: f.type,
          data: await encodeFile(f),
        })),
      );
      // Archive reads can be ready before the permission poll catches up. Read
      // current collection permission before selecting the write API/body.
      const currentInbox = state?.capabilities.includes("full_inbox") ? await api<InboxSettings>("/inbox/settings") : inbox;
      const useInbox = sendRoute.current ? sendRoute.current === "inbox" : inboxAllows(currentInbox, conversation);
      // An uncertain prior submission must keep its original API and key even
      // if collection permission changes before the owner retries.
      sendRoute.current = useInbox ? "inbox" : "legacy";
      const composeBase = `${useInbox ? "/inbox" : ""}/conversations/${encodeURIComponent(conversation.id)}`;
      if (!useInbox && attachments.length) throw new ApiError(409,"inbox_access_disabled");
      await api(
        `${composeBase}/compose`,
        "POST",
        useInbox
          ? { text: outgoing, request_id: requestId, attachments }
          : { text: outgoing },
      );
      setText((current) => (current.trim() === outgoing ? "" : current));
      setFiles([]);
      attachmentRevision.current++;
      sendKey.current = "";
      sendRoute.current = null;
      sendIntentText.current = "";
      void load("", true, request.current, true);
    } catch (e) {
      setSendError(
        e instanceof ApiError
          ? `Message not confirmed: ${e.message.replaceAll("_", " ")}. Your text and files are kept.`
          : "The server did not confirm this message. Your text and files are kept; send again to retry safely.",
      );
    } finally {
      setSending(false);
      composer.current?.focus();
    }
  }

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
            {conversation.protocol}
            {conversation.enrolled ? ", bot replies on" : ""}
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
          {!inbox?.enabled && !conversation.archived &&
            state?.capabilities.includes("history_import") && (
              <button
                type="button"
                className="icon ghost"
                title={
                  importFinished
                    ? "All available phone history imported"
                    : "Import older messages from your phone"
                }
                aria-label={
                  importFinished
                    ? "All available phone history imported"
                    : "Import phone history"
                }
                disabled={
                  busy || loading || importFinished || !state.transport_ready
                }
                onClick={() => void importPage()}
              >
                <Icon name="history" size={18} />
              </button>
            )}
          {enrollControl}
        </div>
      </header>
      {state?.capabilities.includes("archive_history") && (inbox?.enabled || conversation.archived) && <HistorySync id={conversation.id} ready={!!state?.transport_ready && !!inbox?.enabled} />}
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
      {notice && (
        <p role="status" className="inline-note">
          {notice}
          <button
            type="button"
            className="icon ghost"
            aria-label="Dismiss"
            onClick={() => setNotice("")}
          >
            <Icon name="x" size={14} />
          </button>
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
          const el = scroll.current;
          if (!el) return;
          if (el.scrollHeight - el.scrollTop - el.clientHeight < 80)
            setHasNew(false);
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
            <div key={m.id} data-index={row?.index} ref={row ? virtualizer.measureElement : undefined} style={row ? {position: "absolute", top: 0, left: 0, width: "100%", transform: `translateY(${row.start}px)`} : undefined} className="message-group">
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
                  {m.status && <span className="msg-status">{m.status}</span>}
                </small>
              </div>
            </div>
          );
        })}
        </div>
      </div>
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
                disabled={sending}
                remove={() => {
                  attachmentRevision.current++;
                  setFiles(files.filter((_, i) => i !== index));
                }}
              />
            ))}
          </div>
        )}
        <div className="composer-box">
          {inbox?.enabled && state?.capabilities.includes("attachments") && (
            <>
              <input
                ref={fileInput}
                className="sr-only"
                type="file"
                multiple
                aria-label="Choose files"
                disabled={sending}
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
                disabled={sending}
                onClick={() => fileInput.current?.click()}
              >
                <Icon name="attach" size={20} />
              </button>
            </>
          )}
          <textarea
            ref={composer}
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
                send();
              }
            }}
          />
          <button
            className="send"
            aria-label="Send message"
            disabled={!canSend || (!text.trim() && files.length === 0)}
          >
            {sending ? (
              <span className="spinner" />
            ) : (
              <Icon name="send" size={18} />
            )}
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
