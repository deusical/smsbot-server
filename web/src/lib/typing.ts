import { useEffect, useState } from "react";
import { api } from "./api";

const none: ReadonlySet<string> = new Set();

// Conversations whose other participant is typing right now. The server keeps
// this in memory only and it expires on its own, so a short poll while the tab
// is visible is enough. Nothing is stored in the browser.
export function useTyping(enabled: boolean): ReadonlySet<string> {
  const [ids, setIds] = useState<ReadonlySet<string>>(none);
  useEffect(() => {
    if (!enabled) {
      setIds(none);
      return;
    }
    let stopped = false;
    let busy = false;
    const poll = async () => {
      if (busy || document.visibilityState === "hidden") return;
      busy = true;
      try {
        const result = await api<{ conversations: string[] }>("/inbox/typing");
        if (stopped) return;
        setIds((previous) =>
          previous.size === result.conversations.length &&
          result.conversations.every((id) => previous.has(id))
            ? previous
            : new Set(result.conversations),
        );
      } catch {
        if (!stopped) setIds(none);
      } finally {
        busy = false;
      }
    };
    void poll();
    const timer = setInterval(() => void poll(), 2000);
    const onVisible = () => {
      if (document.visibilityState === "visible") void poll();
    };
    document.addEventListener("visibilitychange", onVisible);
    return () => {
      stopped = true;
      clearInterval(timer);
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, [enabled]);
  return ids;
}
