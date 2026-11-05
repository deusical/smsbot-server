import { useEffect, useRef, useState } from "react";
import { api } from "../lib/api";
import { Field } from "./ui";

type PairingStatus = { state: string; emoji?: string; maintenance?: boolean };
export function ReplacePhone() {
  const requestRevision = useRef(0);
  const [status, setStatus] = useState<PairingStatus>({ state: "idle" });
  const [input, setInput] = useState("");
  const [open, setOpen] = useState(false);
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);
  useEffect(() => {
    let alive = true;
    let reading = false;
    const poll = async () => {
      if (reading || document.visibilityState === "hidden") return;
      reading = true;
      const revision = requestRevision.current;
      try {
        const next = await api<PairingStatus>("/phone-pairing");
        if (alive && revision === requestRevision.current) { setStatus(next); if (next.state === "pairing" || next.state === "failed" || next.state === "complete") setOpen(true); }
      } catch { /* Leave the last status visible during a network outage. */ }
      finally { reading = false; }
    };
    void poll();
    const timer = window.setInterval(() => void poll(), 1500);
    return () => { alive = false; window.clearInterval(timer); };
  }, []);
  const pairing = status.state === "pairing";
  return <div className="replace-phone">
    <button type="button" aria-expanded={open} disabled={status.state === "unsupported"} onClick={() => setOpen(!open)}>Replace phone</button>
    {open && <div className="replace-phone-body">
      <ol className="pairing-steps">
        <li>On the <strong>new</strong> phone, put in your SIM and make Google Messages the default SMS app.</li>
        <li>On this computer, open a private window: <strong>New Private Window</strong> in Firefox (Ctrl+Shift+P), or <strong>New Incognito window</strong> in Chrome (Ctrl+Shift+N). Go to <code>messages.google.com/web</code>.</li>
        <li>Press F12 for developer tools and open the <strong>Network</strong> tab, so the page's requests are recorded. Leave it open.</li>
        <li>Pair with the new phone, as when you first set up Messages for web: in Google Messages, tap your profile picture, then <strong>Device pairing</strong>, then <strong>QR code scanner</strong>, and scan the code on the page. Don&rsquo;t pair with the old phone.</li>
        <li>In the Network tab, filter for <code>config</code>, then right-click the <code>/web/config</code> request. In Firefox choose <strong>Copy Value</strong>, then <strong>Copy as cURL (POSIX)</strong>. In Chrome or Edge choose <strong>Copy</strong>, then <strong>Copy as cURL (bash)</strong>.</li>
        <li>Paste it below and start pairing. The new phone will show an emoji to approve.</li>
      </ol>
      <p className="hint">Starting pauses automation and cancels unsent work. Your settings and archive stay. Google sign-in information is used only for pairing; it is never shown in Activity.</p>
      {!pairing && <Field label="Copied Google request">
        <textarea aria-label="Copied Google request" autoComplete="off" spellCheck={false} value={input} onChange={e => setInput(e.target.value)} />
      </Field>}
      {pairing && <p role="status">{status.emoji ? `Approve this emoji on your NEW phone: ${status.emoji}` : "Preparing pairing and waiting for Google..."}</p>}
      {status.state === "complete" && <p role="status">Phone paired. Check the SIM above and review Bot replies for conversations that appear as new. Remove the old browser in Device pairing on the old phone. Automation stays paused until you switch to Live.</p>}
      {status.state === "failed" && <p role="alert">Pairing or reconnecting did not complete. {status.maintenance ? "The service is in maintenance; retry here or restart it to recover." : "The service reconnected and remains paused. You can retry."} A failed pairing attempt preserves the previous session.</p>}
      {status.state === "complete" && <button type="button" onClick={() => window.location.reload()}>Refresh devices</button>}
      {error && <p role="alert">{error}</p>}
      <button type="button" disabled={submitting || (!pairing && !input.trim())} onClick={async () => {
        setError(""); setSubmitting(true); requestRevision.current++;
        try {
          if (pairing) { await api("/phone-pairing", "DELETE"); }
          else {
            const cookies = input; setInput("");
            const next = await api<PairingStatus>("/phone-pairing", "POST", { cookies });
            setStatus(next);
          }
        } catch { setError("Request failed. Check pairing status before retrying; copy a fresh request if needed."); }
        finally { setSubmitting(false); }
      }}>{pairing ? "Cancel pairing" : "Pause and pair new phone"}</button>
      <p className="hint">Clear your clipboard after submitting. The previous pairing is kept as an encrypted backup on the server; remove it after verifying the new phone.</p>
    </div>}
  </div>;
}
