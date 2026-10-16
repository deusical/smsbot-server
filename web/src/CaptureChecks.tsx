import type {CaptureGap} from "./api";
export function CaptureChecks({gaps, ready}: {gaps: CaptureGap[]; ready: boolean}) {
 if (!gaps.length) return null;
 return <details className="history-sync capture-checks" open>
  <summary>Capture check - {gaps.some(g => g.state !== "checked_available") ? "needs verification" : "available range rechecked"}</summary>
  {gaps.map(g => <div key={g.conversation} role="status">
   <p>Possible capture gap: {new Date(g.since).toLocaleString()} to {new Date(g.until).toLocaleString()}.</p>
   <p>Reason: {g.reason === "phone_unavailable" ? "the phone was unavailable during live capture" : g.reason === "capture_queue_full" ? "the live capture queue filled up" : g.reason === "capture_delayed" ? "live capture was delayed" : "a phone identity lookup or local save failed"}.</p>
   <p>{g.state === "checked_available" ? `Available history in this range rechecked ${g.checked_at ? new Date(g.checked_at).toLocaleString() : ""}. This does not prove Google returned every message.` : !ready ? "Waiting for phone connection and enabled conversation access; the range will be rechecked automatically." : g.state === "checking" ? "Checking available phone history for this range. Old commands never run." : g.state === "retrying" ? `Could not finish the check. Retrying${g.retry_at ? ` after ${new Date(g.retry_at).toLocaleTimeString()}` : " automatically"}.` : "Waiting for an automatic history repair check. Missing messages will be saved if Google returns them."}</p>
  </div>)}
 </details>;
}
