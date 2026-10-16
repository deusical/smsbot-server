import {useEffect, useRef, useState} from "react";
import {api, inboxAllows, type Conversation, type InboxSettings, type State} from "./api";
import {ArchiveSettings} from "./ArchiveSettings";
import {CaptureChecks} from "./CaptureChecks";
import {HistorySync, importLabel, type Archive} from "./HistorySync";
import type {Workspace} from "./workspace";
type StorageStats = {original_bytes:number;original_files:number;database_bytes:number;export_bytes:number;temporary_bytes:number;total_bytes:number;messages:number;quota_bytes:number;measured_at:string};
export function formatBytes(value:number):string {
  if (!value) return "0 B";
  const unit=Math.min(4,Math.floor(Math.log(value)/Math.log(1024)));
  return `${(value/1024**unit).toLocaleString(undefined,{maximumFractionDigits:1})} ${["B","KiB","MiB","GiB","TiB"][unit]}`;
}
export function ArchivePage({conversations, inbox, state, workspace}: {conversations: Conversation[]; inbox: InboxSettings | null; state: State | null; workspace: Workspace}) {
  const [rows,setRows] = useState<Archive[]>([]), [selected,setSelected] = useState("");
  const [error,setError] = useState(""), [loaded,setLoaded] = useState(false), [busy,setBusy] = useState(false);
  const [reload,setReload] = useState(0), [slow,setSlow] = useState(false);
  const [query,setQuery] = useState("");
  const [stats,setStats] = useState<StorageStats | null>(null), [statsError,setStatsError] = useState(false);
  const statsSupported=!!state?.capabilities.includes("archive_stats");
  useEffect(() => {
    if (!statsSupported) return;
    let disposed=false, reading=false;
    const controller=new AbortController();
    const refresh=async()=>{
      if (reading || document.visibilityState === "hidden") return;
      reading=true;
      try { const result=await api<StorageStats>("/inbox/archive/stats","GET",undefined,AbortSignal.any([controller.signal,AbortSignal.timeout(8000)])); if(!disposed){setStats(result);setStatsError(false);} }
      catch {if(!disposed)setStatsError(true);}
      finally {reading=false;}
    };
    void refresh(); const timer=setInterval(()=>void refresh(),15000);
    document.addEventListener("visibilitychange",refresh);
    return ()=>{disposed=true;controller.abort();clearInterval(timer);document.removeEventListener("visibilitychange",refresh);};
  },[statsSupported,reload]);
  const mutating = useRef(false), epoch = useRef(0);
  const supported = !!state?.capabilities.includes("archive_history");
  useEffect(() => {
    if (!supported) return;
    let disposed = false, reading = false;
    const controller=new AbortController();
    setSlow(false);
    const slowTimer=setTimeout(()=>setSlow(true),2000);
    const refresh = async () => {
      if (disposed || reading || mutating.current || document.visibilityState === "hidden") return;
      reading = true; const version = epoch.current;
      try { const result = await api<Archive[]>("/inbox/archive","GET",undefined,AbortSignal.any([controller.signal,AbortSignal.timeout(8000)])); if (!disposed && version === epoch.current) {setRows(result); setLoaded(true); setError("");} }
      catch { if (!disposed && version === epoch.current) setError("Could not load archive progress. Retrying automatically."); }
      finally {reading = false;}
    };
    void refresh(); const timer = setInterval(() => void refresh(),3000);
    document.addEventListener("visibilitychange",refresh);
    return () => {disposed = true;controller.abort();clearInterval(timer);clearTimeout(slowTimer);document.removeEventListener("visibilitychange",refresh);};
  },[supported,reload]);
  const known = new Map(rows.map(row => [row.conversation.id,{...row.conversation, archived:true}]));
  for (const conversation of conversations) known.set(conversation.id,{...conversation,archived:known.has(conversation.id) || !!conversation.archived});
  const choices=[...known.values()].filter(item=>item.archived || inboxAllows(inbox,item));
  const conversation=choices.find(item=>item.id===selected) ?? choices.find(item=>item.id===rows[0]?.conversation.id) ?? choices[0];
  const current:Archive | undefined=conversation && (rows.find(row=>row.conversation.id===conversation.id) ?? {conversation,state:"not_started",imported:0,since:"",last_sync:"",oldest:""});
  const filtered=choices.filter(item=>(item.label || "Saved conversation").toLocaleLowerCase().includes(query.toLocaleLowerCase()));
  const active = rows.filter(row => ["queued","running","retrying"].includes(row.state)).length;
  const pendingChecks=(state?.inbox_capture_gaps ?? []).filter(gap => gap.state !== "checked_available").length;
  const checked = rows.filter(row => row.state === "available_pages_exhausted").length;
  const update = (result: Archive) => setRows(previous => previous.some(row=>row.conversation.id===result.conversation.id) ? previous.map(row => row.conversation.id === result.conversation.id ? result : row) : [...previous,result]);
  const mutation = (value: boolean) => {epoch.current++; mutating.current = value; setBusy(value);};
  return <div className="archive-page">
    <section className="panel">
      <h2>Background archive</h2>
      <p role="status" aria-label="Background archive scope">{inbox?.enabled ? `${inbox.scope === "all" ? "All individual conversations" : `${inbox.conversations.length} selected individual conversations`}; all available history by default; ${inbox.archive_retention_days ? `keep ${inbox.archive_retention_days} days` : "keep indefinitely"}.` : "Collection stopped; saved history stays readable."}</p>
      <p>Archiving continues with this tab closed and while sending is paused. Collection access is managed in Settings, Messages &amp; privacy.</p>
      {supported ? <div className="archive-summary" aria-label="Archive overview">
        <div className="archive-stat storage-stat"><span>Storage used</span><strong aria-busy={statsSupported && !stats && !statsError} className={!stats ? "archive-metric-placeholder" : undefined}>{stats ? formatBytes(stats.total_bytes) : statsError || !statsSupported ? "Unavailable" : "Loading"}</strong><small>{statsError ? "Measurement unavailable; retrying" : stats ? "Originals, database and exports" : statsSupported ? "Measuring local storage..." : "Update server for storage metrics"}</small></div>
        <div className="archive-stat"><span>Saved messages</span><strong aria-busy={statsSupported && !stats && !statsError} className={!stats ? "archive-metric-placeholder" : undefined}>{stats ? stats.messages.toLocaleString() : statsError || !statsSupported ? "Unavailable" : "Loading"}</strong><small>In archived conversations</small></div>
        <div className="archive-stat"><span>Original files</span><strong aria-busy={statsSupported && !stats && !statsError} className={!stats ? "archive-metric-placeholder" : undefined}>{stats ? stats.original_files.toLocaleString() : statsError || !statsSupported ? "Unavailable" : "Loading"}</strong><small>{stats ? `${formatBytes(stats.original_bytes)} / ${stats.quota_bytes ? formatBytes(stats.quota_bytes) : "unlimited"}` : "Preserved locally"}</small></div>
        <div className="archive-stat"><span>Conversations tracked</span><strong aria-busy={!loaded} className={!loaded ? "archive-metric-placeholder" : undefined}>{loaded ? rows.length : "Loading"}</strong><small>{loaded ? `${active} ${active === 1 ? "import active" : "imports active"}` : "Waiting for archive index"}</small></div>
      </div> : <p>History archiving is not supported by this server.</p>}
      {stats && <details className="archive-storage"><summary>Storage breakdown{statsError ? " (last successful measurement)" : ""}</summary><dl>
        <div><dt>Original files</dt><dd>{formatBytes(stats.original_bytes)}</dd></div>
        <div><dt>Shared service database, including journal</dt><dd>{formatBytes(stats.database_bytes)}</dd></div>
        <div><dt>Backup exports</dt><dd>{formatBytes(stats.export_bytes)}</dd></div>
        <div><dt>Temporary and working files</dt><dd>{formatBytes(stats.temporary_bytes)}</dd></div>
        <div><dt>Original-file storage limit</dt><dd>{stats.quota_bytes ? formatBytes(stats.quota_bytes) : "Unlimited"}</dd></div>
      </dl><p className="muted">Measured {new Date(stats.measured_at).toLocaleTimeString()}; refreshes every 15 seconds. The database also contains service settings and bot activity. File counts describe stored originals, not download completion.</p></details>}
      <p className="muted">{loaded ? <>{checked} {checked === 1 ? "history scan checked" : "history scans checked"} all available pages. Google may not expose every message or original file.</> : "Loading conversation totals..."}</p>
      {error && <p role="alert">{error}</p>}
      {state?.inbox_capture_gap_unknown && <p role="alert">A possible capture gap has unknown scope. The server cannot confirm which conversations or dates were affected.</p>}

    </section>
    {supported && <section className="panel"><h2>Conversation history</h2>
      {!loaded && !error && <p role="status">{slow ? "Archive index is taking longer than usual. Requests time out and retry automatically." : "Loading archive progress..."}</p>}
      {(error || (!loaded && slow)) && <button type="button" onClick={()=>setReload(value=>value+1)}>Retry archive loading</button>}
      {loaded && !rows.length && <p>No conversations archived yet. Choose an enabled conversation below, or enable collection in Settings, Messages &amp; privacy.</p>}
      {loaded && !!choices.length && <div className="archive-grid"><div>
        <label>Find a conversation<input type="search" aria-label="Find archived conversation" placeholder="Search conversations" value={query} onChange={event=>setQuery(event.target.value)} /></label>
        <ul className="archive-jobs" aria-label="Archive jobs">{filtered.map(item => <li key={item.id}><button type="button" className="ghost" disabled={busy} aria-label={`Select ${item.label || "Saved conversation"}`} aria-pressed={current?.conversation.id === item.id} onClick={() => setSelected(item.id)}><span className="archive-conversation-name">{item.label || "Saved conversation"}</span><small className="archive-state">{importLabel(rows.find(row=>row.conversation.id===item.id)?.state ?? "not_started")}</small></button></li>)}</ul>
        {!filtered.length && <p className="muted">No matching conversations.</p>}
      </div>{current && conversation && <div className="archive-detail"><h3 title={conversation.label}>{conversation.label || "Saved conversation"}</h3>
        <HistorySync key={current.conversation.id} record={current} ready={!!state?.transport_ready && inboxAllows(inbox,conversation)} onUpdate={update} onBusy={mutation} />
      </div>}</div>}
    </section>}
    {!!state?.inbox_capture_gaps?.length && <details className="panel archive-verification"><summary><span>Automatic history repair</span><small>{pendingChecks ? `${pendingChecks} ${pendingChecks === 1 ? "conversation" : "conversations"} pending` : "Available history rechecked"}</small></summary>
      <p className="muted">The server retries message updates that could not be saved. Collection must remain enabled; sending can stay paused.</p>
      {state.inbox_capture_gaps.map(gap => {
        const affected = known.get(gap.conversation);
        return <div className="archive-repair-row" key={gap.conversation}><h3>{affected?.label || "Saved conversation"}</h3><CaptureChecks gaps={[gap]} ready={!!state.transport_ready && !!affected && inboxAllows(inbox,affected)} /></div>;
      })}
    </details>}
    {!inbox?.enabled && state?.capabilities.includes("history_import") && <LegacyImport conversations={conversations.filter(conversation => conversation.enrolled && !conversation.archived && !conversation.group)} ready={state.transport_ready} />}
    {workspace.inboxDraft && <section className="panel"><h2>Archive retention</h2><p>Zero keeps archived messages indefinitely. Changes use the shared Save changes bar.</p><label>Keep archived messages for (days)<input type="number" min={0} max={3650} value={workspace.inboxDraft.archive_retention_days ?? 0} onChange={e => workspace.editInbox({archive_retention_days:Number(e.target.value)})} /></label></section>}
    {state?.capabilities.includes("archive_backup") && <ArchiveSettings conversations={[...known.values()]} />}
  </div>;
}

function LegacyImport({conversations, ready}: {conversations: Conversation[]; ready: boolean}) {
  const [selected,setSelected] = useState(""), [cursor,setCursor] = useState(""), [finished,setFinished] = useState(false);
  const [busy,setBusy] = useState(false), [notice,setNotice] = useState("");
  const pending = useRef(false);
  const current = conversations.find(conversation => conversation.id === selected) ?? conversations[0];
  const currentId = useRef("");
  currentId.current = current?.id ?? "";
  useEffect(() => {setCursor("");setFinished(false);setNotice("");},[current?.id]);
  if (!current) return null;
  async function start() {
    if (!current || pending.current) return;
    const id = current.id;
    pending.current = true; setBusy(true); setNotice("");
    try {
      const result = await api<{imported: number; next_cursor: string}>(`/conversations/${encodeURIComponent(current.id)}/history/import`,"POST",{cursor});
      if (currentId.current !== id) return;
      setCursor(result.next_cursor); setFinished(!result.next_cursor || result.next_cursor === cursor);
      setNotice(`${result.imported} messages saved from this phone page. Old commands never run.`);
    } catch {if(currentId.current === id) setNotice("Could not import phone history. Check the connection and conversation access.");}
    finally {pending.current = false; setBusy(false);}
  }
  return <section className="panel"><h2>Enrolled conversation import</h2><p>With full-inbox collection stopped, import available text history for an enrolled conversation one page at a time.</p>
    <label>Conversation<select aria-label="Enrolled import conversation" disabled={busy} value={current.id} onChange={event => {setSelected(event.target.value); setCursor(""); setFinished(false); setNotice("");}}>{conversations.map(conversation => <option key={conversation.id} value={conversation.id}>{conversation.label}</option>)}</select></label>
    <button type="button" disabled={busy || !ready || finished} onClick={() => void start()}>Import phone history</button>
    {finished && <p>All available phone pages checked.</p>}{notice && <p role="status">{notice}</p>}
  </section>;
}
