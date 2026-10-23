import {useEffect, useRef, useState} from "react";
import {api, type Conversation} from "../lib/api";
import {Field, Notice, StatusPill, type Tone} from "./ui";
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
  const inFlight = ["queued","running","retrying"].includes(record.state);
  const finishedScan = ["available_pages_exhausted","cutoff_reached"].includes(record.state);
  const detail = record.state === "not_started" ? "Not archived yet. Select Archive conversation to start saving available history." : !ready && inFlight ? "Waiting for a phone connection and enabled conversation access." : record.state === "queued" ? "Waiting for a history worker turn. Explicit imports run ahead of routine inbox checks." : record.state === "running" ? "Fetching history in batches; visible-section checks continue between batches." : record.state === "retrying" ? `History fetch failed. Retrying${record.retry_at ? ` after ${new Date(record.retry_at).toLocaleTimeString()}` : " automatically"}.` : record.state === "cancelled" ? "Import cancelled. Saved messages are kept." : record.state === "cutoff_reached" ? "Requested start date reached." : "All available pages checked. Google may not expose every message.";
  const hasCounts = record.pages || record.scanned_through || record.last_progress_at || inFlight;
  return <>
    <div className="archive-actions">
      <button type="button" className="primary" disabled={busy || !ready} onClick={() => void start()}>Archive conversation</button>
      <button type="button" disabled={busy || !inFlight} onClick={() => void start(true)}>Cancel history import</button>
    </div>
    <p className="muted">Save this conversation locally. Archive again to check all available history, add missing messages and reconcile edits. Missing original files download in the background under your file archive policy. Existing saved data is kept.</p>
    <div className="archive-progress" role="status" aria-label="Import progress">
      <div className="archive-progress-head">
        <strong>Older-history import</strong>
        <StatusPill tone={stateTone(record.state)}>{importLabel(record.state)}</StatusPill>
      </div>
      {(inFlight || finishedScan) && <div className={"progress-bar" + (inFlight ? " indeterminate" : "")} aria-hidden="true"><span /></div>}
      <p>{record.since ? `Limited to messages from ${new Date(record.since).toLocaleDateString()}. Use Archive conversation to remove this limit.` : record.state === "not_started" ? "No history scan requested yet." : "All available history requested."}</p>
      <p>{detail}</p>
      {hasCounts ? <>
        <div className="metric-strip archive-progress-counts"><div className="metric"><strong>{record.checked ?? 0}</strong>{" "}<span>messages checked</span></div><div className="metric"><strong>{record.pages ?? 0}</strong>{" "}<span>pages</span></div><div className="metric"><strong>{record.imported}</strong>{" "}<span>newly saved</span></div><div className="metric"><strong>{record.updated ?? 0}</strong>{" "}<span>updated</span></div></div>
        <p className="muted">{record.scanned_through ? `Checked through ${new Date(record.scanned_through).toLocaleString()}.` : record.last_progress_at ? "No dated message returned in this scan." : "No older-history page checked in this request yet."}</p>
      </> : record.state !== "not_started" && <p className="muted">Detailed scan counts are not available for this import.</p>}
      {record.last_progress_at && <p className="muted">Last progress {new Date(record.last_progress_at).toLocaleString()}.</p>}
    </div>
    <dl className="kv-list">
      <div><dt>Recent background check</dt><dd role="status" aria-label="Recent message checks">{record.recent_error ? "waiting to retry" : record.last_sync ? `last completed ${new Date(record.last_sync).toLocaleString()}` : "not completed yet"}</dd></div>
      <div><dt>Viewed-section check</dt><dd>{record.refresh_error ? "waiting to retry" : record.refresh_state?.replaceAll("_"," ") || "not requested"}{record.refresh_checked_at ? `. Last checked ${new Date(record.refresh_checked_at).toLocaleString()}` : ""}</dd></div>
    </dl>
    {((record.state !== "cancelled" && record.error) || record.recent_error || record.refresh_error) && <Notice tone="bad" role="alert">A history check failed; retrying automatically while access remains enabled.</Notice>}
    <details><summary>Advanced import options</summary>
      <div className="details-body">
        <Field label="History from" hint={dateEdited ? "Unsaved date selection. Click Import from selected date to apply." : "Optional: restrict a new import to this start date. Existing saved messages are kept."}><input type="date" disabled={busy} aria-label="History from" value={since} onChange={e => {setDateEdited(true); setSince(e.target.value);}} /></Field>
        <div><button type="button" disabled={busy || !ready || !since} onClick={() => void start(false,true)}>Import from selected date</button></div>
      </div>
    </details>
    <p className="muted hint-line">Recent checks, viewed-section checks and original-file downloads run separately. Available history may have gaps. Old commands never run.</p>
    {error && <Notice tone="bad" role="alert">{error}</Notice>}
  </>;
}
export function stateTone(state: string): Tone {
  return state === "running" ? "busy" : state === "queued" || state === "retrying" ? "wait" : state === "available_pages_exhausted" || state === "cutoff_reached" ? "ok" : "idle";
}
export function importLabel(state: string): string {
  return ({queued:"waiting",running:"fetching",retrying:"waiting to retry",cutoff_reached:"start date reached",not_started:"not archived yet",available_pages_exhausted:"available pages checked"} as Record<string,string>)[state] || state.replaceAll("_"," ");
}
