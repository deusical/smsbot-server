import type {CaptureGap} from "../lib/api";
export function CaptureChecks({gaps, ready}: {gaps: CaptureGap[]; ready: boolean}) {
 if (!gaps.length) return null;
 return <div className="capture-checks">{gaps.map(g => <div key={g.conversation}>
  <p role="status">{g.state === "checked_available" ? "Available history in this range rechecked. Google may not return every message." : g.state === "not_collected" ? "Nothing to repair: collection does not apply to this conversation (a group, read-only, or not selected)." : !ready ? "Waiting for phone connection and enabled conversation access; repair resumes automatically." : g.state === "checking" ? "Checking available phone history for this range. No action needed." : g.state === "retrying" ? "The last check could not finish. Retrying automatically." : "Queued for automatic repair. No action needed."}</p>
  {!!g.pages && <p className="muted">{g.checked ?? 0} messages checked across {g.pages} pages.{g.last_progress_at ? ` Last progress ${new Date(g.last_progress_at).toLocaleTimeString()}.` : ""}</p>}
  <details><summary>Repair details</summary>
   <p>{g.kind === "historical_update" ? "An update to an older message could not be saved. This check targets the surrounding message times." : !g.kind ? "This request was saved by an earlier server version. Its range may be broader than necessary because it combined an old message date with the time the update arrived. The server keeps it pending until available history has been checked." : "This range covers message updates that could not be saved."}</p>
   <p>Reason: {g.reason === "phone_unavailable" ? "the phone was unavailable when a message update arrived" : g.reason === "capture_queue_full" ? "the capture queue filled up" : g.reason === "capture_delayed" ? "capture was delayed" : "a phone identity lookup or local save failed"}.</p>
   <dl><div><dt>Range to recheck:</dt><dd>{new Date(g.since).toLocaleString()} to {new Date(g.until).toLocaleString()}</dd></div>
   {g.observed_at && <div><dt>Update observed</dt><dd>{new Date(g.observed_at).toLocaleString()}</dd></div>}
   {g.checked_at && <div><dt>Last checked</dt><dd>{new Date(g.checked_at).toLocaleString()}</dd></div>}
   {g.retry_at && g.state === "retrying" && <div><dt>Next retry</dt><dd>{new Date(g.retry_at).toLocaleString()}</dd></div>}</dl>
   <p className="muted">This is a verification range, not a confirmed outage or a count of missing messages. Old commands never run.</p>
  </details>
 </div>)}</div>;
}
