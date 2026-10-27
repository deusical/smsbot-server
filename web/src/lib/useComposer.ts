import { useEffect, useRef, useState, type RefObject } from "react";
import {
  api,
  ApiError,
  defaultAttachmentLimits,
  inboxAllows,
  type Conversation,
  type InboxSettings,
  type Message,
} from "./api";
import { encodeFile } from "../components/Attachments";
import {
  addPending,
  enqueue,
  getPending,
  patchPending,
  removePending,
  type Pending,
} from "./pendingSends";

const quotePreview = (m: Message) => ({
  id: m.id,
  text: m.text.slice(0, 180) || "Attachment",
  direction: m.direction,
  available: true,
});

/**
 * The composer: draft text, selected files and reply target, and turning a
 * draft into pending sends. Each file and the text become separate messages,
 * each one pending intent with its own key, even under a rapid double Enter
 * or click; retries reuse that key and route, so a lost acknowledgement can
 * never send twice.
 */
export function useComposer({
  conversation,
  roster,
  inbox,
  canSend,
  readInbox,
  textarea,
}: {
  conversation: Conversation;
  /** For a group: the roster the owner has seen and agreed to send to. */
  roster?: string;
  inbox?: InboxSettings | null;
  canSend: boolean;
  /** Current collection permission, read just before choosing the send route. */
  readInbox: () => Promise<InboxSettings | null | undefined>;
  textarea: RefObject<HTMLTextAreaElement | null>;
}) {
  const [text, setText] = useState("");
  const [files, setFiles] = useState<File[]>([]);
  const [replyTarget, setReplyTarget] = useState<Message | null>(null);
  const [sendError, setSendError] = useState("");
  const fileLimits = inbox?.attachment_limits ?? defaultAttachmentLimits;
  useEffect(() => setReplyTarget(null), [conversation.id]);
  // Grow the box with its text, up to a limit.
  useEffect(() => {
    const el = textarea.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = Math.min(el.scrollHeight, 200) + "px";
  }, [text]);

  function selectFiles(selected: File[]) {
    const next = [...files, ...selected];
    if (
      (fileLimits.max_files > 0 && next.length > fileLimits.max_files) ||
      next.some((f) => f.size === 0 || (fileLimits.max_file_mb > 0 && f.size > fileLimits.max_file_mb * 1024 * 1024)) ||
      (fileLimits.max_total_mb > 0 && next.reduce((sum, f) => sum + f.size, 0) > fileLimits.max_total_mb * 1024 * 1024)
    ) {
      setSendError("Choose nonempty files within your attachment limits in Settings > Messages & privacy.");
      return;
    }
    setFiles(next);
    setSendError("");
  }
  function removeFile(index: number) {
    setFiles(files.filter((_, i) => i !== index));
  }

  // The draft clears on the next render; until then a second Enter or click
  // would read the same text and enqueue a duplicate with a new key.
  const draftTaken = useRef(false);
  useEffect(() => {
    draftTaken.current = false;
  }, [text, files]);
  function send() {
    const outgoing = text.trim();
    if (!canSend || draftTaken.current || (!outgoing && files.length === 0)) return;
    draftTaken.current = true;
    // The phone sends one file or one text per message: a message combining
    // them is accepted but never delivered. Each file goes as its own message,
    // then the text, in that order. Only the first one quotes the reply target.
    const parts = [...files.map((f) => ({ text: "", files: [f] })), ...(outgoing ? [{ text: outgoing, files: [] }] : [])];
    const start = Date.now();
    const items: Pending[] = parts.map((part, i) => ({
      key: crypto.randomUUID(),
      text: part.text,
      replyTo: i === 0 ? replyTarget?.id : undefined,
      quote: i === 0 && replyTarget ? quotePreview(replyTarget) : undefined,
      files: part.files,
      roster: conversation.group ? roster : undefined,
      at: new Date(start + i).toISOString(),
      route: null,
      phase: "posting",
    }));
    // Clear the box at once; the pending bubbles carry the message from here.
    setText("");
    setReplyTarget(null);
    setFiles([]);
    setSendError("");
    for (const item of items) {
      addPending(conversation.id, item);
      void enqueue(() => postPending(conversation, item.key, readInbox));
    }
    textarea.current?.focus();
  }
  function retryPending(p: Pending) {
    patchPending(conversation.id, p.key, { phase: "posting", failure: undefined, error: undefined });
    void enqueue(() => postPending(conversation, p.key, readInbox));
  }
  /** Moves a definitely unsent message back into the draft. */
  function editPending(p: Pending) {
    removePending(conversation.id, p.key);
    setText((current) => (current.trim() ? `${p.text}\n${current}` : p.text));
    if (p.replyTo && p.quote)
      setReplyTarget({ id: p.replyTo, text: p.quote.text, direction: p.quote.direction ?? "incoming", at: p.at });
    if (p.files.length) selectFiles(p.files);
    textarea.current?.focus();
  }
  return {
    text,
    setText: (value: string) => {
      setText(Array.from(value).slice(0, 12000).join(""));
      setSendError("");
    },
    files,
    selectFiles,
    removeFile,
    replyTarget,
    setReplyTarget,
    sendError,
    send,
    retryPending,
    editPending,
  };
}

export type Composer = ReturnType<typeof useComposer>;

// Browsers name every pasted screenshot "image.png"; give each a distinct name.
let pasteCounter = 0;
export function pastedName(file: File): File {
  if (file.name && file.name !== "image.png") return file;
  const now = new Date(),
    pad = (n: number) => String(n).padStart(2, "0");
  const stamp = `${now.getFullYear()}${pad(now.getMonth() + 1)}${pad(now.getDate())}-${pad(now.getHours())}${pad(now.getMinutes())}${pad(now.getSeconds())}`;
  const extension = file.type.split("/")[1]?.replace("jpeg", "jpg").replace(/[^a-z0-9]/gi, "") || "png";
  return new File([file], `image-${stamp}-${++pasteCounter}.${extension}`, { type: file.type, lastModified: file.lastModified });
}

// A definite refusal: nothing was sent, and why.
function refusal(code: string): string {
  const why: Record<string, string> = {
    bot_unavailable: "sending is paused, it is quiet hours, or your phone is not connected",
    inbox_access_disabled: "sending is not allowed in this conversation",
    attachment_size_limit: "the attachments are too large",
    too_many_attachments: "there are too many attachments",
    request_limit_reached: "the hourly or daily limit was reached",
    group_changed: "the people in this group changed. Check who is in it, then send again",
  };
  return `Not sent: ${why[code] ?? code.replaceAll("_", " ")}. You can retry once that changes.`;
}

// Posts one pending message. A retry reuses the same key and API route, so a
// lost response can never create a second message.
async function postPending(
  conversation: Conversation,
  key: string,
  readInbox: () => Promise<InboxSettings | null | undefined>,
) {
  const item = getPending(conversation.id, key);
  if (!item || item.phase !== "posting") return;
  try {
    const attachments = await Promise.all(
      item.files.map(async (f) => ({ name: f.name, mime: f.type, data: await encodeFile(f) })),
    );
    // Archive reads can be ready before the permission poll catches up. Read
    // current collection permission before selecting the write API/body.
    const useInbox = item.route ? item.route === "inbox" : inboxAllows(await readInbox(), conversation);
    patchPending(conversation.id, key, { route: useInbox ? "inbox" : "legacy" });
    if (!useInbox && (attachments.length || item.replyTo)) throw new ApiError(409, "inbox_access_disabled");
    const composeBase = `${useInbox ? "/inbox" : ""}/conversations/${encodeURIComponent(conversation.id)}`;
    const result = await api<{ id?: string }>(
      `${composeBase}/compose`,
      "POST",
      useInbox
        ? { text: item.text, request_id: key, attachments, ...(item.replyTo ? { reply_to: item.replyTo } : {}), ...(item.roster ? { roster: item.roster } : {}) }
        : { text: item.text },
    );
    patchPending(conversation.id, key, { phase: "queued", job: result?.id, queuedAt: Date.now() });
  } catch (e) {
    // The inbox route deduplicates by key, so retrying can never send twice. The
    // legacy route has no key: only a definite server rejection is safe to retry.
    const legacy = getPending(conversation.id, key)?.route === "legacy";
    const safeRetry = !legacy || e instanceof ApiError;
    patchPending(conversation.id, key, {
      phase: "failed",
      failure: safeRetry ? "unconfirmed" : "uncertain",
      error: !safeRetry
        ? "Not confirmed. Check your phone before sending this again."
        : e instanceof ApiError && e.status >= 400 && e.status < 500
          ? refusal(e.message)
          : "The server did not confirm this message. Retrying is safe and will not send twice.",
    });
  }
}
