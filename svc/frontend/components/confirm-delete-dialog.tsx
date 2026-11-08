"use client";

import { useEffect } from "react";

type ConfirmDeleteDialogProps = {
  open: boolean;
  itemName: string;
  itemType: "source" | "project";
  impact: string;
  error?: string;
  busy?: boolean;
  onCancel: () => void;
  onConfirm: () => void;
};

export default function ConfirmDeleteDialog({
  open,
  itemName,
  itemType,
  impact,
  error = "",
  busy = false,
  onCancel,
  onConfirm,
}: ConfirmDeleteDialogProps) {
  useEffect(() => {
    if (!open || busy) return;
    function onKeyDown(event: KeyboardEvent) {
      if (event.key === "Escape") onCancel();
    }
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [busy, onCancel, open]);

  if (!open) return null;

  const titleId = "confirm-delete-title";
  const descriptionId = "confirm-delete-description";
  const impactId = "confirm-delete-impact";

  return (
    <div
      className="confirm-delete-backdrop"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget && !busy) onCancel();
      }}
    >
      <section
        className="confirm-delete-dialog"
        role="alertdialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={`${descriptionId} ${impactId}`}
        aria-busy={busy}
      >
        <div className="confirm-delete-heading">
          <span className="confirm-delete-icon" aria-hidden="true">
            <svg viewBox="0 0 24 24" fill="none">
              <path d="M4 7h16M9 7V4h6v3m3 0-.8 13H6.8L6 7m4 4v5m4-5v5" />
            </svg>
          </span>
          <button
            type="button"
            className="confirm-delete-close"
            onClick={onCancel}
            aria-label="Cancel deletion"
            disabled={busy}
          >
            ×
          </button>
        </div>

        <h2 id={titleId}>Delete this {itemType}?</h2>
        <p id={descriptionId} className="confirm-delete-name">“{itemName}” will be permanently deleted.</p>
        <p id={impactId} className="confirm-delete-impact">{impact}</p>
        {error && <p className="confirm-delete-error" role="alert">{error}</p>}

        <div className="confirm-delete-actions">
          <button type="button" className="button button-outline" onClick={onCancel} disabled={busy}>
            Keep {itemType}
          </button>
          <button type="button" className="button button-danger" onClick={onConfirm} disabled={busy}>
            {busy ? <><i className="confirm-delete-spinner" /> Deleting…</> : `Delete ${itemType}`}
          </button>
        </div>
      </section>
    </div>
  );
}
