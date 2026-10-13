import { useEffect, useRef, useState } from "react";
import {
  api,
  ApiError,
  type Conversation,
  type History,
  type Message,
  type State,
} from "./api";
import { Confirmation } from "./Confirmation";
import { Icon } from "./Icon";
type Props = {
  conversation: Conversation;
  state: State | null;
  busy: boolean;
  act: (fn: () => Promise<unknown>, refreshAfter?: boolean) => Promise<void>;
};
export function ConversationHistory({ conversation, state, busy, act }: Props) {
  const [messages, setMessages] = useState<Message[]>([]),
    [cursor, setCursor] = useState(""),
    [search, setSearch] = useState(""),
    [query, setQuery] = useState(""),
    [text, setText] = useState(""),
    [error, setError] = useState(""),
    [loading, setLoading] = useState(false),
    [notice, setNotice] = useState("");
  const [confirmation, setConfirmation] = useState<string | null>(null);
  const request = useRef(1),
    inFlight = useRef(0);
  const [importCursor, setImportCursor] = useState(""),
    [importFinished, setImportFinished] = useState(false);
  async function importPage() {
    await act(async () => {
      const result = await api<{ imported: number; next_cursor: string }>(
        `/conversations/${encodeURIComponent(conversation.id)}/history/import`,
        "POST",
        { cursor: importCursor },
      );
      setImportCursor(result.next_cursor);
      setImportFinished(!result.next_cursor);
      setNotice(
        `Imported ${result.imported} available text messages. Historical commands were not executed.`,
      );
      await load();
    }, false);
  }
  useEffect(() => {
    setImportCursor("");
    setImportFinished(false);
  }, [conversation.id]);
  async function load(next = "", append = false, generation = request.current) {
    if (generation !== request.current || inFlight.current === generation)
      return;
    inFlight.current = generation;
    setLoading(true);
    try {
      const params = new URLSearchParams({ limit: "50", search: query });
      if (next) params.set("cursor", next);
      const result = await api<History>(
        `/conversations/${encodeURIComponent(conversation.id)}/history?${params}`,
      );
      if (generation === request.current) {
        setMessages((previous) => {
          const map = new Map((append ? previous : []).map((m) => [m.id, m]));
          for (const m of result.messages) map.set(m.id, m);
          return [...map.values()].sort(
            (a, b) => Date.parse(a.at) - Date.parse(b.at),
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
      if (generation === request.current) setLoading(false);
      if (inFlight.current === generation) inFlight.current = 0;
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
    const timer = setInterval(() => void load("", true, generation), 3000);
    return () => clearInterval(timer);
  }, [conversation.id, query, cursor]);
  const canSend =
    !!state &&
    !state.paused &&
    state.transport_ready &&
    conversation.enrolled &&
    conversation.writable &&
    !busy;
  return (
    <>
      <p className="muted">
        Retained text from this conversation. Available history may be
        incomplete.
      </p>
      {state?.capabilities.includes("history_import") && (
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
        <div className="messages">
          {messages.map((m) => (
            <div key={m.id} className={"bubble " + m.direction}>
              <p>{m.text}</p>
              <small>
                {m.direction === "outgoing" ? "Your bot" : "Incoming"} ·{" "}
                {new Date(m.at).toLocaleString()}
              </small>
            </div>
          ))}
        </div>
      )}
      {cursor && (
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
          if (canSend && text.trim()) setConfirmation(text);
        }}
      >
        <label className="field">
          Manual message
          <textarea
            maxLength={10000}
            required
            placeholder="Write a reply…"
            value={text}
            onChange={(e) => {
              setText(e.target.value);
              setNotice("");
            }}
          />
        </label>
        <div className="compose-meta">
          <small>{text.length.toLocaleString()} / 10,000 characters</small>
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
            const outgoing = confirmation;
            setConfirmation(null);
            void act(async () => {
              await api(
                `/conversations/${encodeURIComponent(conversation.id)}/compose`,
                "POST",
                { text: outgoing },
              );
              setText("");
              setNotice(
                "Message queued. Check Activity for submission status.",
              );
              await load("", true);
            }, false);
          }}
        >
          <div className="compose-summary">{confirmation}</div>
        </Confirmation>
      )}
    </>
  );
}
