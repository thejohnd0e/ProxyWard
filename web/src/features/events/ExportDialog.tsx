import { useEffect, useId, useRef, useState, type FormEvent } from 'react';
import { ApiError } from '../../api/client';
import { exportData } from '../../api/endpoints';
import { Dialog } from '../../components/Dialog';

const DAY_MS = 24 * 60 * 60 * 1000;

/** Formats a date for a datetime-local input, in this computer's time zone. */
function toLocalInput(d: Date): string {
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

function saveFile(blob: Blob): void {
  const url = URL.createObjectURL(blob);
  try {
    const link = document.createElement('a');
    link.href = url;
    link.download = 'proxyward-export.json';
    link.rel = 'noopener';
    document.body.append(link);
    link.click();
    link.remove();
  } finally {
    URL.revokeObjectURL(url);
  }
}

interface Props {
  proxies: Array<{ id: number; name: string }>;
  proxyError: string | null;
  onRetryProxies: () => void;
  onClose: () => void;
}

/**
 * Downloads one JSON file with the selected events and check results. The
 * browser chooses where to save it; nothing is written by the server.
 */
export function ExportDialog({ proxies, proxyError, onRetryProxies, onClose }: Props) {
  const uid = useId();
  const [from, setFrom] = useState(() => toLocalInput(new Date(Date.now() - DAY_MS)));
  const [to, setTo] = useState(() => toLocalInput(new Date()));
  const [selected, setSelected] = useState<Set<number>>(() => new Set(proxies.map((p) => p.id)));
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const selectAll = useRef<HTMLInputElement>(null);
  const pending = useRef<AbortController | null>(null);
  const openedWithProxies = useRef(proxies.length > 0);

  // The list can arrive after the dialog opened (for example after a retry).
  useEffect(() => {
    if (openedWithProxies.current || proxies.length === 0) return;
    openedWithProxies.current = true;
    setSelected(new Set(proxies.map((p) => p.id)));
  }, [proxies]);

  useEffect(
    () => () => {
      pending.current?.abort();
    },
    [],
  );

  const allSelected = proxies.length > 0 && selected.size === proxies.length;
  useEffect(() => {
    if (selectAll.current) selectAll.current.indeterminate = selected.size > 0 && !allSelected;
  }, [selected, allSelected]);

  const toggle = (id: number, on: boolean) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (on) next.add(id);
      else next.delete(id);
      return next;
    });
  };

  const submit = async (ev: FormEvent) => {
    ev.preventDefault();
    const start = new Date(from);
    const end = new Date(to);
    if (from === '' || to === '' || Number.isNaN(start.getTime()) || Number.isNaN(end.getTime())) {
      setError('Enter a start and an end for the period.');
      return;
    }
    if (end.getTime() <= start.getTime()) {
      setError('The end of the period must be later than its start.');
      return;
    }
    if (selected.size === 0) {
      setError('Select at least one proxy.');
      return;
    }
    setBusy(true);
    setError(null);
    const ctl = new AbortController();
    pending.current = ctl;
    try {
      const blob = await exportData(
        { from: start.toISOString(), to: end.toISOString(), proxyIds: proxies.filter((p) => selected.has(p.id)).map((p) => p.id) },
        ctl.signal,
      );
      saveFile(blob);
      onClose();
    } catch (err) {
      if (ctl.signal.aborted) return;
      setError(err instanceof ApiError ? err.message : 'Could not create the export.');
    } finally {
      if (!ctl.signal.aborted) setBusy(false);
    }
  };

  const fieldId = (key: string) => `${uid}-${key}`;

  return (
    <Dialog title="Export events and checks" onClose={onClose} wide>
      <form onSubmit={(e) => void submit(e)} noValidate className="form">
        {proxyError ? (
          <div role="alert" className="notice notice--error">
            <p>{proxyError}</p>
            <button type="button" className="button" onClick={onRetryProxies}>
              Try again
            </button>
          </div>
        ) : error ? (
          <p role="alert" className="notice notice--error">
            {error}
          </p>
        ) : null}

        <div className="field-row">
          <div className="field field--grow">
            <label htmlFor={fieldId('from')}>Start of the period</label>
            <input id={fieldId('from')} type="datetime-local" value={from} onChange={(e) => setFrom(e.target.value)} />
          </div>
          <div className="field field--grow">
            <label htmlFor={fieldId('to')}>End of the period</label>
            <input id={fieldId('to')} type="datetime-local" value={to} onChange={(e) => setTo(e.target.value)} />
          </div>
        </div>
        <p className="hint">Times are in this computer's time zone and are sent to the server in UTC.</p>

        <fieldset className="fieldset">
          <legend>Proxies</legend>
          <label className="check">
            <input
              ref={selectAll}
              type="checkbox"
              checked={allSelected}
              disabled={proxies.length === 0}
              onChange={(e) => setSelected(e.target.checked ? new Set(proxies.map((p) => p.id)) : new Set())}
            />
            Select all proxies
          </label>
          {proxies.map((p) => (
            <label key={p.id} className="check">
              <input type="checkbox" checked={selected.has(p.id)} onChange={(e) => toggle(p.id, e.target.checked)} />
              {p.name}
            </label>
          ))}
          {proxies.length === 0 && !proxyError ? <p className="hint">There are no proxies to export.</p> : null}
        </fieldset>

        <div className="notice">
          <p>The file lists proxy names, addresses and exit IPs. Proxy names are user-written text, so read the file before sharing it.</p>
          <p>Only history that ProxyWard still stores is exported; older results are removed by the retention setting.</p>
          <p>Events of deleted proxies are not included because they can no longer be attributed to a proxy.</p>
          <p>Your browser chooses where the file is saved and may ask you to pick a location.</p>
        </div>

        <div className="dialog__actions">
          <button type="button" className="button" onClick={onClose}>
            Cancel
          </button>
          <button type="submit" className="button button--primary" disabled={busy || proxyError !== null || proxies.length === 0}>
            {busy ? 'Preparing…' : 'Download export'}
          </button>
        </div>
      </form>
    </Dialog>
  );
}
