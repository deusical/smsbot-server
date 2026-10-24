import { useRef, useState, type RefObject } from "react";

const nearBottomPx = 80;

/**
 * The single owner of a thread's scroll position. Message updates call
 * capture() first; after they render, restore() puts the position back: pinned to
 * the bottom, or kept on the same message when older history is prepended.
 * It also tracks whether the reader has scrolled away and whether new
 * messages arrived below them.
 */
export function useThreadScroll(scroll: RefObject<HTMLDivElement | null>) {
  const snapshot = useRef<{ top: number; height: number; bottom: boolean; prepend: boolean } | null>(null);
  const [away, setAway] = useState(false);
  const [hasNew, setHasNew] = useState(false);
  const previousTop = useRef(0);
  const previousPending = useRef(0);

  /** Whether the reader is at the newest message (true before layout). */
  function nearBottom() {
    const el = scroll.current;
    return !el || el.scrollHeight - el.scrollTop - el.clientHeight < nearBottomPx;
  }
  /** Records the position to restore after the next message update. */
  function capture({ prepend, keepBottom }: { prepend: boolean; keepBottom: boolean }) {
    const el = scroll.current;
    snapshot.current = el ? { top: el.scrollTop, height: el.scrollHeight, bottom: keepBottom, prepend } : null;
  }
  /** Restores the captured position; call in a layout effect on message updates. */
  function restore() {
    const el = scroll.current,
      s = snapshot.current;
    if (!el || !s) return;
    if (s.bottom) el.scrollTop = el.scrollHeight;
    else el.scrollTop = s.top + (s.prepend ? el.scrollHeight - s.height : 0);
    snapshot.current = null;
  }
  /** A message the owner just sent scrolls into view; call in a layout effect after restore. */
  function followSend(pendingCount: number) {
    const el = scroll.current;
    if (el && pendingCount > previousPending.current) el.scrollTop = el.scrollHeight;
    previousPending.current = pendingCount;
  }

  /** Scroll handler; returns true when the reader moved up near the top. */
  function onScroll() {
    const el = scroll.current;
    if (!el) return false;
    const fromBottom = el.scrollHeight - el.scrollTop - el.clientHeight;
    if (fromBottom < nearBottomPx) setHasNew(false);
    setAway(fromBottom > 400);
    const upwards = el.scrollTop < previousTop.current;
    previousTop.current = el.scrollTop;
    return upwards && el.scrollTop < 250;
  }
  function jumpToLatest(smooth: boolean) {
    const el = scroll.current;
    if (!el) return;
    if (smooth) el.scrollTo({ top: el.scrollHeight, behavior: "smooth" });
    else el.scrollTop = el.scrollHeight;
    setHasNew(false);
  }
  return {
    away,
    setAway,
    hasNew,
    noteNew: () => setHasNew(true),
    nearBottom,
    capture,
    restore,
    followSend,
    onScroll,
    jumpToLatest,
  };
}

export type ThreadScroll = ReturnType<typeof useThreadScroll>;
