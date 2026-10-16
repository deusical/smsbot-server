import {useEffect, useRef, useState} from "react";
import {api, inboxAllows, type Conversation, type InboxSettings, type State} from "./api";
import {ArchiveSettings} from "./ArchiveSettings";
import {CaptureChecks} from "./CaptureChecks";
import {HistorySync, importLabel, type Archive} from "./HistorySync";
import type {Workspace} from "./workspace";
export function ArchivePage({conversations, inbox, state, workspace}: {conversations: Conversation[]; inbox: InboxSettings | null; state: State | null; workspace: Workspace}) {
  const [rows,setRows] = useState<Archive[]>([]), [selected,setSelected] = useState("");
  const [error,setError] = useState(""), [loaded,setLoaded] = useState(false), [busy,setBusy] = useState(false);
  const reading = useRef(false), mutating = useRef(false), epoch = useRef(0);
  const supported = !!state?.capabilities.includes("archive_history");
  useEffect(() => {
    if (!supported) return;
    let disposed = false;
    const refresh = async () => {
      if (disposed || reading.current || mutating.current || document.visibilityState === "hidden") return;
      reading.current = true; const version = epoch.current;
      try { const result = await api<Archive[]>("/inbox/archive"); if (!disposed && version === epoch.current) {setRows(result); setLoaded(true); setError("");} }
      catch { if (!disposed && version === epoch.current) setError("Could not load archive progress. Retrying automatically."); }
      finally {reading.current = false;}
    };
    void refresh(); const timer = setInterval(() => void refresh(),3000);
    return () => {disposed = true; clearInterval(timer);};
  },[supported]);
  const known = new Map(rows.map(row => [row.conversation.id,{...row.conversation, archived:true}]));
  for (const conversation of conversations) known.set(conversation.id,{...conversation,archived:known.has(conversation.id) || !!conversation.archived});
  const current = rows.find(row => row.conversation.id === selected) ?? rows[0];
  const conversation = current && known.get(current.conversation.id)!;
  const active = rows.filter(row => ["queued","running","retrying"].includes(row.state)).length;
  const checked = rows.filter(row => row.state === "available_pages_exhausted").length;
  const update = (result: Archive) => setRows(previous => previous.map(row => row.conversation.id === result.conversation.id ? result : row));
  const mutation = (value: boolean) => {epoch.current++; mutating.current = value; setBusy(value);};
  return <div className="archive-page">
    <section className="panel">
      <h2>Background archive</h2>
      <p role="status" aria-label="Background archive scope">{inbox?.enabled ? `${inbox.scope === "all" ? "All individual conversations" : `${inbox.conversations.length} selected individual conversations`}; all available history by default; ${inbox.archive_retention_days ? `keep ${inbox.archive_retention_days} days` : "keep indefinitely"}.` : "Collection stopped; saved history stays readable."}</p>
      <p>Archiving continues with this tab closed and while sending is paused. Collection access is managed in Settings, Messages &amp; privacy.</p>
      {supported ? <div className="archive-summary" aria-label="Archive overview"><span><strong>{rows.length}</strong> conversations tracked</span><span><strong>{active}</strong> imports active</span><span><strong>{checked}</strong> available pages checked</span></div> : <p>History archiving is not supported by this server.</p>}
      <p className="muted">These counts describe history scans, not original-file completion or a guarantee that every message is available.</p>
      {error && <p role="alert">{error}</p>}
      {state?.inbox_capture_gap_unknown && <p role="alert">A possible capture gap has unknown scope. The server cannot confirm which conversations or dates were affected.</p>}
      {state?.inbox_capture_gap && !state.inbox_capture_gap_unknown && <p role="status">{(state.inbox_capture_gaps ?? []).filter(gap => gap.state !== "checked_available").length} conversations have capture checks pending.</p>}
    </section>
    {supported && <section className="panel"><h2>Conversation history</h2>
      {!loaded && !error && <p>Loading archive progress...</p>}
      {loaded && !rows.length && <p>No conversations archived yet. Enable collection in Settings, Messages &amp; privacy.</p>}
      {!!rows.length && <div className="archive-grid"><div>
        <label>Conversation<select aria-label="Archive conversation" value={current?.conversation.id ?? ""} disabled={busy} onChange={e => setSelected(e.target.value)}>{rows.map(row => <option key={row.conversation.id} value={row.conversation.id}>{known.get(row.conversation.id)?.label || "Saved conversation"}</option>)}</select></label>
        <ul className="archive-jobs" aria-label="Archive jobs">{rows.map(row => <li key={row.conversation.id}><button type="button" className="ghost" disabled={busy} aria-pressed={current?.conversation.id === row.conversation.id} onClick={() => setSelected(row.conversation.id)}>{known.get(row.conversation.id)?.label || "Saved conversation"}<br/><small>{importLabel(row.state)}</small></button></li>)}</ul>
      </div>{current && <div><h3>{conversation.label || "Saved conversation"}</h3>
        <HistorySync key={current.conversation.id} record={current} ready={!!state?.transport_ready && inboxAllows(inbox,conversation)} onUpdate={update} onBusy={mutation} />
      </div>}</div>}
    </section>}
    {!!state?.inbox_capture_gaps?.length && <section className="panel"><h2>Capture checks</h2>{state.inbox_capture_gaps.map(gap => {
      const affected = known.get(gap.conversation);
      return <div key={gap.conversation}><h3>{affected?.label || "Saved conversation"}</h3><CaptureChecks gaps={[gap]} ready={!!state.transport_ready && !!affected && inboxAllows(inbox,affected)} /></div>;
    })}</section>}
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
