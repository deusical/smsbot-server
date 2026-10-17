import { useEffect, useState } from "react";
import type { Attachment } from "./api";
import { Icon } from "./Icon";

export function fileSize(size: number) {
  return size <= 0 ? "Size unknown" : size >= 1024 * 1024
    ? `${(size / 1024 / 1024).toFixed(1)} MB`
    : `${Math.ceil(size / 1024)} KB`;
}
export function encodeFile(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onerror = () =>
      reject(new Error("Could not read this file. Select it again."));
    reader.onload = () => resolve(String(reader.result).split(",", 2)[1]);
    reader.readAsDataURL(file);
  });
}

const imageTypes = new Set([
  "image/png",
  "image/jpeg",
  "image/gif",
  "image/webp",
  "image/bmp",
]);
const videoTypes = new Set(["video/mp4", "video/webm", "video/ogg"]);
const audioTypes = new Set([
  "audio/mpeg",
  "audio/mp3",
  "audio/mp4",
  "audio/ogg",
  "audio/wav",
  "audio/aac",
]);

export function MessageAttachment({
  attachment,
  fileLimitMB,
  base,
  message,
}: {
  attachment: Attachment;
  fileLimitMB: number;
  base: string;
  message: string;
}) {
  const [failed, setFailed] = useState(false);
  const [retry, setRetry] = useState(0);
  const [error, setError] = useState("");
 const [downloading, setDownloading] = useState(false);
 useEffect(()=>{setFailed(false);setError("");setRetry(n=>n+1)},[attachment.id,attachment.archive_state]);
  const url = `/api/v1${base}/messages/${encodeURIComponent(message)}/attachments/${encodeURIComponent(attachment.id)}`;
  const source = `${url}?inline=1&retry=${retry}`;
  const oversized =
    fileLimitMB > 0 && attachment.size > fileLimitMB * 1024 * 1024;
  async function download() {
    if(attachment.archive_state==="saved"){
      const link=document.createElement("a");link.href=`/api/v1${base}/messages/${encodeURIComponent(message)}/attachments/${encodeURIComponent(attachment.id)}`;link.download=attachment.name;link.click();return;
    }
    setDownloading(true);
    setError("");
    try {
      const response = await fetch(url, {
        credentials: "same-origin",
        cache: "no-store",
      });
      if (!response.ok) throw new Error();
      const blob = URL.createObjectURL(await response.blob());
      const link = document.createElement("a");
      link.href = blob;
      link.download = attachment.name;
      link.click();
      setTimeout(() => URL.revokeObjectURL(blob), 1000);
    } catch {
      setError(
        "File unavailable. Your phone may be disconnected or the original may no longer be available.",
      );
    } finally {
      setDownloading(false);
    }
  }
  return (
    <div className="message-attachment">
      {!failed && !oversized && imageTypes.has(attachment.mime) && (
        <a
          href={source}
          target="_blank"
          rel="noreferrer"
          aria-label={`Open ${attachment.name}`}
        >
          <img
            src={source}
            alt={attachment.name}
            loading="lazy"
            onError={() => setFailed(true)}
          />
        </a>
      )}
      {!failed && !oversized && videoTypes.has(attachment.mime) && (
        <video
          controls
          preload="none"
          src={source}
          aria-label={attachment.name}
          onError={() => setFailed(true)}
        />
      )}
      {!failed && !oversized && audioTypes.has(attachment.mime) && (
        <audio
          controls
          preload="none"
          src={source}
          aria-label={attachment.name}
          onError={() => setFailed(true)}
        />
      )}
      <div className="attachment-info">
        <span>
          <strong>{attachment.name}</strong>
          <small>
            {attachment.preview_only ? "Thumbnail preview ? " : ""}{fileSize(attachment.size)} · {attachment.mime}
          </small>
        </span>
        <button
          type="button"
          className="icon ghost"
          aria-label={`Download ${attachment.name}`}
          disabled={oversized || downloading}
          onClick={() => void download()}
        >
          {downloading ? (
            <span className="spinner" />
          ) : (
            <Icon name="download" size={18} />
          )}
        </button>
      </div>
      {oversized && (
        <p className="attachment-error">
          This file exceeds your {fileLimitMB} MB limit. Change it in Messages
          &amp; privacy, or open the file on your phone.
        </p>
      )}
      {failed && (
        <p className="attachment-error">
          Preview unavailable.{" "}
          <button
            type="button"
            className="link"
            onClick={() => {
              setRetry(retry + 1);
              setFailed(false);
            }}
          >
            Retry preview
          </button>
        </p>
      )}
      {error && (
        <p role="alert" className="attachment-error">
          {error}
        </p>
      )}
    </div>
  );
}

export function SelectedAttachment({
  file,
  remove,
  disabled,
}: {
  file: File;
  remove: () => void;
  disabled: boolean;
}) {
  const [preview, setPreview] = useState("");
  useEffect(() => {
    if (!imageTypes.has(file.type)) return;
    const url = URL.createObjectURL(file);
    setPreview(url);
    return () => URL.revokeObjectURL(url);
  }, [file]);
  return (
    <div className={"selected-attachment" + (preview ? " has-thumb" : "")}>
      {preview && <img src={preview} alt="" />}
      <span>
        <strong>{file.name}</strong>
        <small>{fileSize(file.size)}</small>
      </span>
      <button
        type="button"
        className="icon ghost"
        aria-label={`Remove ${file.name}`}
        disabled={disabled}
        onClick={remove}
      >
        <Icon name="x" size={16} />
      </button>
    </div>
  );
}

// A sent-but-not-yet-saved file inside a pending bubble: image thumbnail or a name chip.
export function PendingFile({ file }: { file: File }) {
  const [preview, setPreview] = useState("");
  useEffect(() => {
    if (!imageTypes.has(file.type)) return;
    const url = URL.createObjectURL(file);
    setPreview(url);
    return () => URL.revokeObjectURL(url);
  }, [file]);
  return preview ? (
    <img className="pending-thumb" src={preview} alt={file.name} />
  ) : (
    <span className="pending-file">
      <Icon name="attach" size={14} />
      {file.name}
    </span>
  );
}
