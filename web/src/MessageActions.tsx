import "./messageActions.css";
import {useEffect, useLayoutEffect, useRef, useState} from "react";
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
  const [capsError, setCapsError] = useState("");
  const [actionError, setActionError] = useState("");
  const anchor = useRef<HTMLButtonElement>(null), panel = useRef<HTMLDivElement>(null);
  const [position, setPosition] = useState({top: 0, left: 0, up: false, right: false});
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
    setCapsError("");
    request.done = (async () => {
      try {
        const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(25000)]);
        const next = await api<MessageCapabilities>(`${resource}/actions`, "GET", undefined, signal);
        if (controller.signal.aborted || currentKey.current !== requestKey || epoch !== reactionEpoch()) return;
        setCachedCaps({key: requestKey, value: next});
        if (next.pending && !reactionAttempt(key)) setReactionAttempt(key, {id: next.pending, request_id: "", emoji: "", operation: "add", version: "", state: "submitted"});
      } catch {
        if (!controller.signal.aborted && currentKey.current === requestKey && epoch === reactionEpoch()) setCapsError("Message actions are unavailable. Try again.");
      } finally {
        if (inFlight.current === request) inFlight.current = null;
      }
    })();
    inFlight.current = request;
    return request.done;
  }
  useEffect(() => {
    if (open && writable && !caps) {
      void capabilities();
    }
  }, [open, writable, caps, requestKey]);
  useEffect(() => {
    setCapsError("");
    setActionError("");
    return () => {
      inFlight.current?.controller.abort();
      inFlight.current = null;
    };
  }, [requestKey]);
  function show() {
    restoreFocus.current=true;
    setCapsError(""); setActionError(""); setDetails(false); setPicker(false); setOpen(true);
  }
  // Touch screens have no hover: tapping the bubble itself opens the menu.
  const showRef = useRef(show); showRef.current = show;
  useEffect(() => {
    const bubble = anchor.current?.closest<HTMLElement>(".bubble");
    if (!bubble) return;
    function tap(e: MouseEvent) {
      if (!window.matchMedia("(hover: none)").matches) return;
      if ((e.target as HTMLElement).closest("button, a, input, textarea, select, video, audio, summary")) return;
      if (window.getSelection()?.toString()) return;
      showRef.current();
    }
    bubble.addEventListener("click", tap);
    return () => bubble.removeEventListener("click", tap);
  }, []);
  // Place the menu beside the message, never on top of it: below when it fits,
  // otherwise above, otherwise to the side. Phones use a bottom sheet instead and
  // the message is scrolled clear of it.
  useLayoutEffect(() => {
    const menu = panel.current, trigger = anchor.current;
    if (!open || !menu || !trigger) return;
    const bubble = trigger.closest<HTMLElement>(".bubble") ?? trigger;
    const box = bubble.getBoundingClientRect();
    const chips = bubble.querySelector(".reaction-chips")?.getBoundingClientRect();
    const bottom = Math.max(box.bottom, chips?.bottom ?? 0);
    const w = menu.offsetWidth, h = menu.offsetHeight, vw = window.innerWidth, vh = window.innerHeight, gap = 8, edge = 12;
    if (window.matchMedia("(max-width: 820px)").matches) {
      const over = bottom - (vh - h - edge);
      if (over > 0) bubble.closest<HTMLElement>(".messages")?.scrollBy({top: over});
      return;
    }
    const outgoing = bubble.classList.contains("outgoing");
    // Prefer space inside the message pane so the menu does not cover the composer.
    const pane = bubble.closest<HTMLElement>(".messages")?.getBoundingClientRect().bottom ?? vh;
    const room = {pane: pane - bottom - edge, below: vh - bottom - edge, above: box.top - edge};
    const place = room.pane >= h + gap ? "below" : room.above >= h + gap ? "above" : room.below >= h + gap ? "below" : "beside";
    const top = place === "below" ? bottom + gap : place === "above" ? box.top - h - gap : Math.max(edge, Math.min(box.top, vh - h - edge));
    const left = place === "beside" ? (outgoing ? box.left - w - gap : box.right + gap) : outgoing ? box.right - w : box.left;
    setPosition({top, left: Math.max(edge, Math.min(left, vw - w - edge)), up: place === "above", right: outgoing && place !== "beside"});
  }, [open, details, picker, caps, capsError, actionError]);
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
    const close = () => setOpen(false);
    document.addEventListener("keydown", keyboard);
    window.addEventListener("resize", close);
    return () => {document.removeEventListener("keydown", keyboard);window.removeEventListener("resize", close);if(restoreFocus.current)previous?.focus();};
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
      setActionError("Reaction request not confirmed. Check its status before changing it.");
    } finally {submitting.current = false;}
  }
  function react(emoji: string) {
    if (!caps?.react || busy || !writable) return;
    const operation = mine === emoji ? "remove" : mine ? "switch" : "add";
    void post({request_id: crypto.randomUUID(), emoji, operation, version: caps.version, state: "posting"});
  }
  return <>
    <button ref={anchor} type="button" className="message-options icon ghost" aria-label="Message actions" aria-haspopup="dialog" aria-expanded={open} aria-busy={false} onClick={show}><Icon name="more" size={18}/></button>
    {!!message.reactions?.length && <div className="reaction-chips" role="group" aria-label="Reactions">
      {message.reactions.map((r, i) => <button key={`${r.emoji}-${i}`} type="button" className={`reaction-chip${r.mine ? " mine" : ""}`} aria-label={`${r.unsupported ? "Unsupported reaction" : r.emoji}: ${r.mine ? "You" : label}${r.count > 1 ? ` and ${r.mine ? label : "you"}` : ""}`} title={`${r.mine ? "You" : label}${r.count > 1 ? ` and ${r.mine ? label : "you"}` : ""}`} onClick={show}>{r.unsupported ? "◌" : r.emoji}{r.count > 1 && <span>{r.count}</span>}</button>)}
    </div>}
    {pending && <div className="reaction-progress" role="status">
      {pending.state === "unknown_submission" ? "Reaction outcome uncertain. Check your phone." : pending.state === "unconfirmed" ? "Reaction request not confirmed." : ["failed", "cancelled", "expired"].includes(pending.state) ? "Reaction was not sent." : pending.state === "submitted" ? "Waiting for reaction to sync…" : "Updating reaction…"}
      {pending.state === "unconfirmed" && <button type="button" className="small ghost" onClick={() => void post(pending)}>Check request</button>}
      {["failed", "cancelled", "expired"].includes(pending.state) && <button type="button" className="small ghost" onClick={() => {setReactionAttempt(key);void capabilities(true);}}>Dismiss</button>}
    </div>}
    {open && createPortal(<div className="message-action-overlay" onPointerDown={e => {if (e.target === e.currentTarget) setOpen(false);}}>
      <div ref={panel} tabIndex={-1} role="dialog" aria-modal="true" aria-busy={!details && writable && !caps && !capsError} aria-label={details ? "Message details" : "Message actions"} className={`message-action-panel${position.up ? " up" : ""}${position.right ? " right" : ""}${details ? " details" : ""}`} style={{top: position.top, left: position.left}}>
        <button type="button" className="message-action-close icon ghost" aria-label="Close message actions" onClick={() => setOpen(false)}><Icon name="x" size={16}/></button>
        {details && <div className="message-action-heading"><button type="button" className="icon ghost" aria-label="Back to actions" onClick={() => setDetails(false)}><Icon name="back" size={16}/></button><strong>Message details</strong></div>}
        {details ? <dl className="message-details"><dt>Protocol</dt><dd>{message.protocol ?? "Unknown"}</dd><dt>Sent</dt><dd>{new Date(message.at).toLocaleString()}</dd><dt>Sender</dt><dd>{message.direction === "outgoing" ? "You" : label}</dd>{message.status && <><dt>Status</dt><dd>{({submitted:"Sent",delivered:"Delivered",read:"Read"} as Record<string,string>)[message.status] ?? message.status}</dd></>}{!!message.revision && <><dt>Content</dt><dd>Edited · {message.revision} observed revision(s)</dd></>}</dl> : <>
          {actionError && <p role="alert" className="hint">{actionError}</p>}
          {capsError && <p role="alert" className="hint">{capsError}</p>}
          {writable && !caps ? <>
            {!capsError && <p role="status" className="hint">Loading actions...</p>}
            {capsError && <button type="button" className="message-action-item" onClick={() => void capabilities(true)}>Try again</button>}
          </> : <>
            {writable && caps?.react && <>
              <div className="reaction-picker" aria-label="Choose a reaction">{commonEmoji.map(emoji => <button type="button" key={emoji} disabled={busy} aria-label={`React ${emoji}`} aria-pressed={mine === emoji} onClick={() => react(emoji)}>{emoji}</button>)}<button type="button" disabled={busy} aria-label="More emoji" aria-expanded={picker} onClick={() => setPicker(!picker)}><Icon name="plus" size={17}/></button></div>
              {picker && <div className="emoji-picker-expanded"><div className="emoji-grid">{moreEmoji.map(emoji => <button type="button" key={emoji} disabled={busy} aria-label={`React ${emoji}`} onClick={() => react(emoji)}>{emoji}</button>)}</div><div className="emoji-custom"><label>Custom emoji<input value={custom} maxLength={32} onChange={e => setCustom(e.target.value)} placeholder="Paste an emoji"/></label><button type="button" disabled={busy || !custom.trim()} aria-label="Use emoji" onClick={() => react(custom.trim())}>Use</button></div></div>}
            </>}
            {writable && caps?.reply && <button type="button" className="message-action-item" onClick={() => {restoreFocus.current=false;onReply(message);setOpen(false);}}><Icon name="reply" size={17}/>Reply</button>}
          </>}
          {!!message.text && <button type="button" className="message-action-item" onClick={() => {void navigator.clipboard.writeText(message.text).then(() => setOpen(false)).catch(() => setActionError("Could not copy. Select the message text to copy it."));}}><Icon name="copy" size={17}/>Copy text</button>}
          <button type="button" className="message-action-item" onClick={() => setDetails(true)}><Icon name="info" size={17}/>Details</button>
        </>}
      </div>
    </div>, document.body)}
  </>;
}
