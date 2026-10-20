import { useId, useMemo, useState } from 'react';
import { ApiError } from '../../api/client';
import { importProxies, previewImport } from '../../api/endpoints';
import type { ImportPreview, ImportRow, Protocol } from '../../api/types';
import { Dialog } from '../../components/Dialog';
import { describeImportError } from '../../lib/format';

interface Props {
  onClose: () => void;
  /** Called with the number of imported proxies. */
  onImported: (count: number) => void;
}

function rowResult(r: ImportRow): string {
  if (r.valid && !r.duplicate) return 'Ready to import';
  if (r.valid) return 'Duplicate: this proxy is already added or repeated in the list.';
  return describeImportError(r.error_code);
}

function importable(r: ImportRow): boolean {
  return r.valid && !r.duplicate;
}

function describeError(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.code === 'too_many_lines') return 'The list has more than 1,000 lines. Split it and import it in parts.';
    if (err.code === 'payload_too_large') return 'The list is larger than 256 KiB. Split it and import it in parts.';
    return err.message;
  }
  return 'Something went wrong. Try again.';
}

export function ImportDialog({ onClose, onImported }: Props) {
  const uid = useId();
  const [text, setText] = useState('');
  const [protocol, setProtocol] = useState<Protocol>('http');
  const [result, setResult] = useState<ImportPreview | null>(null);
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const ready = useMemo(() => (result ? result.rows.filter(importable) : []), [result]);
  const over = result ? result.existing_count + selected.size > result.limit : false;

  const edit = (next: () => void) => {
    // Any change makes the preview stale: it must be generated again.
    next();
    setResult(null);
    setSelected(new Set());
    setError(null);
  };

  const runPreview = async () => {
    setBusy(true);
    setError(null);
    try {
      const out = await previewImport(text, protocol);
      setResult(out);
      setSelected(new Set(out.rows.filter(importable).map((r) => r.line)));
    } catch (err) {
      setResult(null);
      setError(describeError(err));
    } finally {
      setBusy(false);
    }
  };

  const runImport = async () => {
    setBusy(true);
    setError(null);
    try {
      const out = await importProxies(text, protocol, [...selected].sort((a, b) => a - b));
      onImported(out.imported);
    } catch (err) {
      setError(describeError(err));
    } finally {
      setBusy(false);
    }
  };

  const toggle = (line: number, on: boolean) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (on) next.add(line);
      else next.delete(line);
      return next;
    });
  };

  const allSelected = ready.length > 0 && ready.every((r) => selected.has(r.line));
  const count = selected.size;

  return (
    <Dialog title="Import list" onClose={onClose} wide>
      <div className="form">
        <p className="hint">
          One proxy per line: <code>scheme://user:password@host:port</code>, <code>scheme://host:port</code> or <code>host:port</code>. Encode special characters in credentials, for example &quot;@&quot; as %40.
        </p>
        <div className="field-row">
          <div className="field">
            <label htmlFor={`${uid}-protocol`}>Default protocol</label>
            <select id={`${uid}-protocol`} value={protocol} onChange={(e) => edit(() => setProtocol(e.target.value as Protocol))}>
              <option value="http">HTTP</option>
              <option value="https">HTTPS</option>
              <option value="socks5">SOCKS5</option>
            </select>
          </div>
        </div>
        <div className="field">
          <label htmlFor={`${uid}-text`}>Proxy list</label>
          <textarea
            id={`${uid}-text`}
            value={text}
            rows={7}
            onChange={(e) => edit(() => setText(e.target.value))}
            placeholder={'socks5://user:password@10.0.0.9:1080\n10.0.0.5:3128'}
            spellCheck={false}
            autoComplete="off"
            data-autofocus
          />
        </div>

        {error ? (
          <p role="alert" className="notice notice--error">
            {error}
          </p>
        ) : null}

        {result ? (
          <>
            <p className="summary">
              {result.rows.length} {result.rows.length === 1 ? 'line' : 'lines'}, {result.importable_count} ready, {result.rows.length - result.importable_count} skipped. {result.existing_count} of {result.limit} proxies are used.
            </p>
            {result.importable_count === 0 ? <p className="notice">Nothing in this list can be imported. Fix the lines below and preview again.</p> : null}
            {over ? (
              <p role="alert" className="notice notice--error">
                Importing {count} {count === 1 ? 'proxy' : 'proxies'} would exceed the limit of {result.limit}. Deselect some rows.
              </p>
            ) : null}
            <div className="table-scroll">
              <table className="table table--compact">
                <caption className="visually-hidden">Import preview</caption>
                <thead>
                  <tr>
                    <th scope="col">
                      <input
                        type="checkbox"
                        aria-label="Select all importable rows"
                        checked={allSelected}
                        disabled={ready.length === 0}
                        onChange={(e) => setSelected(e.target.checked ? new Set(ready.map((r) => r.line)) : new Set())}
                      />
                    </th>
                    <th scope="col">Line</th>
                    <th scope="col">Address</th>
                    <th scope="col">Result</th>
                  </tr>
                </thead>
                <tbody>
                  {result.rows.map((r) => (
                    <tr key={r.line} className={importable(r) ? undefined : 'row--skipped'}>
                      <td>
                        <input
                          type="checkbox"
                          aria-label={`Import line ${r.line}`}
                          checked={selected.has(r.line)}
                          disabled={!importable(r)}
                          onChange={(e) => toggle(r.line, e.target.checked)}
                        />
                      </td>
                      <td className="num">{r.line}</td>
                      <td className="mono">{r.masked}</td>
                      <td>{rowResult(r)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </>
        ) : null}

        <div className="dialog__actions">
          <button type="button" className="button" onClick={onClose}>
            Cancel
          </button>
          {result ? (
            <button type="button" className="button button--primary" disabled={busy || count === 0 || over} onClick={() => void runImport()}>
              Import {count} {count === 1 ? 'proxy' : 'proxies'}
            </button>
          ) : (
            <button type="button" className="button button--primary" disabled={busy || text.trim() === ''} onClick={() => void runPreview()}>
              Preview
            </button>
          )}
        </div>
      </div>
    </Dialog>
  );
}
