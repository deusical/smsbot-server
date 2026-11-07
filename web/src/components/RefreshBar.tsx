import { useEffect, useState } from "react";
import { Icon } from "./Icon";

/** "just now", "3 min ago", "2 h ago" for a time in ms since the epoch. */
export function ago(at: number, now = Date.now()): string {
  const seconds = Math.max(0, (now - at) / 1000);
  if (seconds < 45) return "just now";
  if (seconds < 3600) return `${Math.round(seconds / 60)} min ago`;
  if (seconds < 86400) return `${Math.round(seconds / 3600)} h ago`;
  return new Date(at).toLocaleDateString([], { month: "short", day: "numeric" });
}

/**
 * Says how old the data on screen is, offers a Refresh button, and, if the last
 * refresh failed, says so while the page keeps showing what it has.
 */
export function RefreshBar({
  updatedAt,
  refreshing,
  error,
  onRefresh,
}: {
  updatedAt: number | null;
  refreshing: boolean;
  error?: string;
  onRefresh: () => void;
}) {
  const [, tick] = useState(0);
  useEffect(() => {
    const id = setInterval(() => tick((n) => n + 1), 30000);
    return () => clearInterval(id);
  }, []);
  return (
    <div className="refresh-bar">
      <span className="muted" aria-live="polite">
        {refreshing
          ? "Updating…"
          : error
            ? `Couldn’t update. Showing ${updatedAt ? `what was loaded ${ago(updatedAt)}` : "nothing new"}.`
            : updatedAt
              ? `Updated ${ago(updatedAt)}`
              : ""}
      </span>
      <button type="button" className="small ghost" disabled={refreshing} onClick={onRefresh}>
        <Icon name="refresh" size={15} />
        Refresh
      </button>
    </div>
  );
}
