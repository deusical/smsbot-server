import type { ReactNode } from "react";
import type { Message } from "../lib/api";
import type { Pending } from "../lib/pendingSends";
import { MessageAttachment, PendingFile } from "./Attachments";

const senders: Record<string, string> = {
  bot: "Bot reply",
  web: "Sent from web",
  phone: "Sent from phone",
};
const statusLabels: Record<string, string> = { submitted: "Sent", delivered: "Delivered", read: "Read" };
const statusLabel = (status: string) => statusLabels[status] ?? status;

/** One saved message: quote, files, text, sender, time, edit and status. */
export function MessageBubble({
  message: m,
  label,
  base,
  fileLimitMB,
  joinedPrev,
  highlighted,
  onJump,
  actions,
}: {
  message: Message;
  /** The other participant's name, for quotes of their messages. */
  label: string;
  /** API prefix for attachment downloads. */
  base: string;
  fileLimitMB: number;
  /** Continues a run of messages from the same sender. */
  joinedPrev: boolean;
  highlighted: boolean;
  onJump: (id: string) => void;
  actions?: ReactNode;
}) {
  const at = new Date(m.at);
  return (
    <div
      data-message-id={m.id}
      className={
        "bubble " +
        m.direction +
        (highlighted ? " message-highlight" : "") +
        (joinedPrev ? " joined" : " run-start") +
        (m.reactions?.length ? " has-reactions" : "") +
        (m.direction === "outgoing" ? " from-" + (m.origin ?? "bot") : "")
      }
    >
      {m.quote && (
        <button
          type="button"
          className="message-quote"
          disabled={!m.quote.available || !m.quote.id}
          onClick={() => m.quote?.id && onJump(m.quote.id)}
          aria-label="Jump to original message"
        >
          <strong>{m.quote.available ? (m.quote.direction === "outgoing" ? "You" : label) : "Original unavailable"}</strong>
          <span>{m.quote.text}</span>
        </button>
      )}
      {m.attachments?.map((attachment) => (
        <MessageAttachment key={attachment.id} attachment={attachment} fileLimitMB={fileLimitMB} base={base} message={m.id} />
      ))}
      {m.text && <p>{m.text}</p>}
      <small>
        {m.direction === "outgoing" && <span className="sender">{senders[m.origin ?? ""] ?? "Sent"}</span>}
        <time dateTime={m.at} title={at.toLocaleString()}>
          {at.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" })}
        </time>
        {m.revision ? <span title="Previously observed content is retained in the encrypted archive">Edited</span> : null}
        {m.status && (
          <span className="msg-status" title={m.status === "read" ? "Google reports this message was displayed" : undefined}>
            {statusLabel(m.status)}
          </span>
        )}
      </small>
      {actions}
    </div>
  );
}

/** A message on its way: sending, sent, or failed with its safe next steps. */
export function PendingBubble({
  pending: p,
  label,
  canSend,
  onRetry,
  onEdit,
  onDismiss,
}: {
  pending: Pending;
  label: string;
  canSend: boolean;
  onRetry: () => void;
  onEdit: () => void;
  onDismiss: () => void;
}) {
  return (
    <div className="message-group pending-send">
      <div className={"bubble outgoing from-web pending" + (p.phase === "failed" ? " send-failed" : "")} data-pending={p.phase}>
        {p.files.map((file, index) => (
          <PendingFile key={`${index}-${file.name}`} file={file} />
        ))}
        {p.quote && (
          <div className="message-quote">
            <strong>{p.quote.direction === "outgoing" ? "You" : label}</strong>
            <span>{p.quote.text}</span>
          </div>
        )}
        {p.text && <p>{p.text}</p>}
        <small>
          {p.phase === "failed" ? (
            <span className="pending-error" role="alert">{p.error}</span>
          ) : (
            <span className="pending-state msg-status" role="status">
              {p.phase === "sent" ? "Sent" : (<><span className="spinner" aria-hidden="true" />Sending…</>)}
            </span>
          )}
        </small>
        {p.phase === "failed" && (
          <div className="pending-actions">
            {/* Only an intent the server deduplicates may be retried; a definite
                non-send may be edited; an uncertain one is never resent here. */}
            {p.failure === "unconfirmed" && (
              <button type="button" className="small primary" disabled={!canSend} title={canSend ? undefined : "Sending is unavailable right now"} onClick={onRetry}>Retry sending</button>
            )}
            {p.failure === "not_sent" && (
              <button type="button" className="small" onClick={onEdit}>Edit and resend</button>
            )}
            <button type="button" className="small ghost" onClick={onDismiss}>Dismiss</button>
          </div>
        )}
      </div>
    </div>
  );
}
