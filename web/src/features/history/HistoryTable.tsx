import { useCallback, useEffect, useRef, useState } from 'react';
import { ApiError } from '../../api/client';
import { availabilityHistory, speedHistory } from '../../api/endpoints';
import type { AvailabilityItem, HistoryPage, SpeedItem } from '../../api/types';
import { SpeedValue } from '../../components/SpeedValue';
import { StatusBadge } from '../../components/StatusBadge';
import { formatBytes, formatDateTime, formatDuration } from '../../lib/format';

type Fetcher<T> = (id: number, cursor: string | null, signal?: AbortSignal) => Promise<HistoryPage<T>>;

function usePagedHistory<T>(proxyId: number, fetcher: Fetcher<T>, refreshKey: string) {
  const [items, setItems] = useState<T[]>([]);
  const [cursor, setCursor] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [loaded, setLoaded] = useState(false);
  const generation = useRef(0);

  const load = useCallback(
    async (more: boolean, from: string | null) => {
      const gen = ++generation.current;
      setLoading(true);
      setError(null);
      try {
        const page = await fetcher(proxyId, more ? from : null);
        if (gen !== generation.current) return;
        setItems((prev) => (more ? [...prev, ...page.items] : page.items));
        setCursor(page.next_cursor);
        setLoaded(true);
      } catch (err) {
        if (gen !== generation.current) return;
        setError(err instanceof ApiError ? err.message : 'Could not load the history.');
      } finally {
        if (gen === generation.current) setLoading(false);
      }
    },
    [proxyId, fetcher],
  );

  // A new result for this proxy restarts the list from the newest entry.
  useEffect(() => {
    void load(false, null);
    return () => {
      generation.current++;
    };
  }, [load, refreshKey]);

  return { items, loading, error, loaded, hasMore: cursor !== null, more: () => void load(true, cursor) };
}

function Footer({ hook, noun }: { hook: { loading: boolean; error: string | null; loaded: boolean; hasMore: boolean; more: () => void; items: unknown[] }; noun: string }) {
  return (
    <>
      {hook.error ? (
        <p role="alert" className="notice notice--error">
          {hook.error}
        </p>
      ) : null}
      {hook.loaded && hook.items.length === 0 && !hook.error ? <p className="muted">No {noun} recorded yet.</p> : null}
      {hook.hasMore ? (
        <button type="button" className="button" disabled={hook.loading} onClick={hook.more}>
          Load more
        </button>
      ) : null}
    </>
  );
}

export function AvailabilityHistory({ proxyId, refreshKey }: { proxyId: number; refreshKey: string }) {
  const hook = usePagedHistory<AvailabilityItem>(proxyId, availabilityHistory, refreshKey);
  return (
    <div>
      {hook.items.length > 0 ? (
        <div className="table-scroll">
          <table className="table table--compact">
            <caption className="visually-hidden">Availability history</caption>
            <thead>
              <tr>
                <th scope="col">Time</th>
                <th scope="col">State</th>
                <th scope="col">Latency</th>
                <th scope="col">Exit IP</th>
                <th scope="col">Checked with</th>
                <th scope="col">Details</th>
              </tr>
            </thead>
            <tbody>
              {hook.items.map((r) => (
                <tr key={r.id}>
                  <td>{formatDateTime(r.checked_at)}</td>
                  <td>
                    <StatusBadge state={r.state} />
                  </td>
                  <td className="num">{r.state === 'up' ? formatDuration(r.duration_ms) : '—'}</td>
                  <td className="mono">{r.exit_ip || '—'}</td>
                  <td>{r.endpoint || '—'}</td>
                  <td>{r.failure ? r.failure.message : ''}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : null}
      <Footer hook={hook} noun="availability checks" />
    </div>
  );
}

export function SpeedHistory({ proxyId, refreshKey }: { proxyId: number; refreshKey: string }) {
  const hook = usePagedHistory<SpeedItem>(proxyId, speedHistory, refreshKey);
  return (
    <div>
      {hook.items.length > 0 ? (
        <div className="table-scroll">
          <table className="table table--compact">
            <caption className="visually-hidden">Speed test history</caption>
            <thead>
              <tr>
                <th scope="col">Time</th>
                <th scope="col">Download</th>
                <th scope="col">Upload</th>
                <th scope="col">Data used</th>
                <th scope="col">Service</th>
              </tr>
            </thead>
            <tbody>
              {hook.items.map((r) => (
                <tr key={r.id}>
                  <td>{formatDateTime(r.checked_at)}</td>
                  <td>
                    <SpeedValue mbps={r.download.mbps} lowConfidence={r.download.low_confidence} failure={r.download.failure} />
                    {r.download.failure ? <div className="failure">{r.download.failure.message}</div> : null}
                  </td>
                  <td>
                    <SpeedValue mbps={r.upload.mbps} lowConfidence={r.upload.low_confidence} failure={r.upload.failure} />
                    {r.upload.failure ? <div className="failure">{r.upload.failure.message}</div> : null}
                  </td>
                  <td className="num">{formatBytes(r.total_bytes)}</td>
                  <td>{r.provider}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : null}
      <Footer hook={hook} noun="speed tests" />
    </div>
  );
}
