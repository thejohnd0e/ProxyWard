import { useCallback, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { ApiError } from '../../api/client';
import { listProxies } from '../../api/endpoints';
import type { Proxy, ProxyList, State } from '../../api/types';
import { Flag } from '../../components/Flag';
import { StatusBadge } from '../../components/StatusBadge';
import { formatDuration, formatPercent, formatRelative } from '../../lib/format';
import { useVisiblePolling } from '../../lib/useVisiblePolling';

const DAY_MS = 24 * 3600_000;

function errorText(err: unknown): string {
  return err instanceof ApiError ? err.message : 'Something went wrong. Try again.';
}

function placeOf(proxy: Proxy): string {
  const g = proxy.geo;
  if (!g) return 'Unknown';
  return [g.city, g.country].filter((value) => value !== '').join(', ') || 'Unknown';
}

function checkedWithinDay(proxy: Proxy): boolean {
  if (!proxy.status.checked_at) return false;
  return Date.now() - new Date(proxy.status.checked_at).getTime() <= DAY_MS;
}

function urgency(proxy: Proxy): number {
  if (proxy.status.state === 'down') return 0;
  if (proxy.status.state === 'indeterminate') return 1;
  if (proxy.status.state === 'unknown') return 2;
  return 3;
}

export function OverviewPage() {
  const [data, setData] = useState<ProxyList | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);

  const refresh = useCallback(async (signal?: AbortSignal) => {
    try {
      setData(await listProxies(signal));
      setLoadError(null);
    } catch (err) {
      if (signal?.aborted) return;
      setLoadError(errorText(err));
    }
  }, []);

  useVisiblePolling(refresh, 5000);

  const proxies = data?.proxies ?? [];
  const counts = useMemo(() => {
    const result: Record<State, number> = { up: 0, down: 0, indeterminate: 0, unknown: 0 };
    for (const proxy of proxies) result[proxy.status.state]++;
    return result;
  }, [proxies]);

  const paused = proxies.filter((proxy) => proxy.monitoring_paused).length;
  const queued = proxies.reduce((total, proxy) => total + proxy.active_jobs.length, 0);
  const checkedToday = proxies.filter(checkedWithinDay).length;
  const measured = proxies.filter((proxy) => proxy.uptime.percent !== null);
  const meanUptime =
    measured.length === 0 ? null : measured.reduce((total, proxy) => total + (proxy.uptime.percent ?? 0), 0) / measured.length;

  const attention = useMemo(
    () =>
      proxies
        .filter((proxy) => !proxy.monitoring_paused && (proxy.status.state === 'down' || proxy.status.state === 'indeterminate'))
        .sort((a, b) => urgency(a) - urgency(b) || a.name.localeCompare(b.name)),
    [proxies],
  );

  const coverage = useMemo(
    () => [...proxies].sort((a, b) => urgency(a) - urgency(b) || a.name.localeCompare(b.name)),
    [proxies],
  );

  return (
    <section aria-labelledby="overview-title">
      <div className="page-head overview__head">
        <div>
          <p className="eyebrow">Operations workspace</p>
          <h1 id="overview-title">Overview</h1>
          <p className="muted">
            {data
              ? `${proxies.length} ${proxies.length === 1 ? 'proxy' : 'proxies'} · ${checkedToday} checked in the last 24 hours`
              : 'Loading monitoring evidence…'}
          </p>
        </div>
        <div className="page-actions">
          <Link className="button button--primary" to="/proxies">
            Open proxies
          </Link>
        </div>
      </div>

      {loadError ? (
        <div role="alert" className="notice notice--error">
          <p>
            {data ? 'Showing the last loaded data. ' : ''}
            {loadError}
          </p>
          <button type="button" className="button button--small" onClick={() => void refresh()}>
            Retry
          </button>
        </div>
      ) : null}

      {data === null ? (
        loadError ? null : (
          <p className="loading" role="status">
            Loading overview…
          </p>
        )
      ) : (
        <>
          <div className="health-band">
            <div className="stat stat--accent">
              <span className="stat__label">Monitored</span>
              <span className="stat__value">{proxies.length}</span>
              <span className="stat__note">
                {proxies.length === 0 ? 'No proxies yet' : queued === 0 ? 'No checks queued' : `${queued} check${queued === 1 ? '' : 's'} queued`}
              </span>
            </div>
            <div className="stat stat--up">
              <span className="stat__label">Up</span>
              <span className="stat__value">{counts.up}</span>
              <span className="stat__note">Passing the latest check</span>
            </div>
            <div className="stat stat--down">
              <span className="stat__label">Down</span>
              <span className="stat__value">{counts.down}</span>
              <span className="stat__note">{counts.down === 0 ? 'No confirmed failures' : 'Need inspection'}</span>
            </div>
            <div className="stat stat--warn">
              <span className="stat__label">Unverified</span>
              <span className="stat__value">{counts.indeterminate + counts.unknown}</span>
              <span className="stat__note">
                {counts.indeterminate} indeterminate · {counts.unknown} never checked
              </span>
            </div>
            <div className="stat">
              <span className="stat__label">Paused</span>
              <span className="stat__value">{paused}</span>
              <span className="stat__note">Manual checks only</span>
            </div>
          </div>

          <div className="overview-grid">
            <section className="panel" aria-labelledby="coverage-title">
              <div className="panel__head">
                <h2 id="coverage-title">Observation coverage</h2>
                <span className="panel__meta">
                  {meanUptime === null ? 'No measured uptime' : `Mean ${formatPercent(meanUptime)} · ${measured.length} measured`}
                </span>
              </div>
              <div className="panel__body">
                {coverage.length === 0 ? (
                  <p className="all-clear">No proxies to cover yet.</p>
                ) : (
                  coverage.map((proxy) => (
                    <div key={proxy.id} className="coverage-row">
                      <div className="coverage-row__identity">
                        <Link className="coverage-row__name" to={`/proxies/${proxy.id}`}>
                          {proxy.name}
                        </Link>
                        <span className="coverage-row__meta">
                          {proxy.monitoring_paused ? 'Monitoring paused' : proxy.status.checked_at ? formatRelative(proxy.status.checked_at) : 'Never checked'}
                        </span>
                      </div>
                      <div
                        className="uptime-bar"
                        role="img"
                        aria-label={
                          proxy.uptime.percent === null
                            ? `${proxy.name}: no measured uptime`
                            : `${proxy.name}: ${formatPercent(proxy.uptime.percent)} uptime over ${proxy.uptime.samples} checks`
                        }
                      >
                        <div
                          className={`uptime-bar__fill${
                            proxy.uptime.percent === null
                              ? ' uptime-bar__fill--none'
                              : proxy.uptime.percent < 50
                                ? ' uptime-bar__fill--down'
                                : proxy.uptime.percent < 90
                                  ? ' uptime-bar__fill--warn'
                                  : ''
                          }`}
                          style={{ width: proxy.uptime.percent === null ? '100%' : `${Math.max(2, Math.min(100, proxy.uptime.percent))}%` }}
                        />
                      </div>
                      <span className="coverage-row__value">
                        {proxy.uptime.percent === null ? 'No data' : formatPercent(proxy.uptime.percent)}
                      </span>
                    </div>
                  ))
                )}
              </div>
            </section>

            <section className="panel" aria-labelledby="attention-title">
              <div className="panel__head">
                <h2 id="attention-title">Needs attention</h2>
                <span className="panel__meta">
                  {attention.length === 0 ? 'All clear' : `${attention.length} ${attention.length === 1 ? 'proxy' : 'proxies'}`}
                </span>
              </div>
              {attention.length === 0 ? (
                <div className="all-clear">
                  <span className="all-clear__mark" aria-hidden="true">
                    ✓
                  </span>
                  <span>No confirmed failures or inconclusive checks.</span>
                </div>
              ) : (
                <ul className="problem-list">
                  {attention.map((proxy) => (
                    <li key={proxy.id} className="problem">
                      <div className="problem__body">
                        <div className="problem__title">
                          <Link to={`/proxies/${proxy.id}`}>{proxy.name}</Link>{' '}
                          <StatusBadge state={proxy.status.state} />
                        </div>
                        <div className="problem__message">
                          {proxy.status.failure?.message ?? 'The check did not complete; inspect the proxy.'}
                        </div>
                      </div>
                      <span className="problem__time">
                        {proxy.status.checked_at ? formatRelative(proxy.status.checked_at) : 'Never'}
                      </span>
                    </li>
                  ))}
                </ul>
              )}
            </section>
          </div>

          <section className="panel" aria-labelledby="fleet-title">
            <div className="panel__head">
              <h2 id="fleet-title">Fleet</h2>
              <span className="panel__meta">Latest evidence per proxy</span>
            </div>
            {proxies.length === 0 ? (
              <div className="all-clear">
                <span>No proxies yet. Add one to start collecting evidence.</span>
              </div>
            ) : (
              <div className="table-scroll" style={{ border: 0, borderRadius: 0, marginBottom: 0, boxShadow: 'none' }}>
                <table className="table overview-table">
                  <caption className="visually-hidden">Proxies at a glance</caption>
                  <thead>
                    <tr>
                      <th scope="col">Proxy</th>
                      <th scope="col">Status</th>
                      <th scope="col">Location</th>
                      <th scope="col">Latency</th>
                      <th scope="col">Uptime, 24 h</th>
                      <th scope="col">Last check</th>
                    </tr>
                  </thead>
                  <tbody>
                    {coverage.map((proxy) => (
                      <tr key={proxy.id}>
                        <td>
                          <Link to={`/proxies/${proxy.id}`} className="proxy-name">
                            {proxy.name}
                          </Link>
                          <div className="mono muted">
                            {proxy.protocol}://{proxy.host}:{proxy.port}
                          </div>
                        </td>
                        <td>
                          {proxy.monitoring_paused ? (
                            <span className="status status--unknown">Paused</span>
                          ) : (
                            <StatusBadge state={proxy.status.state} />
                          )}
                        </td>
                        <td>
                          <span className="location">
                            {proxy.geo?.country_code ? <Flag code={proxy.geo.country_code} /> : null}
                            {placeOf(proxy)}
                          </span>
                        </td>
                        <td className="num">{proxy.status.state === 'up' ? formatDuration(proxy.status.duration_ms) : '—'}</td>
                        <td>
                          <div className="table-cell-stack">
                            <div className="mini-bar" aria-hidden="true">
                              <div
                                className={`mini-bar__fill${
                                  proxy.uptime.percent === null || proxy.uptime.percent >= 90
                                    ? ''
                                    : proxy.uptime.percent >= 50
                                      ? ' mini-bar__fill--warn'
                                      : ' mini-bar__fill--down'
                                }`}
                                style={{ width: proxy.uptime.percent === null ? '0%' : `${Math.max(3, Math.min(100, proxy.uptime.percent))}%` }}
                              />
                            </div>
                            <span className="muted">
                              {proxy.uptime.percent === null
                                ? 'No data'
                                : `${formatPercent(proxy.uptime.percent)} of ${proxy.uptime.samples} checks`}
                            </span>
                          </div>
                        </td>
                        <td className="num">{proxy.status.checked_at ? formatRelative(proxy.status.checked_at) : 'Never'}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </section>
        </>
      )}
    </section>
  );
}
