import { useCallback, useEffect, useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { ApiError } from '../../api/client';
import { availabilityChart, deleteProxy, enqueueChecks, getProxy, speedChart } from '../../api/endpoints';
import type { AvailabilityPoint, Chart, CheckKind, Proxy, SpeedPoint } from '../../api/types';
import { ConfirmDialog } from '../../components/ConfirmDialog';
import { Flag } from '../../components/Flag';
import { StatusBadge } from '../../components/StatusBadge';
import { formatDateTime, formatDuration, formatPercent, formatRelative } from '../../lib/format';
import { useVisiblePolling } from '../../lib/useVisiblePolling';
import { AvailabilityHistory, SpeedHistory } from '../history/HistoryTable';
import { LineChart, StateStrip } from '../history/charts';
import { ProxyDialog } from './ProxyDialog';

type Range = '24h' | '7d' | '30d';

const RANGES: Record<Range, { label: string; ms: number; points: number }> = {
  '24h': { label: 'Last 24 hours', ms: 24 * 3600_000, points: 96 },
  '7d': { label: 'Last 7 days', ms: 7 * 24 * 3600_000, points: 168 },
  '30d': { label: 'Last 30 days', ms: 30 * 24 * 3600_000, points: 120 },
};

function Fact({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="fact">
      <dt>{label}</dt>
      <dd>{children}</dd>
    </div>
  );
}

export function ProxyDetail() {
  const { id: idParam } = useParams();
  const id = Number(idParam);
  const navigate = useNavigate();
  const [proxy, setProxy] = useState<Proxy | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [notFound, setNotFound] = useState(false);
  const [range, setRange] = useState<Range>('24h');
  const [avail, setAvail] = useState<Chart<AvailabilityPoint> | null>(null);
  const [speed, setSpeed] = useState<Chart<SpeedPoint> | null>(null);
  const [tab, setTab] = useState<'availability' | 'speed'>('availability');
  const [dialog, setDialog] = useState<'edit' | 'delete' | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const refresh = useCallback(
    async (signal?: AbortSignal) => {
      try {
        setProxy(await getProxy(id, signal));
        setError(null);
      } catch (err) {
        if (signal?.aborted) return;
        if (err instanceof ApiError && err.status === 404) setNotFound(true);
        else setError(err instanceof ApiError ? err.message : 'Could not load the proxy.');
      }
    },
    [id],
  );
  useVisiblePolling(refresh, 5000, id);

  // Moving to another proxy must not show the previous one while loading.
  useEffect(() => {
    setProxy(null);
    setNotFound(false);
    setAvail(null);
    setSpeed(null);
  }, [id]);

  const refreshKey = `${proxy?.status.checked_at ?? ''}|${proxy?.speed?.checked_at ?? ''}`;

  useEffect(() => {
    if (!Number.isInteger(id) || id <= 0) return;
    const ctl = new AbortController();
    const to = new Date();
    const from = new Date(to.getTime() - RANGES[range].ms);
    const pts = RANGES[range].points;
    availabilityChart(id, from, to, pts, ctl.signal).then(setAvail, () => undefined);
    speedChart(id, from, to, pts, ctl.signal).then(setSpeed, () => undefined);
    return () => ctl.abort();
  }, [id, range, refreshKey]);

  if (!Number.isInteger(id) || id <= 0 || notFound) {
    return (
      <section>
        <h1>Proxy not found</h1>
        <p>This proxy does not exist, or it was deleted.</p>
        <Link to="/proxies">Back to proxies</Link>
      </section>
    );
  }
  if (!proxy) {
    return error ? (
      <p role="alert" className="notice notice--error">
        {error}
      </p>
    ) : (
      <p className="loading" role="status">
        Loading…
      </p>
    );
  }

  const busy = (kind: CheckKind) => proxy.active_jobs.find((j) => j.kind === kind);
  const run = async (kind: CheckKind) => {
    setNotice(null);
    try {
      await enqueueChecks(kind, { proxyIds: [proxy.id] });
      await refresh();
    } catch (err) {
      setNotice(err instanceof ApiError ? err.message : 'Could not queue the check.');
    }
  };
  const g = proxy.geo;
  const place = g ? [g.city, g.country].filter((s) => s !== '').join(', ') : '';

  return (
    <section aria-labelledby="detail-title">
      <p>
        <Link to="/proxies">Back to proxies</Link>
      </p>
      <div className="page-head">
        <div>
          <h1 id="detail-title">{proxy.name}</h1>
          <p className="mono muted">
            {proxy.protocol}://{proxy.host}:{proxy.port}
          </p>
          <StatusBadge state={proxy.status.state} />{' '}
          <span className="muted" title={formatDateTime(proxy.status.checked_at)}>
            {proxy.status.checked_at ? `checked ${formatRelative(proxy.status.checked_at)}` : 'never checked'}
          </span>
          {proxy.status.failure ? <p className="failure">{proxy.status.failure.message}</p> : null}
        </div>
        <div className="page-actions">
          <button type="button" className="button" disabled={busy('availability') !== undefined} onClick={() => void run('availability')}>
            {busy('availability') ? (busy('availability')!.status === 'running' ? 'Checking' : 'Check queued') : 'Check availability'}
          </button>
          <button type="button" className="button" disabled={busy('speed') !== undefined} onClick={() => void run('speed')}>
            {busy('speed') ? (busy('speed')!.status === 'running' ? 'Testing speed' : 'Speed test queued') : 'Test speed'}
          </button>
          <button type="button" className="button" onClick={() => setDialog('edit')}>
            Edit
          </button>
          <button type="button" className="button button--danger" onClick={() => setDialog('delete')}>
            Delete
          </button>
        </div>
      </div>

      {notice ? (
        <p role="alert" className="notice notice--error">
          {notice}
        </p>
      ) : null}
      {error ? (
        <p role="alert" className="notice notice--error">
          Showing the last loaded data. {error}
        </p>
      ) : null}

      <dl className="facts">
        <Fact label="Exit IP">{proxy.status.exit_ip ? <span className="mono">{proxy.status.exit_ip}</span> : '—'}</Fact>
        <Fact label="Location">
          {g ? (
            <span className="location">
              {g.country_code ? <Flag code={g.country_code} /> : null}
              {place || 'Unknown'}
              {g.stale ? <span className="chip chip--warn">stale</span> : null}
            </span>
          ) : (
            <span className="muted">Unknown</span>
          )}
        </Fact>
        <Fact label="Network">{g && (g.asn !== null || g.organization) ? [g.asn !== null ? `AS${g.asn}` : '', g.organization].filter(Boolean).join(' ') : '—'}</Fact>
        <Fact label="Latency">{proxy.status.state === 'up' ? formatDuration(proxy.status.duration_ms) : '—'}</Fact>
        <Fact label="Uptime, 24 h">
          {proxy.uptime.percent === null ? '—' : `${formatPercent(proxy.uptime.percent)} of ${proxy.uptime.samples} ${proxy.uptime.samples === 1 ? 'check' : 'checks'}`}
        </Fact>
        <Fact label="Next availability check">{proxy.next_runs.availability ? formatDateTime(proxy.next_runs.availability) : 'Not scheduled'}</Fact>
        <Fact label="Next speed test">{proxy.next_runs.speed ? formatDateTime(proxy.next_runs.speed) : 'Not scheduled'}</Fact>
        <Fact label="Authentication">{proxy.auth_enabled ? `User ${proxy.username}${proxy.has_password ? ', password set' : ', no password'}` : 'None'}</Fact>
      </dl>

      <div className="range" role="group" aria-label="Time range">
        {(Object.keys(RANGES) as Range[]).map((r) => (
          <button key={r} type="button" className={`button button--small${r === range ? ' button--selected' : ''}`} aria-pressed={r === range} onClick={() => setRange(r)}>
            {RANGES[r].label}
          </button>
        ))}
      </div>

      <div className="charts">
        {avail ? <StateStrip points={avail.points} /> : null}
        {avail ? (
          <LineChart
            title="Latency of successful checks"
            unit="ms"
            times={avail.points.map((p) => p.t)}
            series={[{ name: 'Latency', values: avail.points.map((p) => (p.avg_duration_ms === null ? null : p.avg_duration_ms)) }]}
          />
        ) : null}
        {speed ? (
          <LineChart
            title="Speed"
            unit="Mbps"
            times={speed.points.map((p) => p.t)}
            series={[
              { name: 'Download', values: speed.points.map((p) => p.download_mbps) },
              { name: 'Upload', values: speed.points.map((p) => p.upload_mbps) },
            ]}
          />
        ) : null}
      </div>

      <div className="tabs" role="tablist" aria-label="History">
        <button type="button" role="tab" aria-selected={tab === 'availability'} className={tab === 'availability' ? 'tab tab--active' : 'tab'} onClick={() => setTab('availability')}>
          Availability history
        </button>
        <button type="button" role="tab" aria-selected={tab === 'speed'} className={tab === 'speed' ? 'tab tab--active' : 'tab'} onClick={() => setTab('speed')}>
          Speed history
        </button>
      </div>
      <div role="tabpanel">
        {tab === 'availability' ? <AvailabilityHistory proxyId={proxy.id} refreshKey={refreshKey} /> : <SpeedHistory proxyId={proxy.id} refreshKey={refreshKey} />}
      </div>

      {dialog === 'edit' ? (
        <ProxyDialog
          proxy={proxy}
          onClose={() => setDialog(null)}
          onSaved={() => {
            setDialog(null);
            void refresh();
          }}
        />
      ) : null}
      {dialog === 'delete' ? (
        <ConfirmDialog
          title="Delete proxy"
          message={`Delete "${proxy.name}"? Its history and queued checks are removed. This cannot be undone.`}
          confirmLabel="Delete proxy"
          danger
          onClose={() => setDialog(null)}
          onConfirm={async () => {
            await deleteProxy(proxy.id);
            navigate('/proxies');
          }}
        />
      ) : null}
    </section>
  );
}
