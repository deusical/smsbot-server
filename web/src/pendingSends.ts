import { useEffect, useSyncExternalStore } from "react";
import {clearReactionAttempts} from "./pendingActions";
import { api, type Job } from "./api";

// Messages the owner has sent from this dashboard that the server has not yet
// confirmed as saved history. They live in memory only (never browser storage),
// survive moving between conversations, and clear on sign-out.
export type Pending = {
 replyTo?: string;
 quote?: import("./api").Quote;
  // Idempotency key. Retrying a lost response always reuses it.
  key: string;
  text: string;
  files: File[];
  at: string;
  // API route chosen at first attempt; a retry must keep it.
  route: "inbox" | "legacy" | null;
  // posting: request in flight. queued: server accepted, waiting for the phone.
  // sent: submitted, waiting for it to appear in history. failed: needs the owner.
  phase: "posting" | "queued" | "sent" | "failed";
  job?: string;
  transaction?: string;
  sentAt?: number;
  queuedAt?: number;
  failure?: "unconfirmed" | "not_sent" | "uncertain";
  error?: string;
};

const none: Pending[] = [];
let all: Record<string, Pending[]> = {};
const listeners = new Set<() => void>();
const emit = () => listeners.forEach((listener) => listener());
const subscribe = (listener: () => void) => {
  listeners.add(listener);
  return () => void listeners.delete(listener);
};

export function usePending(conversation: string): Pending[] {
  return useSyncExternalStore(subscribe, () => all[conversation] ?? none);
}

export function addPending(conversation: string, item: Pending) {
  all = { ...all, [conversation]: [...(all[conversation] ?? none), item] };
  emit();
}

export function patchPending(
  conversation: string,
  key: string,
  patch: Partial<Pending>,
) {
  const list = all[conversation];
  if (!list?.some((p) => p.key === key)) return;
  all = {
    ...all,
    [conversation]: list.map((p) => (p.key === key ? { ...p, ...patch } : p)),
  };
  emit();
}

export function removePending(conversation: string, key: string) {
  const list = all[conversation];
  if (!list?.some((p) => p.key === key)) return;
  const rest = list.filter((p) => p.key !== key);
  const next = { ...all };
  if (rest.length) next[conversation] = rest;
  else delete next[conversation];
  all = next;
  emit();
}

export function getPending(conversation: string, key: string) {
  return all[conversation]?.find((p) => p.key === key);
}

export function clearPending() {
 clearReactionAttempts();
  all = {};
  emit();
}

// Sends are posted one at a time so messages keep the order they were typed in.
let chain: Promise<void> = Promise.resolve();
export function enqueue(task: () => Promise<void>) {
  chain = chain.then(task, task);
  return chain;
}

// Bumped whenever a pending send reaches the phone, so an open thread refreshes at once.
let signal = 0;
export function useSubmittedSignal(): number {
  return useSyncExternalStore(subscribe, () => signal);
}

const queuedGraceMs = 30000;
const sentGraceMs = 15000;

// Follows accepted sends through queued -> submitted once a second, from the app
// shell so tracking continues while the owner views another conversation or page.
export function usePendingFollower(active: boolean) {
  const live = useSyncExternalStore(subscribe, () =>
    Object.values(all).some((list) => list.some((p) => p.phase === "queued" || p.phase === "sent")),
  );
  useEffect(() => {
    if (!active || !live) return;
    let busy = false;
    const tick = async () => {
      if (busy || document.visibilityState === "hidden") return;
      busy = true;
      try {
        // Only the newest 200 jobs are listed; a missing job is handled by the grace period below.
        const jobs = await api<Job[]>("/activity");
        let submitted = false;
        for (const [conversation, list] of Object.entries(all)) {
          for (const p of list) {
            if (p.phase === "sent") {
              if (Date.now() - (p.sentAt ?? 0) > sentGraceMs) removePending(conversation, p.key);
              continue;
            }
            if (p.phase !== "queued") continue;
            const job = p.job ? jobs.find((j) => j.id === p.job) : undefined;
            if (!job) {
              if (Date.now() - (p.queuedAt ?? 0) > queuedGraceMs)
                patchPending(conversation, p.key, {
                  phase: "failed",
                  failure: "uncertain",
                  error: "Not confirmed. Check Activity or your phone before sending this again.",
                });
              continue;
            }
            if (["submitted", "delivered", "read"].includes(job.state)) {
              patchPending(conversation, p.key, { phase: "sent", transaction: job.transaction_id, sentAt: Date.now() });
              submitted = true;
            } else if (job.state === "unknown_submission") {
              patchPending(conversation, p.key, { phase: "failed", failure: "uncertain", error: "Not confirmed. Check your phone before sending this again." });
            } else if (job.state === "failed" || job.state === "cancelled") {
              patchPending(conversation, p.key, {
                phase: "failed",
                failure: "not_sent",
                error: job.state === "cancelled" ? "Not sent. It was cancelled before reaching your phone." : `Not sent${job.code ? `: ${job.code.replaceAll("_", " ")}` : ""}.`,
              });
            }
          }
        }
        if (submitted) {
          signal++;
          emit();
        }
      } catch {
        /* the next tick retries */
      } finally {
        busy = false;
      }
    };
    void tick();
    const timer = setInterval(() => void tick(), 1000);
    return () => clearInterval(timer);
  }, [active, live]);
}
