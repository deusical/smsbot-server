import { useEffect, useRef, useState } from "react";
import { api } from "./api";

type Archive = { conversation: { id: string }; state: string; imported: number; since: string; last_sync: string; oldest: string; error?: string; recent_error?: string };
export function HistorySync({ id, ready }: { id: string; ready: boolean }) {
  const [record, setRecord] = useState<Archive | null>(null);
  const [since, setSince] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const pending = useRef(false);
  useEffect(() => {
    let disposed = false;
    const refresh = async () => {
      try {
        const rows = await api<Archive[]>("/inbox/archive");
        if (!disposed) setRecord(rows.find((a) => a.conversation.id === id) ?? null);
      } catch { /* Main dashboard reports connectivity; preserve job state. */ }
    };
    void refresh();
    const timer = setInterval(() => void refresh(), 3000);
    return () => { disposed = true; clearInterval(timer); };
  }, [id]);
  async function start(cancel = false) {
    if (pending.current) return;
    pending.current = true; setBusy(true); setError("");
    try {
      const result = await api<Archive>(`/inbox/conversations/${encodeURIComponent(id)}/sync`, "POST", {
        since: since ? new Date(`${since}T00:00:00`).toISOString() : "", cancel,
      });
      setRecord(result);
    } catch { setError("Could not update history sync. Check the connection and conversation access."); }
    finally { pending.current = false; setBusy(false); }
  }
  return <details className="history-sync">
    <summary>History sync{record ? ` · ${record.state.replaceAll("_", " ")}` : ""}</summary>
    <p>Stored history stays on this server. Fetch all available history, or choose a start date. Old commands never run.</p>
    <label>History from <input type="date" aria-label="History from" value={since} onChange={(e) => setSince(e.target.value)} /></label>
    <button type="button" disabled={busy || !ready} onClick={() => void start()}>Sync available history</button>
    <button type="button" disabled={busy || !record} onClick={() => void start(true)}>Cancel history import</button>
    {record && <p role="status">{record.imported} messages saved in this job. {record.since ? `Requested from ${new Date(record.since).toLocaleDateString()}. ` : "All available history requested. "}{record.oldest ? `Oldest observed ${new Date(record.oldest).toLocaleDateString()}. ` : ""} {record.last_sync ? `Last sync ${new Date(record.last_sync).toLocaleString()}. ` : ""}{record.error || record.recent_error ? "History fetch failed; retrying while access remains enabled. " : ""}Available history may have gaps.</p>}
    {error && <p role="alert">{error}</p>}
  </details>;
}
