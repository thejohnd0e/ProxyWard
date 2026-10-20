import { useCallback, useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { ApiError } from '../../api/client';
import { listEvents, listProxies } from '../../api/endpoints';
import type { AppEvent } from '../../api/types';
import { formatDateTime, formatRelative } from '../../lib/format';
import { useVisiblePolling } from '../../lib/useVisiblePolling';

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

export function EventPage() {
  const [kind, setKind] = useState<KindFilter>('all');
  const [proxyId, setProxyId] = useState<number | null>(null);
  const [proxies, setProxies] = useState<Array<{ id: number; name: string }>>([]);
  const [head, setHead] = useState<AppEvent[]>([]);
  const [tail, setTail] = useState<AppEvent[]>([]);
  const [cursor, setCursor] = useState<string | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [more, setMore] = useState(false);

  const filters = useMemo(() => ({ kinds: kind === 'all' ? undefined : [kind], proxyId }), [kind, proxyId]);

  useEffect(() => {
    const ctl = new AbortController();
    listProxies(ctl.signal).then(
      (l) => setProxies(l.proxies.map((p) => ({ id: p.id, name: p.name }))),
      () => undefined,
    );
    return () => ctl.abort();
  }, []);

  // Changing a filter starts the list over.
  useEffect(() => {
    setHead([]);
    setTail([]);
    setCursor(null);
    setLoaded(false);
  }, [filters]);

  const refresh = useCallback(
    async (signal: AbortSignal) => {
      try {
        const page = await listEvents({ ...filters, cursor: null }, signal);
        if (signal.aborted) return;
        setHead(page.events);
        setCursor((prev) => (tail.length === 0 ? page.next_cursor : prev));
        setLoaded(true);
        setError(null);
      } catch (err) {
        if (signal.aborted) return;
        setError(err instanceof ApiError ? err.message : 'Could not load events.');
      }
    },
    [filters, tail.length],
  );
  useVisiblePolling(refresh, 5000, filters);

  const loadMore = async () => {
    if (cursor === null) return;
    setMore(true);
    try {
      const page = await listEvents({ ...filters, cursor });
      setTail((prev) => [...prev, ...page.events]);
      setCursor(page.next_cursor);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Could not load more events.');
    } finally {
      setMore(false);
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
      </div>

      <div className="toolbar">
        <div className="field">
          <label htmlFor="event-kind">Event type</label>
          <select id="event-kind" value={kind} onChange={(e) => setKind(e.target.value as KindFilter)}>
            <option value="all">All events</option>
            <option value="proxy_down">Went down</option>
            <option value="proxy_recovered">Recovered</option>
          </select>
        </div>
        <div className="field">
          <label htmlFor="event-proxy">Proxy</label>
          <select id="event-proxy" value={proxyId ?? ''} onChange={(e) => setProxyId(e.target.value === '' ? null : Number(e.target.value))}>
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
    </section>
  );
}
