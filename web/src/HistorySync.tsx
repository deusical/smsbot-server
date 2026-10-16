import {useEffect, useRef, useState} from "react";
import {api, type Conversation} from "./api";
export type Archive = {conversation: Conversation; state: string; imported: number; since: string; last_sync: string; oldest: string; requested_at?: string; request_source?: string; checked?: number; pages?: number; updated?: number; scanned_through?: string; last_progress_at?: string; retry_at?: string; error?: string; recent_error?: string; recent_retry_at?: string; refresh_state?: string; refresh_checked_at?: string; refresh_error?: string};
export function HistorySync({record, ready, onUpdate, onBusy}: {record: Archive; ready: boolean; onUpdate: (record: Archive) => void; onBusy: (busy: boolean) => void}) {
  const [since, setSince] = useState("");
  const [dateEdited, setDateEdited] = useState(false);
  const [busy, setBusy] = useState(false), [error, setError] = useState("");
  const pending = useRef(false);
  useEffect(() => {
    if (dateEdited) return;
    const date = record.since ? new Date(record.since) : null;
    setSince(date ? `${date.getFullYear()}-${String(date.getMonth()+1).padStart(2,"0")}-${String(date.getDate()).padStart(2,"0")}` : "");
  }, [record.since, dateEdited]);
  async function start(cancel = false, limited = false) {
    if (pending.current || (limited && !since)) return;
    pending.current = true; setBusy(true); onBusy(true); setError("");
    try {
      const result = await api<Archive>(`/inbox/conversations/${encodeURIComponent(record.conversation.id)}/sync`, "POST", {
        since: cancel ? record.since : limited ? new Date(`${since}T00:00:00`).toISOString() : "", cancel,
      });
      onUpdate(result); setDateEdited(false);
    } catch { setError("Could not update history import. Check the connection and conversation access."); }
    finally { pending.current = false; setBusy(false); onBusy(false); }
  }
  return <>
    <div className="action-row">
      <button type="button" disabled={busy || !ready} onClick={() => void start()}>Archive all available history</button>
      <button type="button" disabled={busy || !["queued","running","retrying"].includes(record.state)} onClick={() => void start(true)}>Cancel history import</button>
    </div>
    <div className="archive-progress" role="status" aria-label="Import progress">
      <p><strong>Older-history import: {importLabel(record.state)}</strong></p>
      <p>{record.since ? `Limited to messages from ${new Date(record.since).toLocaleDateString()}. Archive all available history to remove this limit.` : "All available history requested."}</p>
      <p>{!ready && ["queued","running","retrying"].includes(record.state) ? "Waiting for a phone connection and enabled conversation access." : record.state === "queued" ? "Waiting for a history worker turn. Explicit imports run ahead of routine inbox checks." : record.state === "running" ? "Fetching history in batches; visible-section checks continue between batches." : record.state === "retrying" ? `History fetch failed. Retrying${record.retry_at ? ` after ${new Date(record.retry_at).toLocaleTimeString()}` : " automatically"}.` : record.state === "cancelled" ? "Import cancelled. Saved messages are kept." : record.state === "cutoff_reached" ? "Requested start date reached." : "All available pages checked. Google may not expose every message."}</p>
      {(record.pages || record.scanned_through || record.last_progress_at || ["queued","running","retrying"].includes(record.state)) ? <>
        <div className="archive-progress-counts"><span><strong>{record.checked ?? 0}</strong> messages checked</span><span><strong>{record.pages ?? 0}</strong> pages</span><span><strong>{record.imported}</strong> newly saved</span><span><strong>{record.updated ?? 0}</strong> updated</span></div>
        <p className="muted">{record.scanned_through ? `Checked through ${new Date(record.scanned_through).toLocaleString()}.` : record.last_progress_at ? "No dated message returned in this scan." : "No older-history page checked in this request yet."}</p>
      </> : <p className="muted">Detailed scan counts are not available for this import.</p>}

      {record.last_progress_at && <p>Last progress {new Date(record.last_progress_at).toLocaleString()}.</p>}
    </div>
    <p role="status" aria-label="Recent message checks">Recent background check: {record.recent_error ? "waiting to retry" : record.last_sync ? `last completed ${new Date(record.last_sync).toLocaleString()}` : "not completed yet"}.</p>
    <p>Viewed-section check: {record.refresh_error ? "waiting to retry" : record.refresh_state?.replaceAll("_"," ") || "not requested"}.{record.refresh_checked_at ? ` Last checked ${new Date(record.refresh_checked_at).toLocaleString()}.` : ""}</p>
    {((record.state !== "cancelled" && record.error) || record.recent_error || record.refresh_error) && <p role="alert">A history check failed; retrying automatically while access remains enabled.</p>}
    <details><summary>Advanced import options</summary>
      <label>History from<input type="date" disabled={busy} aria-label="History from" value={since} onChange={e => {setDateEdited(true); setSince(e.target.value);}} /></label>
      <p>{dateEdited ? "Unsaved date selection. Click Import from selected date to apply." : "Optional: restrict a new import to this start date. Existing saved messages are kept."}</p>
      <button type="button" disabled={busy || !ready || !since} onClick={() => void start(false,true)}>Import from selected date</button>
    </details>
    <p className="muted">Recent checks, viewed-section checks and original-file downloads run separately. Available history may have gaps. Old commands never run.</p>
    {error && <p role="alert">{error}</p>}
  </>;
}
export function importLabel(state: string): string {
  return ({queued:"waiting",running:"fetching",retrying:"waiting to retry",cutoff_reached:"start date reached",available_pages_exhausted:"available pages checked"} as Record<string,string>)[state] || state.replaceAll("_"," ");
}
