import { useEffect, useLayoutEffect, useRef, useState } from "react";
import {
  api,
  ApiError,
  type Conversation,
  type History,
  type Message,
  type State,
  type InboxSettings,
} from "./api";
import { Confirmation } from "./Confirmation";
import { Icon } from "./Icon";
type Props = {
  conversation: Conversation;
  state: State | null;
  busy: boolean;
  act: (fn: () => Promise<unknown>, refreshAfter?: boolean) => Promise<void>;
  inbox?: InboxSettings | null;
};
export function ConversationHistory({
  conversation,
  state,
  busy,
  act,
  inbox,
}: Props) {
  const base = `${inbox?.enabled ? "/inbox" : ""}/conversations/${encodeURIComponent(conversation.id)}`;
  const scroll = useRef<HTMLDivElement>(null),
    scrollSnapshot = useRef<{
      top: number;
      height: number;
      bottom: boolean;
      prepend: boolean;
    } | null>(null),
    sendKey = useRef(""),
    sendIntentText = useRef("");
  const [hasNew, setHasNew] = useState(false),
    [olderNotice, setOlderNotice] = useState("");
  const [messages, setMessages] = useState<Message[]>([]),
    [cursor, setCursor] = useState(""),
    [search, setSearch] = useState(""),
    [query, setQuery] = useState(""),
    [text, setText] = useState(""),
    [error, setError] = useState(""),
    [loading, setLoading] = useState(false),
    [notice, setNotice] = useState("");
  const [confirmation, setConfirmation] = useState<string | null>(null);
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
      if (generation !== request.current) return;
      setImportCursor(
        result.next_cursor === importCursor ? "" : result.next_cursor,
      );
      setImportFinished(
        !result.next_cursor || result.next_cursor === importCursor,
      );
      setNotice(
        `Imported ${result.imported} available text messages. Historical commands were not executed.`,
      );
      const first = messagesRef.current[0];
      const before = first
        ? btoa(JSON.stringify({ at: first.at, id: first.id }))
            .replaceAll("+", "-")
            .replaceAll("/", "_")
            .replaceAll("=", "")
        : "";
      await load(before, true);
    } catch (e) {
      if (generation === request.current)
        setError(
          e instanceof Error
            ? e.message.replaceAll("_", " ")
            : "Could not import phone history",
        );
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
      while (inFlight.current === generation) {
        if (background) return;
        await inFlightDone.current?.done;
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
            : "Could not load retained history.",
        );
    } finally {
      if (resolveDone) {
        if (inFlight.current === generation) inFlight.current = 0;
        resolveDone();
      }
      if (!background) endForeground(generation);
    }
  }
  useLayoutEffect(() => {
    const el = scroll.current,
      s = scrollSnapshot.current;
    if (!el) return;
    if (!s || s.bottom) el.scrollTop = el.scrollHeight;
    else el.scrollTop = s.top + (s.prepend ? el.scrollHeight - s.height : 0);
    scrollSnapshot.current = null;
  }, [messages]);
  const loadingOlder = useRef(false);
  async function loadOlder() {
    if (loadingOlder.current || loading) return;
    loadingOlder.current = true;
    try {
      if (cursor) await load(cursor, true);
      else if (inbox?.load_older && !importFinished) {
        await importPage();
        setOlderNotice(
          "Older phone text loaded. Available history may be incomplete.",
        );
      }
    } finally {
      loadingOlder.current = false;
    }
  }
  useEffect(() => {
    request.current++;
    inFlight.current = 0;
    setMessages([]);
    setCursor("");
    setError("");
    setNotice("");
    void load();
    return () => {
      request.current++;
    };
  }, [conversation.id, query]);
  useEffect(() => {
    const generation = request.current;
    const timer = setInterval(
      () => void load("", true, generation, true),
      3000,
    );
    return () => clearInterval(timer);
  }, [conversation.id, query, cursor]);
  const canSend =
    !!state &&
    !state.paused &&
    state.transport_ready &&
    (conversation.enrolled || !!inbox?.enabled) &&
    conversation.writable &&
    !busy;
  return (
    <>
      <p className="muted">
        Retained text from this conversation. Available history may be
        incomplete.
      </p>
      {!inbox?.enabled && state?.capabilities.includes("history_import") && (
        <details className="history-tools">
          <summary>
            <Icon name="clock" size={16} />
            History import options
          </summary>
          <p className="muted">
            Import available phone text into encrypted server storage.
            Attachments are excluded, retention applies, and historical commands
            never run.
          </p>
          <button
            className="secondary"
            disabled={
              busy || loading || importFinished || !state.transport_ready
            }
            onClick={() => void importPage()}
          >
            {importFinished
              ? "Available text pages imported"
              : importCursor
                ? "Import next phone history page"
                : "Import phone text history"}
          </button>
        </details>
      )}
      <form
        className="search-form"
        onSubmit={(e) => {
          e.preventDefault();
          setQuery(search);
        }}
      >
        <label className="field">
          Search retained history
          <input
            type="search"
            placeholder="Find something in this conversation"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
        </label>
        <button className="secondary" disabled={loading}>
          <Icon name="search" size={15} />
          Search history
        </button>
      </form>
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      {messages.length === 0 ? (
        <div className="empty">
          <Icon name="conversations" size={24} />
          <strong>
            {loading
              ? "Loading history…"
              : query
                ? "No messages match your search."
                : "No retained messages yet."}
          </strong>
          {query && (
            <button
              className="secondary"
              onClick={() => {
                setSearch("");
                setQuery("");
              }}
            >
              Clear history search
            </button>
          )}
        </div>
      ) : (
        <div
          className={inbox?.enabled ? "messages chat-messages" : "messages"}
          ref={scroll}
          onScroll={() => {
            const el = scroll.current;
            if (!el || !inbox?.enabled) return;
            if (el.scrollHeight - el.scrollTop - el.clientHeight < 80)
              setHasNew(false);
            if (el.scrollTop < 40) void loadOlder();
          }}
        >
          {messages.map((m) => (
            <div
              key={m.id}
              data-message-id={m.id}
              className={"bubble " + m.direction}
            >
              <p>{m.text}</p>
              <small>
                {m.direction === "outgoing"
                  ? m.origin === "web"
                    ? "You · Web"
                    : m.origin === "phone"
                      ? "You · Phone"
                      : "Outgoing"
                  : "Incoming"}{" "}
                · {new Date(m.at).toLocaleString()}
                {m.status && ` · ${m.status}`}
              </small>
            </div>
          ))}
        </div>
      )}
      {hasNew && (
        <button
          className="secondary"
          onClick={() => {
            if (scroll.current)
              scroll.current.scrollTop = scroll.current.scrollHeight;
            setHasNew(false);
          }}
        >
          New messages ↓
        </button>
      )}
      {inbox?.enabled && (
        <>
          <p role="status" className="muted">
            {loading ? "Loading messages…" : olderNotice}
          </p>
          {(cursor || (inbox.load_older && !importFinished)) && (
            <button
              className="secondary"
              disabled={loading || busy}
              onClick={() => void loadOlder()}
            >
              Load older messages
            </button>
          )}
          {!cursor && importFinished && (
            <p className="muted">
              No more available phone text. This does not establish complete
              history.
            </p>
          )}
        </>
      )}
      {!inbox?.enabled && cursor && (
        <button
          className="secondary"
          disabled={loading}
          onClick={() => void load(cursor, true)}
        >
          Load older messages
        </button>
      )}
      <form
        className="compose"
        onSubmit={(e) => {
          e.preventDefault();
          if (canSend && text.trim()) {
            if (!sendKey.current || sendIntentText.current !== text.trim()) {
              sendKey.current = crypto.randomUUID();
              sendIntentText.current = text.trim();
            }
            setConfirmation(text);
          }
        }}
      >
        <label className="field">
          Manual message
          <textarea
            maxLength={24000}
            required
            placeholder="Write a reply…"
            value={text}
            onChange={(e) => {
              setText(Array.from(e.target.value).slice(0, 12000).join(""));
              setNotice("");
            }}
          />
        </label>
        <div className="compose-meta">
          <small>
            {Array.from(text).length.toLocaleString()} / 12,000 characters
          </small>
          <small>
            {state?.paused
              ? "Bot paused"
              : !state?.transport_ready
                ? "Phone unavailable"
                : "Ready to queue"}
          </small>
        </div>
        <p className="muted">
          Review the recipient and text before queuing. Submission status
          appears in Activity.
        </p>
        <button disabled={!canSend || !text.trim()}>
          Send message
          <Icon name="arrow" size={16} />
        </button>
      </form>
      {notice && (
        <p role="status" className="banner">
          {notice}
        </p>
      )}
      {state?.fixture && (
        <button
          className="secondary"
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
      {confirmation !== null && (
        <Confirmation
          title="Queue this message?"
          description={`This reply will be queued for ${conversation.label}. Delivery is not confirmed by queuing.`}
          confirmLabel="Confirm send"
          cancelLabel="Keep editing"
          disabled={!canSend}
          onCancel={() => setConfirmation(null)}
          onConfirm={() => {
            const outgoing = confirmation,
              requestId = sendKey.current;
            setConfirmation(null);
            void act(async () => {
              await api(
                `${base}/compose`,
                "POST",
                inbox?.enabled
                  ? { text: outgoing, request_id: requestId }
                  : { text: outgoing },
              );
              setText((current) => (current === outgoing ? "" : current));
              sendKey.current = "";
              sendIntentText.current = "";
              setNotice(
                "Message queued. Check Activity for submission status.",
              );
              void load("", true, request.current, true);
            }, false);
          }}
        >
          <div className="compose-summary">{confirmation}</div>
        </Confirmation>
      )}
    </>
  );
}
