import { useRef, useState, type ReactNode, type RefObject } from "react";
import { Icon } from "./Icon";
import { SelectedAttachment } from "./Attachments";
import { pastedName, type Composer as ComposerState } from "../lib/useComposer";

/** The message form: reply preview, send blockers, files and the text box. */
export function Composer({
  composer: c,
  textarea,
  label,
  blocked,
  canSend,
  canAttach,
  busy,
  onResume,
  footer,
}: {
  composer: ComposerState;
  textarea: RefObject<HTMLTextAreaElement | null>;
  label: string;
  /** Why sending is unavailable ("paused" offers Resume), or "". */
  blocked: string;
  canSend: boolean;
  canAttach: boolean;
  busy: boolean;
  onResume?: () => void;
  footer?: ReactNode;
}) {
  const fileInput = useRef<HTMLInputElement>(null);
  const [dragging, setDragging] = useState(false);
  const { text, files, replyTarget, sendError } = c;
  return (
    <form
      className="composer"
      onSubmit={(e) => {
        e.preventDefault();
        c.send();
      }}
    >
      {replyTarget && (
        <div className="reply-composer-preview">
          <div>
            <strong>Replying to {replyTarget.direction === "outgoing" ? "yourself" : label}</strong>
            <span>{replyTarget.text.slice(0, 180) || "Attachment"}</span>
          </div>
          <button type="button" className="icon ghost" aria-label="Cancel reply" onClick={() => { c.setReplyTarget(null); textarea.current?.focus(); }}>
            <Icon name="x" size={17} />
          </button>
        </div>
      )}
      {blocked && (
        <div className="composer-blocked" role="status">
          {blocked === "paused" ? (
            <>
              <span>Sending is paused.</span>
              {onResume && (
                <button type="button" className="small primary" disabled={busy} onClick={onResume}>
                  <Icon name="play" size={13} />
                  Resume sending
                </button>
              )}
            </>
          ) : (
            <span>{blocked}</span>
          )}
        </div>
      )}
      {sendError && (
        <p role="alert" className="inline-alert">
          {sendError}
        </p>
      )}
      {files.length > 0 && (
        <div className="selected-attachments" aria-label="Selected files">
          {files.map((file, index) => (
            <SelectedAttachment key={`${index}-${file.name}`} file={file} disabled={false} remove={() => c.removeFile(index)} />
          ))}
        </div>
      )}
      <div
        className={"composer-box" + (dragging ? " dragging" : "")}
        onDragOver={(e) => {
          if (!canAttach || !e.dataTransfer.types.includes("Files")) return;
          e.preventDefault();
          setDragging(true);
        }}
        onDragLeave={(e) => {
          if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setDragging(false);
        }}
        onDrop={(e) => {
          setDragging(false);
          if (!canAttach || !e.dataTransfer.files.length) return;
          e.preventDefault();
          c.selectFiles(Array.from(e.dataTransfer.files));
        }}
      >
        {canAttach && (
          <>
            <input
              ref={fileInput}
              className="sr-only"
              type="file"
              multiple
              aria-label="Choose files"
              onChange={(event) => {
                c.selectFiles(Array.from(event.target.files ?? []));
                event.target.value = "";
              }}
            />
            <button
              type="button"
              className="icon ghost"
              aria-label="Attach files"
              title="Attach files ? limits are configurable in Messages & privacy"
              onClick={() => fileInput.current?.click()}
            >
              <Icon name="attach" size={20} />
            </button>
          </>
        )}
        <textarea
          ref={textarea}
          onPaste={(e) => {
            // Pasted screenshots/images become attachments. Text pastes are untouched.
            if (!canAttach) return;
            const images = Array.from(e.clipboardData.files).filter((f) => f.type.startsWith("image/"));
            if (!images.length) return;
            e.preventDefault();
            c.selectFiles(images.map(pastedName));
          }}
          rows={1}
          maxLength={24000}
          aria-label="Message"
          enterKeyHint="enter"
          placeholder={`Text ${label}`}
          value={text}
          onChange={(e) => c.setText(e.target.value)}
          onKeyDown={(e) => {
            // On touch keyboards Enter adds a line; the send button sends.
            if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing && !window.matchMedia?.("(pointer: coarse)")?.matches) {
              e.preventDefault();
              if (!e.repeat) c.send();
            }
          }}
        />
        <button className="send" aria-label="Send message" disabled={!canSend || (!text.trim() && files.length === 0)}>
          <Icon name="send" size={18} />
        </button>
      </div>
      <div className="composer-meta">
        <small className="enter-hint">Enter to send, Shift+Enter for a new line</small>
        {text.length > 600 && <small>{Array.from(text).length.toLocaleString()} / 12,000</small>}
        {footer}
      </div>
    </form>
  );
}
