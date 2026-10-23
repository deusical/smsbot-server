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
    .map((c, index) => ({ c, index }))
    .sort(
      (a, b) =>
        Number(!!b.c.pinned) - Number(!!a.c.pinned) ||
        stamp(b.c.last_message_at) - stamp(a.c.last_message_at) ||
        a.index - b.index,
    )
    .map((entry) => entry.c);
}

export function formatListTime(iso?: string, now = new Date()): string {
  const at = stamp(iso);
  if (!at) return "";
  const date = new Date(at);
  const startOfDay = (d: Date) =>
    new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
  const days = Math.round((startOfDay(now) - startOfDay(date)) / 86400000);
  if (days <= 0)
    return date.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
  if (days === 1) return "Yesterday";
  if (days < 7) return date.toLocaleDateString([], { weekday: "short" });
  return date.toLocaleDateString(
    [],
    date.getFullYear() === now.getFullYear()
      ? { month: "short", day: "numeric" }
      : { month: "short", day: "numeric", year: "numeric" },
  );
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
