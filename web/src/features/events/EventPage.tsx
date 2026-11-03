import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import { ApiError } from '../../api/client';
import { listEvents, listProxies } from '../../api/endpoints';
import type { AppEvent } from '../../api/types';
import { formatDateTime, formatRelative } from '../../lib/format';
import { useVisiblePolling } from '../../lib/useVisiblePolling';
import { ExportDialog } from './ExportDialog';

const TELEGRAM_REASONS: Record<string, string> = {
  timeout: 'Telegram did not answer in time',
  network: 'Telegram could not be reached',
  rate_limited: 'Telegram asked to slow down',
  forbidden: 'The bot was blocked or cannot post to the chat',
  unauthorized: 'The bot token is not valid',
  not_found: 'The bot token was not recognized',
  bad_request: 'Telegram rejected the message or the chat ID',
  server_error: 'Telegram had a server error',
  rejected: 'Telegram rejected the message',
  invalid_response: 'Telegram sent an unexpected reply',
  canceled: 'Sending was interrupted',
};

export function describeDelivery(d: AppEvent['delivery']): string {
  const reason = d.last_error ? (TELEGRAM_REASONS[d.last_error] ?? 'Sending failed') : '';
  switch (d.status) {
    case 'delivered':
      return `Sent to Telegram ${formatRelative(d.delivered_at)}`;
    case 'failed':
      return `Not sent after ${d.attempts} attempts${reason ? `: ${reason}` : ''}`;
    case 'pending':
      return d.attempts > 0 ? `Waiting to retry, ${d.attempts} ${d.attempts === 1 ? 'attempt' : 'attempts'} so far${reason ? `: ${reason}` : ''}` : 'Waiting to send';
    default:
      return 'No notification';
  }
}

type KindFilter = 'all' | 'proxy_down' | 'proxy_recovered';

function uniqueEvents(events: AppEvent[]): AppEvent[] {
  const seen = new Set<number>();
  return events.filter((event) => {
    if (seen.has(event.id)) return false;
    seen.add(event.id);
    return true;
  });
}

export function EventPage() {
  const [kind, setKind] = useState<KindFilter>('all');
  const [proxyId, setProxyId] = useState<number | null>(null);
  const [proxies, setProxies] = useState<Array<{ id: number; name: string }>>([]);
  const [proxyError, setProxyError] = useState<string | null>(null);
  const [proxyReload, setProxyReload] = useState(0);
  const [exporting, setExporting] = useState(false);
  const [head, setHead] = useState<AppEvent[]>([]);
  const [tail, setTail] = useState<AppEvent[]>([]);
  const [cursor, setCursor] = useState<string | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [more, setMore] = useState(false);
  const loadMoreEpoch = useRef(0);
  const loadMoreController = useRef<AbortController | null>(null);
  const headRef = useRef(head);
  const tailRef = useRef(tail);
  headRef.current = head;
  tailRef.current = tail;

  const invalidateLoadMore = () => {
    loadMoreEpoch.current += 1;
    loadMoreController.current?.abort();
    loadMoreController.current = null;
    setMore(false);
  };

  const filters = useMemo(() => ({ kinds: kind === 'all' ? undefined : [kind], proxyId }), [kind, proxyId]);

  useEffect(() => {
    const ctl = new AbortController();
    setProxyError(null);
    listProxies(ctl.signal).then(
      (l) => setProxies(l.proxies.map((p) => ({ id: p.id, name: p.name }))),
      () => {
        if (!ctl.signal.aborted) setProxyError('Could not load proxies.');
      },
    );
    return () => ctl.abort();
  }, [proxyReload]);

  // Changing a filter starts the list over.
  useEffect(() => {
    invalidateLoadMore();
    setHead([]);
    setTail([]);
    setCursor(null);
    setLoaded(false);
    setMore(false);
  }, [filters]);

  useEffect(
    () => () => {
      loadMoreController.current?.abort();
    },
    [],
  );

  const refresh = useCallback(
    async (signal: AbortSignal) => {
      try {
        const page = await listEvents({ ...filters, cursor: null }, signal);
        if (signal.aborted) return;
        if (tailRef.current.length > 0) {
          const freshIDs = new Set(page.events.map((event) => event.id));
          const displaced = headRef.current.filter((event) => !freshIDs.has(event.id));
          setTail((previous) => uniqueEvents([...displaced, ...previous]).filter((event) => !freshIDs.has(event.id)));
        }
        setHead(page.events);
        setCursor((prev) => (tailRef.current.length === 0 ? page.next_cursor : prev));
        setLoaded(true);
        setError(null);
      } catch (err) {
        if (signal.aborted) return;
        setError(err instanceof ApiError ? err.message : 'Could not load events.');
      }
    },
    [filters],
  );
  useVisiblePolling(refresh, 5000, filters);

  const loadMore = async () => {
    if (cursor === null || loadMoreController.current !== null) return;
    const epoch = loadMoreEpoch.current;
    const controller = new AbortController();
    loadMoreController.current = controller;
    setMore(true);
    try {
      const page = await listEvents({ ...filters, cursor }, controller.signal);
      if (controller.signal.aborted || epoch !== loadMoreEpoch.current) return;
      setTail((prev) => uniqueEvents([...prev, ...page.events]));
      setCursor(page.next_cursor);
    } catch (err) {
      if (!controller.signal.aborted && epoch === loadMoreEpoch.current) {
        setError(err instanceof ApiError ? err.message : 'Could not load more events.');
      }
    } finally {
      if (loadMoreController.current === controller) {
        loadMoreController.current = null;
        if (epoch === loadMoreEpoch.current) setMore(false);
      }
    }
  };

  const events = useMemo(() => {
    const seen = new Set(head.map((e) => e.id));
    return [...head, ...tail.filter((e) => !seen.has(e.id))];
  }, [head, tail]);

  return (
    <section aria-labelledby="events-title">
      <div className="page-head">
        <div>
          <h1 id="events-title">Events</h1>
          <p className="muted">When a proxy went down or recovered, and whether the Telegram message was sent.</p>
        </div>
        <button type="button" className="button" onClick={() => setExporting(true)}>
          Export data
        </button>
      </div>

      <div className="toolbar">
        <div className="field">
          <label htmlFor="event-kind">Event type</label>
          <select id="event-kind" value={kind} onChange={(e) => { invalidateLoadMore(); setKind(e.target.value as KindFilter); }}>
            <option value="all">All events</option>
            <option value="proxy_down">Went down</option>
            <option value="proxy_recovered">Recovered</option>
          </select>
        </div>
        <div className="field">
          <label htmlFor="event-proxy">Proxy</label>
          <select id="event-proxy" value={proxyId ?? ''} onChange={(e) => { invalidateLoadMore(); setProxyId(e.target.value === '' ? null : Number(e.target.value)); }}>
            <option value="">All proxies</option>
            {proxies.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </select>
        </div>
      </div>

      {error ? (
        <p role="alert" className="notice notice--error">
          {error}
        </p>
      ) : null}

      {loaded && events.length === 0 ? (
        <div className="empty">
          <p className="empty__title">No events yet</p>
          <p>Events appear here when a proxy goes down or comes back.</p>
        </div>
      ) : null}

      {events.length > 0 ? (
        <div className="table-scroll">
          <table className="table">
            <caption className="visually-hidden">Events</caption>
            <thead>
              <tr>
                <th scope="col">Time</th>
                <th scope="col">Event</th>
                <th scope="col">Proxy</th>
                <th scope="col">Telegram</th>
              </tr>
            </thead>
            <tbody>
              {events.map((e) => (
                <tr key={e.id}>
                  <td data-label="Time" title={formatDateTime(e.created_at)}>
                    {formatDateTime(e.created_at)}
                  </td>
                  <td data-label="Event">
                    <span className={`chip ${e.kind === 'proxy_down' ? 'chip--down' : 'chip--up'}`}>{e.kind === 'proxy_down' ? 'Went down' : e.kind === 'proxy_recovered' ? 'Recovered' : e.kind}</span>
                    <div>{e.message}</div>
                  </td>
                  <td data-label="Proxy">
                    {e.proxy_id !== null && e.proxy_name ? <Link to={`/proxies/${e.proxy_id}`}>{e.proxy_name}</Link> : <span className="muted">Deleted proxy</span>}
                  </td>
                  <td data-label="Telegram" className={`delivery delivery--${e.delivery.status}`}>
                    {describeDelivery(e.delivery)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : null}

      {cursor !== null ? (
        <button type="button" className="button" disabled={more} onClick={() => void loadMore()}>
          Load more
        </button>
      ) : null}

      {exporting ? (
        <ExportDialog
          proxies={proxies}
          proxyError={proxyError}
          onRetryProxies={() => setProxyReload((n) => n + 1)}
          onClose={() => setExporting(false)}
        />
      ) : null}
    </section>
  );
}
