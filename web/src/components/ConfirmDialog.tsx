import { useState } from 'react';
import { Dialog } from './Dialog';

interface Props {
  title: string;
  message: string;
  confirmLabel: string;
  danger?: boolean;
  onConfirm: () => Promise<void>;
  onClose: () => void;
}

/** Asks before something that cannot be undone. Shows the failure and stays open if the action fails. */
export function ConfirmDialog({ title, message, confirmLabel, danger = false, onConfirm, onClose }: Props) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const confirm = async () => {
    setBusy(true);
    setError(null);
    try {
      await onConfirm();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Something went wrong. Try again.');
      setBusy(false);
    }
  };

  return (
    <Dialog title={title} onClose={onClose}>
      <div className="form">
        <p>{message}</p>
        {error ? (
          <p role="alert" className="notice notice--error">
            {error}
          </p>
        ) : null}
        <div className="dialog__actions">
          <button type="button" className="button" onClick={onClose} data-autofocus>
            Cancel
          </button>
          <button type="button" className={`button ${danger ? 'button--danger-solid' : 'button--primary'}`} disabled={busy} onClick={() => void confirm()}>
            {confirmLabel}
          </button>
        </div>
      </div>
    </Dialog>
  );
}
