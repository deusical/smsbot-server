import { useSyncExternalStore } from "react";
import type { Conversation } from "./api";

function stamp(value?: string): number {
  const parsed = value ? Date.parse(value) : NaN;
  return Number.isFinite(parsed) ? parsed : 0;
}

// Pinned conversations first, then the most recent message, like Messages.
// The original index breaks ties so equal or missing times keep a stable order.
export function sortConversations<T extends Conversation>(list: T[]): T[] {
  return list
    .map((c, index) => ({ c, index, at: stamp(c.last_message_at) }))
    .sort(
      (a, b) =>
        Number(!!b.c.pinned) - Number(!!a.c.pinned) ||
        b.at - a.at ||
        a.index - b.index,
    )
    .map((entry) => entry.c);
}

function formatRelativeDayTime(at: number, dayStartMs: number, year: number): string {
  const date = new Date(at);
  const dateDayStart = new Date(date.getFullYear(), date.getMonth(), date.getDate()).getTime();
  const days = Math.round((dayStartMs - dateDayStart) / 86400000);
  if (days <= 0)
    return date.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
  if (days === 1) return "Yesterday";
  if (days < 7) return date.toLocaleDateString([], { weekday: "short" });
  return date.toLocaleDateString(
    [],
    date.getFullYear() === year
      ? { month: "short", day: "numeric" }
      : { month: "short", day: "numeric", year: "numeric" },
  );
}

let cachedDayStart = 0;
let cachedDayEnd = 0;
let cachedYear = 0;
let cachedTzOffset = 0;
const listTimeCache = new Map<string, string>();

export function formatListTime(iso?: string, now?: Date): string {
  if (!iso) return "";
  if (now) {
    const at = stamp(iso);
    if (!at) return "";
    const refStart = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime();
    return formatRelativeDayTime(at, refStart, now.getFullYear());
  }
  const currentMs = Date.now();
  const today = new Date(currentMs);
  const tz = today.getTimezoneOffset();
  if (currentMs < cachedDayStart || currentMs >= cachedDayEnd || tz !== cachedTzOffset) {
    cachedYear = today.getFullYear();
    cachedTzOffset = tz;
    cachedDayStart = new Date(cachedYear, today.getMonth(), today.getDate()).getTime();
    cachedDayEnd = new Date(cachedYear, today.getMonth(), today.getDate() + 1).getTime();
    listTimeCache.clear();
  } else if (listTimeCache.size > 2000) {
    listTimeCache.clear();
  }
  const hit = listTimeCache.get(iso);
  if (hit !== undefined) return hit;
  const at = stamp(iso);
  if (!at) return "";
  const formatted = formatRelativeDayTime(at, cachedDayStart, cachedYear);
  listTimeCache.set(iso, formatted);
  return formatted;
}

type Seen = Record<string, number>;

// Seen state lives in memory only, like the owner credential: nothing about
// which conversations were opened is written to browser storage, and sign-out
// clears it. After a reload the phone's own unread flag applies again.
let seen: Seen = {};
const listeners = new Set<() => void>();
const emit = () => listeners.forEach((listener) => listener());
const subscribe = (listener: () => void) => {
  listeners.add(listener);
  return () => void listeners.delete(listener);
};

export function clearSeen() {
  seen = {};
  emit();
}

export function markSeen(c?: Conversation) {
  if (!c) return;
  const at = stamp(c.last_message_at) || Date.now();
  if ((seen[c.id] ?? 0) >= at) return;
  seen = { ...seen, [c.id]: at };
  emit();
}

export function useSeen() {
  const current = useSyncExternalStore(subscribe, () => seen);
  const isUnread = (c: Conversation) =>
    !!c.unread && stamp(c.last_message_at) > (current[c.id] ?? 0);
  return { markSeen, isUnread };
}
