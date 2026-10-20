import { useCallback, useMemo, useState } from 'react';
import { ApiError } from '../../api/client';
import { deleteProxy, enqueueChecks, listProxies } from '../../api/endpoints';
import type { CheckKind, Proxy, ProxyList, State } from '../../api/types';
import { ConfirmDialog } from '../../components/ConfirmDialog';
import { useVisiblePolling } from '../../lib/useVisiblePolling';
import { ImportDialog } from './ImportDialog';
import { ProxyDialog } from './ProxyDialog';
import { ProxyTable } from './ProxyTable';

type Dialog = { type: 'add' } | { type: 'import' } | { type: 'edit'; proxy: Proxy } | { type: 'delete'; proxies: Proxy[] } | null;
type StateFilter = 'all' | State;
type SortKey = 'name' | 'status' | 'latency' | 'download' | 'uptime';

const STATE_ORDER: Record<State, number> = { down: 0, indeterminate: 1, unknown: 2, up: 3 };

function matches(p: Proxy, q: string): boolean {
  if (q === '') return true;
  const hay = [p.name, p.host, p.tags.join(' '), p.status.exit_ip, p.geo?.country ?? '', p.geo?.city ?? '', p.geo?.organization ?? ''].join(' ').toLowerCase();
  return hay.includes(q);
}

function compare(a: Proxy, b: Proxy, key: SortKey): number {
  const byName = a.name.localeCompare(b.name, 'en');
  switch (key) {
    case 'status':
      return STATE_ORDER[a.status.state] - STATE_ORDER[b.status.state] || byName;
    case 'latency':
      return (a.status.duration_ms ?? Infinity) - (b.status.duration_ms ?? Infinity) || byName;
    case 'download':
      return (b.speed?.download.mbps ?? -1) - (a.speed?.download.mbps ?? -1) || byName;
    case 'uptime':
      return (b.uptime.percent ?? -1) - (a.uptime.percent ?? -1) || byName;
    default:
      return byName;
  }
}

function errorText(err: unknown): string {
  return err instanceof ApiError ? err.message : 'Something went wrong. Try again.';
}

export function ProxyPage() {
  const [data, setData] = useState<ProxyList | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [search, setSearch] = useState('');
  const [stateFilter, setStateFilter] = useState<StateFilter>('all');
  const [sort, setSort] = useState<SortKey>('name');
  const [dialog, setDialog] = useState<Dialog>(null);
  const [notice, setNotice] = useState<{ kind: 'info' | 'error'; text: string } | null>(null);

  const refresh = useCallback(async (signal?: AbortSignal) => {
    try {
      const list = await listProxies(signal);
      setData(list);
      setLoadError(null);
      // Forget selections of proxies that no longer exist.
      setSelected((prev) => {
        const ids = new Set(list.proxies.map((p) => p.id));
        const next = new Set([...prev].filter((id) => ids.has(id)));
        return next.size === prev.size ? prev : next;
      });
    } catch (err) {
      if (signal?.aborted) return;
      setLoadError(errorText(err));
    }
  }, []);

  useVisiblePolling(refresh, 5000);

  const proxies = data?.proxies ?? [];
  const visible = useMemo(() => {
    const q = search.trim().toLowerCase();
    return proxies
      .filter((p) => (stateFilter === 'all' || p.status.state === stateFilter) && matches(p, q))
      .sort((a, b) => compare(a, b, sort));
  }, [proxies, search, stateFilter, sort]);

  const counts = useMemo(() => {
    const c: Record<State, number> = { up: 0, down: 0, indeterminate: 0, unknown: 0 };
    for (const p of proxies) c[p.status.state]++;
    return c;
  }, [proxies]);

  const toggle = (id: number, on: boolean) =>
    setSelected((prev) => {
      const next = new Set(prev);
      if (on) next.add(id);
      else next.delete(id);
      return next;
    });
  const toggleAll = (on: boolean) => setSelected(on ? new Set(visible.map((p) => p.id)) : new Set());

  const run = async (kind: CheckKind, ids: number[] | 'all') => {
    setNotice(null);
    try {
      const out = await enqueueChecks(kind, ids === 'all' ? { all: true } : { proxyIds: ids });
      const n = out.new.length;
      const what = kind === 'availability' ? (n === 1 ? 'availability check' : 'availability checks') : n === 1 ? 'speed test' : 'speed tests';
      const parts: string[] = [];
      if (n > 0) parts.push(`Queued ${n} ${what}.`);
      if (out.existing.length > 0) parts.push(`${out.existing.length} already queued or running.`);
      if (parts.length === 0) parts.push('There was nothing to queue.');
      setNotice({ kind: 'info', text: parts.join(' ') });
      await refresh();
    } catch (err) {
      setNotice({ kind: 'error', text: errorText(err) });
    }
  };

  const selectedProxies = proxies.filter((p) => selected.has(p.id));
  const target: number[] | 'all' = selected.size > 0 ? [...selected] : 'all';
  const label = (verb: string, all: string) => (selected.size > 0 ? `${verb} selected (${selected.size})` : all);
  const atLimit = data !== null && proxies.length >= data.limit;

  return (
    <section aria-labelledby="proxies-title">
      <div className="page-head">
        <div>
          <h1 id="proxies-title">Proxies</h1>
          <p className="muted" aria-live="polite">
            {data
              ? `${proxies.length} of ${data.limit} proxies: ${counts.up} up, ${counts.down} down, ${counts.indeterminate} indeterminate, ${counts.unknown} unknown`
              : 'Loading…'}
          </p>
        </div>
        <div className="page-actions">
          <button type="button" className="button" onClick={() => setDialog({ type: 'import' })} disabled={atLimit}>
            Import list
          </button>
          <button type="button" className="button button--primary" onClick={() => setDialog({ type: 'add' })} disabled={atLimit}>
            Add proxy
          </button>
        </div>
      </div>

      {atLimit ? <p className="notice">The limit of {data?.limit} proxies is reached. Delete a proxy to add another.</p> : null}
      {loadError ? (
        <p role="alert" className="notice notice--error">
          {data ? 'Showing the last loaded data. ' : ''}
          {loadError}
        </p>
      ) : null}
      {notice ? (
        <p role={notice.kind === 'error' ? 'alert' : 'status'} className={`notice${notice.kind === 'error' ? ' notice--error' : ''}`}>
          {notice.text}
        </p>
      ) : null}

      <div className="toolbar">
        <div className="field field--grow">
          <label htmlFor="proxy-search" className="visually-hidden">
            Search proxies
          </label>
          <input id="proxy-search" type="search" placeholder="Search name, host, tag, IP or country" value={search} onChange={(e) => setSearch(e.target.value)} />
        </div>
        <div className="field">
          <label htmlFor="proxy-state" className="visually-hidden">
            Filter by state
          </label>
          <select id="proxy-state" value={stateFilter} onChange={(e) => setStateFilter(e.target.value as StateFilter)}>
            <option value="all">All states</option>
            <option value="up">Up</option>
            <option value="down">Down</option>
            <option value="indeterminate">Indeterminate</option>
            <option value="unknown">Unknown</option>
          </select>
        </div>
        <div className="field">
          <label htmlFor="proxy-sort" className="visually-hidden">
            Sort by
          </label>
          <select id="proxy-sort" value={sort} onChange={(e) => setSort(e.target.value as SortKey)}>
            <option value="name">Sort by name</option>
            <option value="status">Sort by state</option>
            <option value="latency">Sort by latency</option>
            <option value="download">Sort by download speed</option>
            <option value="uptime">Sort by uptime</option>
          </select>
        </div>
      </div>

      <div className="bulk" role="group" aria-label="Checks">
        <button type="button" className="button" disabled={proxies.length === 0} onClick={() => void run('availability', target)}>
          {label('Check', 'Check all proxies')}
        </button>
        <button type="button" className="button" disabled={proxies.length === 0} onClick={() => void run('speed', target)}>
          {label('Test speed of', 'Test speed of all proxies')}
        </button>
        {selected.size > 0 ? (
          <>
            <button type="button" className="button button--danger" onClick={() => setDialog({ type: 'delete', proxies: selectedProxies })}>
              Delete selected ({selected.size})
            </button>
            <button type="button" className="button button--quiet" onClick={() => setSelected(new Set())}>
              Clear selection
            </button>
          </>
        ) : null}
      </div>

      {data === null && !loadError ? null : (
        <ProxyTable
          proxies={visible}
          geoConfigured={data?.geo_configured ?? false}
          selected={selected}
          onToggle={toggle}
          onToggleAll={toggleAll}
          onCheck={(kind, id) => void run(kind, [id])}
          onEdit={(proxy) => setDialog({ type: 'edit', proxy })}
          onDelete={(proxy) => setDialog({ type: 'delete', proxies: [proxy] })}
        />
      )}
      {proxies.length > 0 && visible.length === 0 ? <p className="empty__title">No proxy matches these filters.</p> : null}

      {dialog?.type === 'add' ? (
        <ProxyDialog
          onClose={() => setDialog(null)}
          onSaved={() => {
            setDialog(null);
            void refresh();
          }}
        />
      ) : null}
      {dialog?.type === 'edit' ? (
        <ProxyDialog
          proxy={dialog.proxy}
          onClose={() => setDialog(null)}
          onSaved={() => {
            setDialog(null);
            void refresh();
          }}
        />
      ) : null}
      {dialog?.type === 'import' ? (
        <ImportDialog
          onClose={() => setDialog(null)}
          onImported={(count) => {
            setDialog(null);
            setNotice({ kind: 'info', text: `Imported ${count} ${count === 1 ? 'proxy' : 'proxies'}.` });
            void refresh();
          }}
        />
      ) : null}
      {dialog?.type === 'delete' ? (
        <ConfirmDialog
          title={dialog.proxies.length === 1 ? 'Delete proxy' : 'Delete proxies'}
          message={
            dialog.proxies.length === 1
              ? `Delete "${dialog.proxies[0]!.name}"? Its history and queued checks are removed. This cannot be undone.`
              : `Delete ${dialog.proxies.length} proxies? Their history and queued checks are removed. This cannot be undone.`
          }
          confirmLabel={dialog.proxies.length === 1 ? 'Delete proxy' : `Delete ${dialog.proxies.length} proxies`}
          danger
          onClose={() => setDialog(null)}
          onConfirm={async () => {
            const ids = dialog.proxies.map((p) => p.id);
            for (const id of ids) await deleteProxy(id);
            setSelected((prev) => new Set([...prev].filter((id) => !ids.includes(id))));
            setDialog(null);
            await refresh();
          }}
        />
      ) : null}
    </section>
  );
}
