import { useEffect, useRef, useState } from "react";
import { api, type Conversation } from "./api";
type Policy={enabled:boolean;max_file_mb:number;quota_mb:number};
type Job={id:string;state:string;error?:string;scope:{plaintext?:boolean};counts:{messages:number;saved_files:number;missing_files:number;bytes:number}};
export function ArchiveSettings({conversations}:{conversations:Conversation[]}){
 const [policy,setPolicy]=useState<Policy|null>(null),[job,setJob]=useState<Job|null>(null),[error,setError]=useState(""),[notice,setNotice]=useState(""),[busy,setBusy]=useState(false);
 const [conversation,setConversation]=useState(""),[since,setSince]=useState(""),[format,setFormat]=useState("encrypted");const pending=useRef(false), epoch=useRef(0), refreshing=useRef(false);
 useEffect(()=>{let disposed=false;void api<Policy>("/inbox/archive/files/policy").then(p=>{if(!disposed)setPolicy(p)}).catch(()=>{if(!disposed)setError("Could not load file archive policy.")});
 const refresh=async()=>{if(pending.current||refreshing.current)return;refreshing.current=true;const started=epoch.current;try{const result=await api<Job|null>("/inbox/archive/backup");if(!disposed&&started===epoch.current)setJob(result)}catch{if(!disposed&&started===epoch.current)setError("Could not load archive export status.")}finally{refreshing.current=false}};void refresh();const timer=setInterval(()=>void refresh(),3000);return()=>{disposed=true;clearInterval(timer)};
 },[]);
 async function action(fn:()=>Promise<void>){if(pending.current)return;pending.current=true;epoch.current++;setBusy(true);setError("");setNotice("");try{await fn()}catch{setError("Archive operation failed. Check disk space and retry.")}finally{pending.current=false;setBusy(false)}}
 return <>
 <section className="panel group"><div className="panel-head"><h2>Original file archive</h2></div>
 <p>Save originals for conversations enabled in Messages &amp; privacy, including files in older history. Saved files remain available when syncing stops or the phone is offline. Thumbnails do not count as originals.</p>
 {policy&&<>
 <label><input type="checkbox" checked={policy.enabled} onChange={e=>setPolicy({...policy,enabled:e.target.checked})}/> Automatically archive original files</label>
 <label>Maximum original file (MiB, 0 unlimited)<input type="number" min="0" aria-label="Archive maximum file MiB" value={policy.max_file_mb} onChange={e=>setPolicy({...policy,max_file_mb:Number(e.target.value)})}/></label>
 <label>Archive disk quota (MiB, 0 unlimited)<input type="number" min="0" aria-label="Archive disk quota MiB" value={policy.quota_mb} onChange={e=>setPolicy({...policy,quota_mb:Number(e.target.value)})}/></label>
 <p>These limits control background preservation separately from sending and viewing limits. Export packages need additional disk space.</p>
 <button type="button" disabled={busy} onClick={()=>void action(async()=>{setPolicy(await api<Policy>("/inbox/archive/files/policy","PUT",policy));setNotice("File archive policy saved.")})}>Save file archive policy</button></>}
 </section>
 <section className="panel group"><div className="panel-head"><h2>Message history export</h2></div>
 <p>Export a consistent snapshot of locally retained messages, observed revisions and available originals. Missing originals are counted explicitly. Collection can continue during export. Only the latest package is kept on this server; download it before starting another.</p>
 <label>Conversations<select aria-label="Archive export conversations" value={conversation} onChange={e=>setConversation(e.target.value)}><option value="">All archived conversations</option>{conversations.filter(c=>c.archived).map(c=><option key={c.id} value={c.id}>{c.label}</option>)}</select></label>
 <label>History from<input type="date" aria-label="Archive export from" value={since} onChange={e=>setSince(e.target.value)}/></label>
 <label>Export format<select aria-label="Archive export format" value={format} onChange={e=>setFormat(e.target.value)}><option value="encrypted">Encrypted backup</option><option value="plaintext">Unencrypted readable export</option></select></label>
 {format==="plaintext"?<p>Unencrypted exports contain readable private messages and original files. Anyone with the package can read them. Pairing credentials and media keys are excluded.</p>:<p>Recovery requires this server's original protected master key and Windows account. Save the package and key safely. Restore offline into fresh initialized state; collection and sending are never restored.</p>}
 <button type="button" disabled={busy||job?.state==="running"} onClick={()=>void action(async()=>{setJob(await api<Job>("/inbox/archive/backup","POST",{conversations:conversation?[conversation]:[],since:since?new Date(`${since}T00:00:00`).toISOString():"",plaintext:format==="plaintext"}))})}>Start message export</button>
 {job?.state==="running"&&<button type="button" disabled={busy} onClick={()=>void action(async()=>{await api("/inbox/archive/backup/cancel","POST",{})})}>Cancel message export</button>}
 {job&&<p role="status">Export {job.state.replaceAll("_"," ")}: {job.counts.messages} messages, {job.counts.saved_files} originals, {job.counts.missing_files} missing originals.{job.error?" Check disk space and retry.":""}</p>}
 {job?.state==="complete"&&<a className="button" href={`/api/v1/inbox/archive/backup/${encodeURIComponent(job.id)}/file`} download={job.scope.plaintext?"messages-export.tar":"messages-archive.sma"}>Download message export</a>}
 {job&&job.state!=="running"&&<button type="button" disabled={busy} onClick={()=>void action(async()=>{await api("/inbox/archive/backup","DELETE");setJob(null)})}>Delete server export package</button>}
 <p>Unencrypted exports contain JSON message records and a files folder. Encrypted recovery instructions are in the server's recovery documentation.</p>
 </section>
 {error&&<p role="alert">{error}</p>}{notice&&<p role="status">{notice}</p>}
 </>;
}
