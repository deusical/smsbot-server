import { useEffect, useRef, useState, type RefObject } from "react";
import { api, type Conversation, type Message } from "./api";
import { markSeen } from "./conversationOrder";

/**
 * Marks a conversation read on the phone only when the owner is actually
 * looking at its newest message: tab visible and focused, scrolled to the
 * bottom, and either the phone reports it unread or a message arrived while
 * open. Attempts are spaced, and a result from an older request generation is
 * ignored. It never fetches messages itself.
 */
export function useReadReceipts({
  conversation,
  messages,
  loading,
  away,
  enabled,
  generation,
}: {
  conversation: Conversation;
  messages: Message[];
  loading: boolean;
  away: boolean;
  enabled: boolean;
  generation: RefObject<number>;
}) {
  const [attention, setAttention] = useState(0);
  useEffect(() => {
    const bump = () => setAttention((n) => n + 1);
    window.addEventListener("focus", bump);
    window.addEventListener("blur", bump);
    document.addEventListener("visibilitychange", bump);
    return () => {
      window.removeEventListener("focus", bump);
      window.removeEventListener("blur", bump);
      document.removeEventListener("visibilitychange", bump);
    };
  }, []);
  const baseline = useRef<number | null>(null),
    through = useRef(0),
    tried = useRef(0);
  useEffect(() => {
    baseline.current = null;
    through.current = 0;
    tried.current = 0;
  }, [conversation.id]);
  useEffect(() => {
    if (loading || !messages.length) return;
    let newest = 0,
      newestIncoming = 0;
    for (const m of messages) {
      const at = Date.parse(m.at);
      if (!Number.isFinite(at)) continue;
      newest = Math.max(newest, at);
      if (m.direction === "incoming") newestIncoming = Math.max(newestIncoming, at);
    }
    if (baseline.current === null) baseline.current = newestIncoming;
    if (!enabled || away || !newest || newest <= through.current) return;
    if (document.visibilityState !== "visible" || !document.hasFocus()) return;
    const arrivedWhileOpen = newestIncoming > (baseline.current ?? 0);
    if (!conversation.unread && !arrivedWhileOpen) return;
    if (Date.now() - tried.current < 5000) return;
    tried.current = Date.now();
    const target = newest;
    const started = generation.current;
    void api(`/inbox/conversations/${encodeURIComponent(conversation.id)}/read`, "POST", {
      through: new Date(target).toISOString(),
    })
      .then(() => {
        if (started !== generation.current) return;
        through.current = target;
        markSeen(conversation);
      })
      .catch(() => {
        /* a newer message or a transient failure; the next change retries */
      });
  }, [messages, away, attention, enabled, conversation.unread, conversation.id, loading]);
}
