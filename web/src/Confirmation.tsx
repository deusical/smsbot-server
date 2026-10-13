import { useEffect, useRef, type ReactNode } from "react";
export function Confirmation({
  title,
  description,
  confirmLabel,
  cancelLabel = "Cancel",
  disabled = false,
  onConfirm,
  onCancel,
  children,
}: {
  title: string;
  description: string;
  confirmLabel: string;
  cancelLabel?: string;
  disabled?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
  children?: ReactNode;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    dialog.current?.showModal();
  }, []);
  return (
    <dialog
      ref={dialog}
      className="modal"
      role="alertdialog"
      aria-modal="true"
      aria-label={title}
      onCancel={(event) => {
        event.preventDefault();
        onCancel();
      }}
    >
      <h2>{title}</h2>
      <p>{description}</p>
      {children}
      <div className="actions">
        <button
          autoFocus
          type="button"
          className="ghost"
          onClick={onCancel}
        >
          {cancelLabel}
        </button>
        <button
          type="button"
          className="primary"
          disabled={disabled}
          onClick={onConfirm}
        >
          {confirmLabel}
        </button>
      </div>
    </dialog>
  );
}
