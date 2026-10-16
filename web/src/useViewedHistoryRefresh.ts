import {useEffect, useRef} from "react";
import {api} from "./api";
export function useViewedHistoryRefresh(id: string, ready: boolean, enabled: boolean, range?: {since: string; until: string}) {
  const automaticPending = useRef(false);
  const latestCheck = useRef<(() => Promise<void>) | null>(null);
  useEffect(() => {
    if (!enabled || !ready || !range) return;
    let disposed = false;
    const check = async () => {
      if (disposed || automaticPending.current || document.visibilityState === "hidden") return;
      automaticPending.current = true;
      try {
        await api<unknown>(`/inbox/conversations/${encodeURIComponent(id)}/refresh`, "POST", range, AbortSignal.timeout(25000));
      } catch {
        // A transient failure retries on the next turn; diagnostics live in Archive.
      } finally {
        automaticPending.current = false;
        if (disposed) void latestCheck.current?.();
      }
    };
    latestCheck.current = check;
    const initial = setTimeout(() => void check(), 500);
    const timer = setInterval(() => void check(), 30000);
    const visible = () => { if (document.visibilityState === "visible") void check(); };
    document.addEventListener("visibilitychange", visible);
    return () => { disposed = true; if (latestCheck.current === check) latestCheck.current = null; clearTimeout(initial); clearInterval(timer); document.removeEventListener("visibilitychange", visible); };
  }, [id, ready, enabled, range?.since, range?.until]);
}
