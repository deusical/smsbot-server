import { useEffect, useRef, useState } from "react";
import { api } from "./api";
import { Icon } from "./Icon";
import { useInfoPanel } from "./infoPanels";

type Archive = { conversation: { id: string }; state: string; imported: number; since: string; last_sync: string; oldest: string; requested_at?: string; request_source?: string; checked?: number; pages?: number; updated?: number; scanned_through?: string; last_progress_at?: string; retry_at?: string; error?: string; recent_error?: string; recent_retry_at?: string; refresh_state?: string; refresh_checked_at?: string; refresh_error?: string; refresh_since?: string; refresh_until?: string };
export function HistorySync({ id, ready, range, automatic = false }: { id: string; ready: boolean; range?: {since: string; until: string}; automatic?: boolean }) {
  const [shown, setShown] = useInfoPanel("sync-details");
  const [record, setRecord] = useState<Archive | null>(null);
  const [since, setSince] = useState("");
  const [dateEdited, setDateEdited] = useState(false);
  const operation = useRef(0);
  useEffect(() => {
    if (dateEdited || !record) return;
    const date = record.since ? new Date(record.since) : null;
    setSince(date ? `${date.getFullYear()}-${String(date.getMonth()+1).padStart(2,"0")}-${String(date.getDate()).padStart(2,"0")}` : "");
  }, [record?.since, dateEdited]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [automaticError, setAutomaticError] = useState("");
  const pending = useRef(false);
  const automaticPending = useRef(false);
  const latestCheck = useRef<(() => Promise<void>) | null>(null);
  useEffect(() => {
    let disposed = false, reading = false;
    const refresh = async () => {
      if (disposed || reading || document.visibilityState === "hidden") return;
      reading = true;
      const version = operation.current;
      try {
        const rows = await api<Archive[]>("/inbox/archive");
        if (!disposed && version === operation.current) setRecord(rows.find((a) => a.conversation.id === id) ?? null);
      } catch { /* Main dashboard reports connectivity; preserve job state. */ }
      finally { reading = false; }
    };
    void refresh();
    const timer = setInterval(() => void refresh(), 3000);
    return () => { disposed = true; clearInterval(timer); };
  }, [id]);
  useEffect(() => {
    if (!automatic || !ready || !range) return;
    let disposed = false;
    const check = async () => {
      if (disposed || automaticPending.current || document.visibilityState === "hidden") return;
      automaticPending.current = true;
      const version = operation.current;
      try {
        const result = await api<Archive>(`/inbox/conversations/${encodeURIComponent(id)}/refresh`, "POST", range, AbortSignal.timeout(25000));
        if (!disposed && version === operation.current) { setRecord(result); setAutomaticError(""); }
      } catch {
        if (!disposed) setAutomaticError("Could not check this section. Saved messages remain available; retrying automatically.");
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
  }, [id, ready, automatic, range?.since, range?.until]);
  async function start(cancel = false) {
    if (pending.current) return;
    pending.current = true; operation.current++; setBusy(true); setError("");
    try {
      const result = await api<Archive>(`/inbox/conversations/${encodeURIComponent(id)}/sync`, "POST", {
        since: since ? new Date(`${since}T00:00:00`).toISOString() : "", cancel,
      });
      setRecord(result); setDateEdited(false);
    } catch { setError("Could not update history sync. Check the connection and conversation access."); }
    finally { operation.current++; pending.current = false; setBusy(false); }
  }
  const matchesView = !range || (!!record && Date.parse(record.refresh_since ?? "") <= Date.parse(range.since) && (!range.until || Date.parse(record.refresh_until ?? "") >= Date.parse(range.until)));
  const viewRecord = matchesView ? record : null;
  const problem = error || automaticError ||
    (viewRecord?.refresh_error ? "Checking this section failed; retrying automatically." : "") ||
    (record?.error || record?.recent_error || record?.state === "retrying" ? "A history check failed; retrying automatically while access remains enabled." : "");
  return <div className="sync-status">
    <div className="sync-info-toolbar">
      <button type="button" className="ghost" aria-expanded={shown} onClick={() => setShown(!shown)}>{shown ? "Sync details" : "Show sync details"}</button>
      {!shown && <small role="status">{problem ? "Needs attention" : !ready ? "Waiting for connection or access" : record ? `Import ${importLabel(record.state)}` : "Checking messages"}</small>}
      {shown && <button type="button" className="icon ghost notice-close" aria-label="Hide sync details" title="Hide these details; syncing continues" onClick={() => setShown(false)}><Icon name="x" size={16} /></button>}
    </div>
    {!shown && problem && <p role="alert" className="inline-alert">{problem}<button type="button" className="ghost" onClick={() => setShown(true)}>Review sync issue</button></p>}
    <div hidden={!shown}>
    {automatic && <p className="inline-note" role="status">{
      !ready ? "Showing saved messages. Phone updates are waiting for a connection and enabled conversation access." :
      automaticError ? automaticError :
      viewRecord?.refresh_error ? "Showing saved messages. Checking this section failed; retrying automatically." :
      viewRecord?.refresh_state === "queued" || viewRecord?.refresh_state === "running" || viewRecord?.refresh_state === "retrying" ? "Showing saved messages. Checking this section with your phone; missing originals have priority." :
      viewRecord?.refresh_checked_at ? `Available messages in this section checked ${new Date(viewRecord.refresh_checked_at).toLocaleTimeString()}. Originals may still be downloading.` :
      "Showing saved messages. Checking this section with your phone..."
    }</p>}
    {record && <p className="inline-note" role="status" aria-label="Recent message checks">Recent background check: {record.recent_error ? `waiting to retry${record.recent_retry_at ? ` after ${new Date(record.recent_retry_at).toLocaleTimeString()}` : ""}` : record.last_sync ? `last completed ${new Date(record.last_sync).toLocaleString()}` : "not completed yet"}. This runs separately from the older-history import.</p>}
    <details className="history-sync">
    <summary>Older-history import{record ? ` - ${importLabel(record.state)}` : ""}</summary>
    <p>Saved history stays on this server. This import is separate from visible-section checks and continues with this tab closed. Old commands never run.</p>
    <label>History from <input type="date" aria-label="History from" value={since} onChange={(e) => { setDateEdited(true); setSince(e.target.value); }} /></label>
    <p>{dateEdited ? "Unsaved date selection. Click Sync available history to apply." : "Date reflects the saved request. Leave blank to request all available history."}</p>
    <button type="button" disabled={busy || !ready} onClick={() => void start()}>Sync available history</button>
    <button type="button" disabled={busy || !record} onClick={() => void start(true)}>Cancel history import</button>
    {record && <div role="status" aria-label="Import progress">
      <p>{(record.requested_at || record.request_source === "owner" || (record.since && !record.request_source)) ? "Requested by you. " : "Started automatically by background archiving. "}{record.since ? `Requested from ${new Date(record.since).toLocaleDateString()}.` : "All available history requested."}</p>
      <p>{!ready ? "Waiting for a phone connection and enabled conversation access." : record.state === "queued" ? "Waiting for a history worker turn. Explicit imports run ahead of routine inbox checks." : record.state === "running" ? "Fetching history in batches; visible-section checks continue between batches." : record.state === "retrying" ? `History fetch failed. Retrying${record.retry_at ? ` after ${new Date(record.retry_at).toLocaleTimeString()}` : " automatically"}.` : record.state === "cancelled" ? "Import cancelled. Saved messages are kept." : record.state === "cutoff_reached" ? "Requested start date reached." : "All available pages checked. Google may not expose every message."}</p>
      <p>{record.checked ?? 0} messages checked across {record.pages ?? 0} pages; {record.imported} newly saved; {record.updated ?? 0} updated. {record.scanned_through ? `Checked through ${new Date(record.scanned_through).toLocaleString()}. ` : "No older-history page checked in this request yet. "}{record.last_progress_at ? `Last progress ${new Date(record.last_progress_at).toLocaleTimeString()}.` : ""}</p>
      <p>Visible-section checks and recent reconciliation have separate progress. Attachment originals download separately. Available history may have gaps.</p>
    </div>}

    {error && <p role="alert">{error}</p>}
  </details></div></div>;
}

function importLabel(state: string): string {
  switch (state) {
    case "queued": return "waiting";
    case "running": return "fetching";
    case "retrying": return "waiting to retry";
    case "cutoff_reached": return "start date reached";
    case "available_pages_exhausted": return "available pages checked";
    default: return state.replaceAll("_", " ");
  }
}
