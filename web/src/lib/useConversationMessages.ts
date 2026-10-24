import { useEffect, useRef, useState } from "react";
import { api, ApiError, type History, type Message } from "./api";
import type { ThreadScroll } from "./useThreadScroll";

type Changes = { messages: Message[]; deleted?: string[]; cursor: number; more: boolean };

const byTime = (a: Message, b: Message) =>
  Date.parse(a.at) - Date.parse(b.at) || a.id.localeCompare(b.id);
const merge = (previous: Message[], next: Message[]) =>
  [...new Map([...previous, ...next].map((m) => [m.id, m])).values()].sort(byTime);
const matchesQuery = (m: Message, query: string) =>
  !query ||
  (m.text + "\n" + (m.attachments ?? []).map((a) => a.name).join("\n"))
    .toLowerCase()
    .includes(query.toLowerCase());

type Options = {
  conversationId: string;
  /** API prefix: the inbox or legacy conversation routes. */
  base: string;
  /** Applied search; changing it starts a new request generation. */
  query: string;
  /** Persisted archives offer a change feed instead of snapshot polling. */
  changeFeed: boolean;
  /** Older pages may be imported from the phone when local history runs out. */
  loadOlderFromPhone: boolean;
  viewport: ThreadScroll;
};

/**
 * One conversation's messages: the single authoritative list and every
 * request that changes it. A request generation is bumped when the
 * conversation or search changes, so older responses are dropped. Page reads
 * and change-feed reads never overlap, so an older snapshot cannot resurrect
 * a deleted row or undo an applied edit. One poll loop, paused while the tab
 * is hidden, keeps the list current.
 */
export function useConversationMessages({ conversationId, base, query, changeFeed, loadOlderFromPhone, viewport }: Options) {
  const [messages, setMessages] = useState<Message[]>([]);
  const [cursor, setCursor] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [importCursor, setImportCursor] = useState("");
  const [importFinished, setImportFinished] = useState(false);
  const [olderNotice, setOlderNotice] = useState("");
  const messagesRef = useRef<Message[]>([]);
  messagesRef.current = messages;
  const request = useRef(1);
  const changeCursor = useRef<number | null>(null);
  const changesBusy = useRef(false);
  const changesDone = useRef<Promise<void> | null>(null);
  const inFlight = useRef(0);
  const inFlightDone = useRef<{ generation: number; done: Promise<void> } | null>(null);
  const foreground = useRef({ generation: 1, count: 0 });
  const loadingOlder = useRef(false);
  const usesFeed = () => changeFeed && base.startsWith("/inbox") && changeCursor.current !== null;

  function beginForeground(generation: number) {
    if (foreground.current.generation !== generation) foreground.current = { generation, count: 0 };
    foreground.current.count++;
    setLoading(true);
  }
  function endForeground(generation: number) {
    if (foreground.current.generation === generation) {
      foreground.current.count--;
      if (generation === request.current && foreground.current.count === 0) setLoading(false);
    }
  }

  /** Reads a page. next pages older history; append merges into the list. */
  async function load(next = "", append = false, generation = request.current, background = false) {
    if (generation !== request.current) return;
    if (background && foreground.current.generation === generation && foreground.current.count > 0) return;
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
      inFlightDone.current = { generation, done: new Promise<void>((resolve) => { resolveDone = resolve; }) };
      const params = new URLSearchParams({ limit: "50", search: query });
      if (next) params.set("cursor", next);
      const result = await api<History>(`${base}/history?${params}`);
      if (generation === request.current && !next && changeCursor.current === null && result.change_cursor !== undefined)
        changeCursor.current = result.change_cursor;
      if (generation === request.current) {
        const keepBottom = !append || viewport.nearBottom();
        viewport.capture({ prepend: !!next, keepBottom });
        if (append && !next && !keepBottom && result.messages.some((m) => !messagesRef.current.some((old) => old.id === m.id)))
          viewport.noteNew();
        setMessages((previous) => merge(append ? previous : [], result.messages));
        setCursor((previous) => (append && !next ? previous : result.next_cursor || ""));
        setError("");
      }
    } catch (e) {
      if (generation === request.current)
        setError(e instanceof ApiError ? e.message.replaceAll("_", " ") : "Could not load messages.");
    } finally {
      if (resolveDone) {
        if (inFlight.current === generation) inFlight.current = 0;
        resolveDone();
      }
      if (!background) endForeground(generation);
    }
  }

  /** Applies the local change feed: new rows, edits, statuses and deletions. */
  async function refreshChanges() {
    if (!base.startsWith("/inbox") || !changeFeed || changeCursor.current === null || changesBusy.current || inFlight.current !== 0) return;
    const generation = request.current;
    changesBusy.current = true;
    let finish!: () => void;
    changesDone.current = new Promise<void>((resolve) => { finish = resolve; });
    try {
      for (let i = 0; i < 10; i++) {
        const result: Changes = await api<Changes>(`${base}/changes?after=${changeCursor.current}`);
        if (generation !== request.current) return;
        if (result.messages.length) {
          const bottom = viewport.nearBottom();
          viewport.capture({ prepend: false, keepBottom: bottom });
          if (!bottom && result.messages.some((m) => !messagesRef.current.some((old) => old.id === m.id))) viewport.noteNew();
        }
        if (result.messages.length || result.deleted?.length)
          setMessages((old) => {
            const map = new Map(old.map((m) => [m.id, m]));
            for (const id of result.deleted ?? []) map.delete(id);
            // Return changes even when an edit stops matching search, so it can be removed.
            for (const m of result.messages) {
              if (matchesQuery(m, query)) map.set(m.id, m);
              else map.delete(m.id);
            }
            return [...map.values()].sort(byTime);
          });
        changeCursor.current = result.cursor;
        setError("");
        if (!result.more) break;
      }
    } catch {
      if (generation === request.current) setError("Could not refresh messages. Retrying automatically.");
    } finally {
      changesBusy.current = false;
      finish();
    }
  }

  /** Imports one older page from the phone, then reloads around it. */
  async function importPage() {
    const generation = request.current;
    beginForeground(generation);
    try {
      const result = await api<{ imported: number; next_cursor: string }>(`${base}/history/import`, "POST", { cursor: importCursor });
      if (generation !== request.current) return false;
      setImportCursor(result.next_cursor === importCursor ? "" : result.next_cursor);
      setImportFinished(!result.next_cursor || result.next_cursor === importCursor);
      const first = messagesRef.current[0];
      const before = first
        ? btoa(JSON.stringify({ at: first.at, id: first.id })).replaceAll("+", "-").replaceAll("/", "_").replaceAll("=", "")
        : "";
      await load(before, true);
      await refreshChanges();
      return result.imported > 0;
    } catch (e) {
      if (generation === request.current)
        setError(e instanceof Error ? e.message.replaceAll("_", " ") : "Could not import phone history");
      return false;
    } finally {
      endForeground(generation);
    }
  }

  /** Older local pages first, then (when allowed) older pages from the phone. */
  async function loadOlder() {
    if (loadingOlder.current || loading) return;
    loadingOlder.current = true;
    try {
      if (cursor) await load(cursor, true);
      else if (loadOlderFromPhone && !importFinished) {
        const imported = await importPage();
        setOlderNotice(imported ? "Loaded older messages from your phone." : "No additional messages are available yet.");
      }
    } finally {
      loadingOlder.current = false;
    }
  }

  /** Ensures a message is loaded, fetching the window around it if needed. */
  async function windowAround(id: string): Promise<Message[] | null> {
    const generation = request.current;
    let next = messagesRef.current;
    if (!next.some((m) => m.id === id)) {
      const page = await api<{ messages: Message[]; next_cursor: string }>(
        `/inbox/conversations/${encodeURIComponent(conversationId)}/messages/${encodeURIComponent(id)}/window`,
      );
      if (generation !== request.current) return null;
      next = merge(next, page.messages);
      setMessages(next);
    }
    return next;
  }

  /** Brings the list up to date now (after a send or an action). */
  function refreshNow() {
    if (usesFeed()) void refreshChanges();
    else void load("", true, request.current, true);
  }

  useEffect(() => {
    setImportCursor("");
    setImportFinished(false);
  }, [conversationId]);
  useEffect(() => {
    request.current++;
    inFlight.current = 0;
    setMessages([]);
    changeCursor.current = null;
    setCursor("");
    setError("");
    void load();
    return () => {
      request.current++;
    };
  }, [conversationId, query, base]);
  // The one poll loop: the change feed when available, else a background
  // snapshot of the newest page. Hidden tabs stop it; returning catches up.
  useEffect(() => {
    const generation = request.current;
    let timer: ReturnType<typeof setInterval> | null = null;
    function poll() {
      if (typeof document !== "undefined" && document.visibilityState === "hidden") return;
      if (usesFeed()) void refreshChanges();
      else void load("", true, generation, true);
    }
    const start = () => { if (!timer) timer = setInterval(poll, 3000); };
    const stop = () => { if (timer) { clearInterval(timer); timer = null; } };
    function onVisibilityChange() {
      if (document.visibilityState === "visible") {
        poll();
        start();
      } else stop();
    }
    if (typeof document !== "undefined") {
      if (document.visibilityState !== "hidden") start();
      document.addEventListener("visibilitychange", onVisibilityChange);
    }
    return () => {
      stop();
      if (typeof document !== "undefined") document.removeEventListener("visibilitychange", onVisibilityChange);
    };
  }, [conversationId, query, base, changeFeed]);

  return {
    messages,
    cursor,
    error,
    setError,
    loading,
    importFinished,
    olderNotice,
    /** Current request generation; responses from older ones are dropped. */
    generation: request,
    load,
    loadOlder,
    windowAround,
    refreshNow,
  };
}
