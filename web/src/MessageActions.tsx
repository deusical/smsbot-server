import "./messageActions.css";
import {useEffect, useRef, useState} from "react";
import {createPortal} from "react-dom";
import {api, ApiError, type Message, type MessageCapabilities} from "./api";
import {Icon} from "./Icon";
import {actionKey, reactionEpoch, reactionAttempt, setReactionAttempt, useReactionAttempt, type ReactionAttempt} from "./pendingActions";

const commonEmoji = ["👍", "❤️", "😂", "😮", "😢", "😡"];
const moreEmoji = ["👎", "😍", "🎉", "🙏", "🤔", "🥰", "👏", "🔥", "💯", "😊", "✅", "👀", "👍🏻", "👍🏼", "👍🏽", "👍🏾", "👍🏿", "🫶", "🤝", "👩‍💻", "👨‍👩‍👧", "🏳️‍🌈", "🇺🇸", "☕"];
export function MessageActions({message, conversation, label, writable, eligibilityKey, onReply, onChanged}: {
  message: Message; conversation: string; label: string; writable: boolean; eligibilityKey: string;
  onReply: (message: Message) => void; onChanged: () => void;
}) {
  const [open, setOpen] = useState(false), [details, setDetails] = useState(false);
  const [cachedCaps, setCachedCaps] = useState<{key: string; value: MessageCapabilities} | null>(null);
  const [picker, setPicker] = useState(false), [custom, setCustom] = useState("");
  const [error, setError] = useState("");
  const anchor = useRef<HTMLButtonElement>(null), panel = useRef<HTMLDivElement>(null);
  const [position, setPosition] = useState({top: 0, left: 0});
  const path = `/inbox/conversations/${encodeURIComponent(conversation)}`;
  const resource = `${path}/messages/${encodeURIComponent(message.id)}`;
  // Cache only in this mounted row. A message or permission change invalidates
  // it synchronously, before a click can use the previous message's actions.
  const requestKey = JSON.stringify([resource, writable, eligibilityKey, message.protocol, message.revision, message.pending_action, message.reactions]);
  const currentKey = useRef(requestKey); currentKey.current = requestKey;
  const caps = cachedCaps?.key === requestKey ? cachedCaps.value : null;
  const inFlight = useRef<{key: string; controller: AbortController; done: Promise<void>} | null>(null);
  const key = actionKey(conversation, message.id), pending = useReactionAttempt(key);
  const mine = message.reactions?.find(r => r.mine)?.emoji;
  const busy = !!pending && !["failed", "cancelled", "expired", "reaction_confirmed"].includes(pending.state);
  const onChangedRef = useRef(onChanged); onChangedRef.current = onChanged;
  const submitting = useRef(false);
  const restoreFocus = useRef(true);

  useEffect(() => {
    if(message.pending_action && !reactionAttempt(key)) setReactionAttempt(key,{id:message.pending_action,request_id:"",emoji:"",operation:"add",version:"",state:"submitted"});
  },[key,message.pending_action]);
  function capabilities(force = false): Promise<void> {
    if (!writable || (caps && !force)) return Promise.resolve();
    if (inFlight.current?.key === requestKey) return inFlight.current.done;
    inFlight.current?.controller.abort();
    const controller = new AbortController(), epoch = reactionEpoch();
    const request = {key: requestKey, controller, done: Promise.resolve()};
    setError("");
    request.done = (async () => {
      try {
        const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(25000)]);
        const next = await api<MessageCapabilities>(`${resource}/actions`, "GET", undefined, signal);
        if (controller.signal.aborted || currentKey.current !== requestKey || epoch !== reactionEpoch()) return;
        setCachedCaps({key: requestKey, value: next});
        if (next.pending && !reactionAttempt(key)) setReactionAttempt(key, {id: next.pending, request_id: "", emoji: "", operation: "add", version: "", state: "submitted"});
      } catch {
        if (!controller.signal.aborted && currentKey.current === requestKey && epoch === reactionEpoch()) setError("Message actions are unavailable. Try again.");
      } finally {
        if (inFlight.current === request) inFlight.current = null;
      }
    })();
    inFlight.current = request;
    return request.done;
  }
  useEffect(() => {
    setError("");
    if (!writable || !anchor.current) return;
    const observer = new IntersectionObserver(entries => {
      if (entries.some(entry => entry.isIntersecting)) {
        observer.disconnect();
        void capabilities();
      }
    });
    observer.observe(anchor.current);
    return () => {
      observer.disconnect();
      inFlight.current?.controller.abort();
      inFlight.current = null;
    };
  }, [requestKey]);
  function show() {
    restoreFocus.current=true;
    const rect = anchor.current?.getBoundingClientRect();
    if (rect) setPosition({top: Math.max(12, Math.min(rect.bottom + 6, window.innerHeight - 380)), left: Math.max(12, Math.min(rect.right - 300, window.innerWidth - 312))});
    setError(""); setDetails(false); setPicker(false); setOpen(true);
    if (writable && !caps) void capabilities();
  }
  useEffect(() => {
    if (!open) return;
    const previous = document.activeElement as HTMLElement | null;
    panel.current?.focus();
    function keyboard(e: KeyboardEvent) {
      if (e.key === "Escape") {e.preventDefault(); setOpen(false);}
      if (e.key === "Tab") {
        const nodes = Array.from(panel.current?.querySelectorAll<HTMLElement>('button:not(:disabled), input, [tabindex="0"]') ?? []);
        const first = nodes[0], last = nodes[nodes.length - 1];
        if (!first) {e.preventDefault(); return;}
        if (!panel.current?.contains(document.activeElement)) {e.preventDefault();(e.shiftKey ? last : first).focus();return;}
        if (e.shiftKey && (document.activeElement === first || document.activeElement === panel.current)) {e.preventDefault();last.focus();}
        else if (!e.shiftKey && (document.activeElement === last || document.activeElement === panel.current)) {e.preventDefault();first.focus();}
      }
    }
    document.addEventListener("keydown", keyboard);
    return () => {document.removeEventListener("keydown", keyboard);if(restoreFocus.current)previous?.focus();};
  }, [open]);

  useEffect(() => {
    if (!pending?.id || ["failed", "cancelled", "expired", "reaction_confirmed"].includes(pending.state)) return;
    let disposed = false, running = false;
    async function tick() {
      if (running || document.visibilityState === "hidden") return;
      running = true;
      try {
        const result = await api<{state: string}>(`${path}/actions/${encodeURIComponent(pending!.id!)}`);
        if (disposed) return;
        if (result.state === "reaction_confirmed") {setReactionAttempt(key);onChangedRef.current();}
        else if (result.state !== pending!.state) setReactionAttempt(key, {...pending!, state: result.state});
      } catch { /* Retain uncertain evidence; connectivity is not failure. */ }
      finally {running = false;}
    }
    void tick(); const timer = setInterval(() => void tick(), 2000);
    return () => {disposed = true;clearInterval(timer);};
  }, [pending, key, path]);

  async function post(attempt: ReactionAttempt) {
    if (submitting.current) return;
    submitting.current = true;
    setReactionAttempt(key, {...attempt, state: "posting"});
    try {
      const result = await api<{id: string}>(`${resource}/reactions`, "POST", {emoji: attempt.emoji, operation: attempt.operation, version: attempt.version, request_id: attempt.request_id});
      if(reactionAttempt(key)?.request_id===attempt.request_id) setReactionAttempt(key, {...attempt, id: result.id, state: "queued"});setOpen(false);
    } catch (err) {
      // Keep the exact intent/key: the request may have reached the server.
      if(reactionAttempt(key)?.request_id===attempt.request_id) setReactionAttempt(key, {...attempt, state: err instanceof ApiError && err.status >= 400 && err.status < 500 ? "failed" : "unconfirmed"});
      setError("Reaction request not confirmed. Check its status before changing it.");
    } finally {submitting.current = false;}
  }
  function react(emoji: string) {
    if (!caps?.react || busy || !writable) return;
    const operation = mine === emoji ? "remove" : mine ? "switch" : "add";
    void post({request_id: crypto.randomUUID(), emoji, operation, version: caps.version, state: "posting"});
  }
  return <>
    <button ref={anchor} type="button" className="message-options icon ghost" aria-label="Message actions" aria-haspopup="dialog" aria-expanded={open} aria-busy={writable && !caps && !error} onClick={show}><Icon name="more" size={18}/></button>
    {!!message.reactions?.length && <div className="reaction-chips" aria-label="Reactions">
      {message.reactions.map((r, i) => <button key={`${r.emoji}-${i}`} type="button" className={`reaction-chip${r.mine ? " mine" : ""}`} aria-label={`${r.unsupported ? "Unsupported reaction" : r.emoji}: ${r.mine ? "You" : label}${r.count > 1 ? ` and ${r.mine ? label : "you"}` : ""}`} title={`${r.mine ? "You" : label}${r.count > 1 ? ` and ${r.mine ? label : "you"}` : ""}`} onClick={show}>{r.unsupported ? "◌" : r.emoji} <span>{r.count}</span></button>)}
    </div>}
    {pending && <div className="reaction-progress" role="status">
      {pending.state === "unknown_submission" ? "Reaction outcome uncertain. Check your phone." : pending.state === "unconfirmed" ? "Reaction request not confirmed." : ["failed", "cancelled", "expired"].includes(pending.state) ? "Reaction was not sent." : pending.state === "submitted" ? "Waiting for reaction to sync…" : "Updating reaction…"}
      {pending.state === "unconfirmed" && <button type="button" className="small ghost" onClick={() => void post(pending)}>Check request</button>}
      {["failed", "cancelled", "expired"].includes(pending.state) && <button type="button" className="small ghost" onClick={() => {setReactionAttempt(key);void capabilities(true);}}>Dismiss</button>}
    </div>}
    {open && createPortal(<div className="message-action-overlay" onPointerDown={e => {if (e.target === e.currentTarget) setOpen(false);}}>
      <div ref={panel} tabIndex={-1} role="dialog" aria-modal="true" aria-label={details ? "Message details" : "Message actions"} className="message-action-panel" style={{top: position.top, left: position.left}}>
        <div className="message-action-heading"><strong>{details ? "Message details" : "Message actions"}</strong><button type="button" className="icon ghost" aria-label="Close message actions" onClick={() => setOpen(false)}><Icon name="x" size={17}/></button></div>
        {error && <p role="alert" className="hint">{error}</p>}
        {writable && !caps ? <>
          {!error && <p role="status" className="hint">Loading actions...</p>}
          {error && <button type="button" className="message-action-item" onClick={() => void capabilities()}>Try again</button>}
        </> : details ? <dl className="message-details"><dt>Protocol</dt><dd>{message.protocol ?? "Unknown"}</dd><dt>Sent</dt><dd>{new Date(message.at).toLocaleString()}</dd><dt>Sender</dt><dd>{message.direction === "outgoing" ? "You" : label}</dd>{message.status && <><dt>Status</dt><dd>{({submitted:"Sent",delivered:"Delivered",read:"Read"} as Record<string,string>)[message.status] ?? message.status}</dd></>}{!!message.revision && <><dt>Content</dt><dd>Edited · {message.revision} observed revision(s)</dd></>}</dl> : <>
          {writable && caps?.react && <>
            <div className="reaction-picker" aria-label="Choose a reaction">{commonEmoji.map(emoji => <button type="button" key={emoji} disabled={busy} aria-label={`React ${emoji}`} aria-pressed={mine === emoji} onClick={() => react(emoji)}>{emoji}</button>)}<button type="button" disabled={busy} aria-label="More emoji" aria-expanded={picker} onClick={() => setPicker(!picker)}><Icon name="plus" size={17}/></button></div>
            {picker && <div className="emoji-picker-expanded"><div className="emoji-grid">{moreEmoji.map(emoji => <button type="button" key={emoji} disabled={busy} aria-label={`React ${emoji}`} onClick={() => react(emoji)}>{emoji}</button>)}</div><label>Custom emoji<input value={custom} maxLength={32} onChange={e => setCustom(e.target.value)} placeholder="Paste an emoji"/></label><button type="button" className="small" disabled={busy || !custom.trim()} onClick={() => react(custom.trim())}>Use emoji</button></div>}
          </>}
          {writable && caps?.reply && <button type="button" className="message-action-item" onClick={() => {restoreFocus.current=false;onReply(message);setOpen(false);}}><Icon name="back" size={18}/>Reply</button>}
          {!!message.text && <button type="button" className="message-action-item" onClick={() => {void navigator.clipboard.writeText(message.text).then(() => setOpen(false)).catch(() => setError("Could not copy. Select the message text to copy it."));}}><Icon name="copy" size={18}/>Copy text</button>}
          <button type="button" className="message-action-item" onClick={() => setDetails(true)}><Icon name="info" size={18}/>Details</button>
        </>}
      </div>
    </div>, document.body)}
  </>;
}
