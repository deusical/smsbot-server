import { useCallback, useEffect, useRef, useState } from "react";
import {
  api,
  ApiError,
  type Conversation,
  type InboxSettings,
  type Job,
  type State,
} from "./api";
import type { Session } from "./useSession";
import { deepEqual } from "./equal";

type ConversationPage = {
  conversations: Conversation[];
  next_cursor: string;
  partial?: boolean;
  stale?: boolean;
};
type ListFetch = { revision: string; ready: boolean; partial: boolean; at: number };
const unfetched: ListFetch = { revision: "", ready: false, partial: false, at: 0 };
const pendingJobStates = new Set(["queued", "generating", "awaiting_approval"]);
const sameJson = deepEqual;

function mergeConversations(previous: Conversation[], next: Conversation[]) {
  const m = new Map(previous.map((c) => [c.id, c]));
  for (const c of next) m.set(c.id, c);
  return [...m.values()];
}

/**
 * Dashboard data and the polling that keeps it current. It owns the request
 * lifetimes: state reads never overlap, the slower details (settings,
 * activity, conversation list) never block readiness or pause updates, hidden
 * tabs stop polling, and a response from an older session is dropped.
 *
 * The conversation list is refetched only when the server reports activity
 * (conversations_revision), when phone readiness changes, when a stale page
 * needs replacing, when a partial page is due for a spaced retry, or once
 * when the app opens or returns to the foreground.
 */
export function useDashboard(session: Session, onExpired: () => void) {
  const [state, setState] = useState<State | null>(null);
  const [jobs, setJobs] = useState<Job[]>([]);
  const [conversations, setConversations] = useState<Conversation[]>([]);
  const [inbox, setInbox] = useState<InboxSettings | null>(null);
  const [conversationCursor, setConversationCursor] = useState("");
  const [discoveryPartial, setDiscoveryPartial] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState("");
  const lastCheckedRef = useRef<Date | null>(null);
  const checkedListeners = useRef(new Set<() => void>());
  const stateRef = useRef<State | null>(null);
  const jobsRef = useRef<Job[]>([]);
  const conversationsRef = useRef<Conversation[]>([]);
  const errorRef = useRef("");
  const updateError = useCallback((next: string) => {
    if (errorRef.current === next) return;
    errorRef.current = next;
    setError(next);
  }, []);
  const markChecked = useCallback((at: Date | null) => {
    const wasLoaded = lastCheckedRef.current !== null;
    lastCheckedRef.current = at;
    if ((at !== null) !== wasLoaded) setLoaded(at !== null);
    checkedListeners.current.forEach((fn) => fn());
  }, []);
  const subscribeChecked = useCallback((fn: () => void) => {
    checkedListeners.current.add(fn);
    return () => {
      checkedListeners.current.delete(fn);
    };
  }, []);
  const getChecked = useCallback(() => lastCheckedRef.current, []);
  const inboxRef = useRef<InboxSettings | null>(null);
  const updateInbox = useCallback((next: InboxSettings | null) => {
    const previous = inboxRef.current;
    if (previous && next && previous.revision > next.revision) return;
    if (previous && next && sameJson(previous, next)) return;
    inboxRef.current = next;
    setInbox(next);
  }, []);
  const paginated = useRef(false);
  const listFetched = useRef<ListFetch>(unfetched);
  const forceList = useRef(true);
  const refreshing = useRef(false);
  const rerunRefresh = useRef(false);
  const refreshingDetails = useRef(false);
  const rerunDetails = useRef<State | null>(null);
  const stateRevision = useRef(0);
  const listEpoch = useRef(0);
  const jobsEpoch = useRef(0);
  const expired = useRef(onExpired);
  expired.current = onExpired;
  const { generation, confirm } = session;

  const refreshDetails = useCallback(
    async (s: State, started: number) => {
      if (refreshingDetails.current) {
        rerunDetails.current = s;
        return;
      }
      refreshingDetails.current = true;
      let stage = "settings";
      try {
        const jobsStamp = jobsEpoch.current;
        const needInbox =
          s.capabilities.includes("full_inbox") &&
          (inboxRef.current === null ||
            s.inbox_revision === undefined ||
            inboxRef.current.revision < s.inbox_revision);
        const [fetchedInbox, activity] = await Promise.all([
          needInbox ? api<InboxSettings>("/inbox/settings") : Promise.resolve(null),
          api<Job[]>("/activity").catch((e: unknown) => {
            stage = "activity";
            throw e;
          }),
        ]);
        if (started !== generation.current) return;
        if (fetchedInbox) updateInbox(fetchedInbox);
        const inboxConfig = inboxRef.current;
        if (jobsStamp === jobsEpoch.current && !sameJson(jobsRef.current, activity)) {
          jobsRef.current = activity;
          setJobs(activity);
        }
        if (rerunDetails.current) return;
        if (s.transport_ready || s.capabilities.includes("archive_history")) {
          stage = "conversations";
          const force = forceList.current;
          const fetched = listFetched.current;
          const fresh =
            !force &&
            !!s.conversations_revision &&
            fetched.revision === s.conversations_revision &&
            // A list read while the phone was unavailable holds archived rows only.
            fetched.ready === s.transport_ready &&
            // A partial list is retried, spaced so a failing lookup cannot keep waking the phone.
            !(fetched.partial && Date.now() - fetched.at >= 30000);
          if (!fresh && (inboxConfig?.enabled || s.capabilities.includes("archive_history"))) {
            // A return to the app during this read keeps its own forced read.
            forceList.current = false;
            const listStamp = listEpoch.current;
            const discovered = await api<ConversationPage>(
              force ? "/inbox/conversations?refresh=1" : "/inbox/conversations",
            ).catch((e: unknown) => {
              if (force) forceList.current = true;
              throw e;
            });
            if (started !== generation.current) return;
            if (listStamp === listEpoch.current) {
              if (!paginated.current) {
                setConversationCursor((prev) =>
                  prev === discovered.next_cursor ? prev : discovered.next_cursor,
                );
              }
              const merged = mergeConversations(conversationsRef.current, discovered.conversations);
              if (!sameJson(conversationsRef.current, merged)) {
                conversationsRef.current = merged;
                setConversations(merged);
              }
              const partial = !!discovered.partial;
              setDiscoveryPartial((prev) => (prev === partial ? prev : partial));
              // A stale page predates recent activity; ask again on the next poll.
              listFetched.current = {
                revision: discovered.stale ? "" : (s.conversations_revision ?? ""),
                ready: s.transport_ready,
                partial,
                at: Date.now(),
              };
            } else {
              // A local toggle changed conversations while this read was in flight;
              // keep the newer local state and let the toggle's refresh re-read.
              listFetched.current = unfetched;
            }
          }
          updateError("");
        } else updateError("Your phone isn’t connected. Conversations may be out of date.");
        markChecked(new Date());
      } catch (e) {
        if (started !== generation.current) return;
        if (e instanceof ApiError && e.status === 401) expired.current();
        else if (stage === "conversations")
          updateError("Could not refresh conversations. The list may be out of date.");
        else if (e instanceof ApiError) updateError("Could not refresh from the server. Retrying…");
        else updateError("Connection lost. Retrying…");
      } finally {
        refreshingDetails.current = false;
        const next = rerunDetails.current;
        rerunDetails.current = null;
        if (next && started === generation.current) void refreshDetails(next, started);
      }
    },
    [generation],
  );

  /** Reads state; on success confirms the session and refreshes details. */
  const refresh = useCallback(async () => {
    if (refreshing.current) {
      rerunRefresh.current = true;
      return;
    }
    refreshing.current = true;
    const started = generation.current;
    try {
      const revision = stateRevision.current;
      const s = await api<State>("/state");
      if (started !== generation.current) return;
      confirm();
      if (revision !== stateRevision.current) return;
      if (!stateRef.current || !sameJson(stateRef.current, s)) {
        stateRef.current = s;
        setState(s);
      }
      // Startup discovery can be slow. It must not hold login busy or stop
      // readiness/pause polling while the phone is connecting.
      void refreshDetails(s, started);
    } catch (e) {
      if (started !== generation.current) return;
      if (e instanceof ApiError && e.status === 401) expired.current();
      else updateError("Could not refresh from the server. Retrying…");
    } finally {
      refreshing.current = false;
      if (rerunRefresh.current && started === generation.current) {
        rerunRefresh.current = false;
        void refresh();
      }
    }
  }, [generation, confirm, refreshDetails]);

  useEffect(() => {
    void refresh();
    // A backgrounded phone app shouldn't poll; catch up as soon as it returns.
    const timer = setInterval(() => {
      if (document.visibilityState !== "hidden") void refresh();
    }, 3000);
    const visible = () => {
      if (document.visibilityState !== "visible") return;
      forceList.current = true;
      void refresh();
    };
    document.addEventListener("visibilitychange", visible);
    return () => {
      clearInterval(timer);
      document.removeEventListener("visibilitychange", visible);
    };
  }, [refresh]);

  /** Forgets the fetched list so the next refresh rereads it. */
  const invalidateList = useCallback(() => {
    listEpoch.current++;
    listFetched.current = unfetched;
  }, []);

  /** Applies a confirmed conversation change at once and schedules a quiet catch-up read. */
  const patchConversation = useCallback(
    (id: string, patch: Partial<Conversation>) => {
      listEpoch.current++;
      listFetched.current = unfetched;
      const next = conversationsRef.current.map((c) =>
        c.id === id ? { ...c, ...patch } : c,
      );
      conversationsRef.current = next;
      setConversations(next);
      void refresh();
    },
    [refresh],
  );

  /** Applies a confirmed job state change at once and schedules a quiet catch-up read. */
  const patchJob = useCallback(
    (id: string, patch: Partial<Job>) => {
      jobsEpoch.current++;
      const next = jobsRef.current.map((j) =>
        j.id === id && pendingJobStates.has(j.state) ? { ...j, ...patch } : j,
      );
      jobsRef.current = next;
      setJobs(next);
      void refresh();
    },
    [refresh],
  );

  const approveJob = useCallback(
    async (id: string) => {
      updateError("");
      try {
        await api(`/jobs/${encodeURIComponent(id)}/approve`, "POST");
        patchJob(id, { state: "queued" });
      } catch (e) {
        updateError(
          e instanceof ApiError
            ? e.message.replaceAll("_", " ")
            : "Could not reach the server.",
        );
        void refresh();
      }
    },
    [patchJob, refresh, updateError],
  );

  const cancelJob = useCallback(
    async (id: string) => {
      updateError("");
      try {
        await api(`/jobs/${encodeURIComponent(id)}/cancel`, "POST");
        patchJob(id, { state: "cancelled", code: "owner_cancelled" });
      } catch (e) {
        updateError(
          e instanceof ApiError
            ? e.message.replaceAll("_", " ")
            : "Could not reach the server.",
        );
        void refresh();
      }
    },
    [patchJob, refresh, updateError],
  );

  /** Clears all session data after sign-out or expiry. */
  const reset = useCallback(() => {
    listEpoch.current++;
    jobsEpoch.current++;
    listFetched.current = unfetched;
    forceList.current = true;
    inboxRef.current = null;
    stateRef.current = null;
    jobsRef.current = [];
    conversationsRef.current = [];
    errorRef.current = "";
    rerunDetails.current = null;
    rerunRefresh.current = false;
    paginated.current = false;
    markChecked(null);
    setState(null);
    setJobs([]);
    setConversations([]);
    setInbox(null);
    setDiscoveryPartial(false);
    setError("");
    setConversationCursor("");
  }, [markChecked]);

  /** Sets Live/Pause; an older state read cannot overwrite the result. */
  const setPaused = useCallback(
    async (paused: boolean) => {
      const started = generation.current;
      stateRevision.current++;
      const updated = await api<State>("/pause", "POST", { paused });
      if (started !== generation.current) return;
      stateRevision.current++;
      stateRef.current = updated;
      setState(updated);
      if (paused) {
        jobsEpoch.current++;
        const next = jobsRef.current.map((j) =>
          pendingJobStates.has(j.state)
            ? { ...j, state: "cancelled", code: "paused" }
            : j,
        );
        jobsRef.current = next;
        setJobs(next);
        void refresh();
      }
    },
    [generation, refresh],
  );

  /** Appends the next page of conversations. */
  const loadMoreConversations = useCallback(async () => {
    const started = generation.current;
    const listStamp = listEpoch.current;
    const p = await api<ConversationPage>(
      `/inbox/conversations?cursor=${encodeURIComponent(conversationCursor)}`,
    );
    if (started !== generation.current) return;
    const old = conversationsRef.current;
    const next =
      listStamp === listEpoch.current
        ? mergeConversations(old, p.conversations)
        : (() => {
            const existing = new Set(old.map((c) => c.id));
            return [...old, ...p.conversations.filter((c) => !existing.has(c.id))];
          })();
    conversationsRef.current = next;
    setConversations(next);
    setDiscoveryPartial(!!p.partial);
    paginated.current = true;
    setConversationCursor(p.next_cursor === conversationCursor ? "" : p.next_cursor);
  }, [conversationCursor, generation]);

  return {
    state,
    jobs,
    conversations,
    inbox,
    setInbox: updateInbox,
    conversationCursor,
    discoveryPartial,
    loaded,
    subscribeChecked,
    getChecked,
    error,
    setError: updateError,
    refresh,
    invalidateList,
    patchConversation,
    approveJob,
    cancelJob,
    reset,
    setPaused,
    loadMoreConversations,
  };
}
