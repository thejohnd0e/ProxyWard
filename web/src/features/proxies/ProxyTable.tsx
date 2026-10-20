import { Link } from 'react-router-dom';
import type { ActiveJob, CheckKind, Proxy } from '../../api/types';
import { Flag } from '../../components/Flag';
import { SpeedValue } from '../../components/SpeedValue';
import { StatusBadge } from '../../components/StatusBadge';
import { formatDateTime, formatDuration, formatPercent, formatRelative } from '../../lib/format';

interface Props {
  proxies: Proxy[];
  /** Whether a geolocation key is set; without one the location is "Not configured". */
  geoConfigured: boolean;
  selected: Set<number>;
  onToggle: (id: number, on: boolean) => void;
  onToggleAll: (on: boolean) => void;
  onCheck: (kind: CheckKind, id: number) => void;
  onEdit: (proxy: Proxy) => void;
  onDelete: (proxy: Proxy) => void;
}

function jobFor(p: Proxy, kind: CheckKind): ActiveJob | undefined {
  return p.active_jobs.find((j) => j.kind === kind);
}

function Location({ proxy, geoConfigured }: { proxy: Proxy; geoConfigured: boolean }) {
  const g = proxy.geo;
  if (!g) {
    return <span className="muted">{geoConfigured ? 'Unknown' : 'Not configured'}</span>;
  }
  const place = [g.city, g.country].filter((s) => s !== '').join(', ');
  return (
    <span className="location">
      {g.country_code ? <Flag code={g.country_code} /> : null}
      <span>{place || 'Unknown'}</span>
      {g.stale ? (
        <span className="chip chip--warn" title={`Last updated ${formatDateTime(g.fetched_at)}. A refresh was not possible.`}>
          stale
        </span>
      ) : null}
    </span>
  );
}

function Network({ proxy }: { proxy: Proxy }) {
  const g = proxy.geo;
  if (!g) return <span className="muted">—</span>;
  const asn = g.asn !== null ? `AS${g.asn}` : '';
  const text = [asn, g.organization].filter((s) => s !== '').join(' ');
  return <span>{text || 'Unknown'}</span>;
}

function CheckButton({ proxy, kind, onCheck }: { proxy: Proxy; kind: CheckKind; onCheck: Props['onCheck'] }) {
  const job = jobFor(proxy, kind);
  const verb = kind === 'availability' ? 'Check availability' : 'Test speed';
  const short = kind === 'availability' ? 'Check' : 'Speed';
  return (
    <button
      type="button"
      className="button button--small"
      disabled={job !== undefined}
      aria-label={`${verb} of ${proxy.name}`}
      onClick={() => onCheck(kind, proxy.id)}
    >
      {job ? (job.status === 'running' ? 'Running' : 'Queued') : short}
    </button>
  );
}

export function ProxyTable({ proxies, geoConfigured, selected, onToggle, onToggleAll, onCheck, onEdit, onDelete }: Props) {
  if (proxies.length === 0) {
    return (
      <div className="empty">
        <p className="empty__title">No proxies yet</p>
        <p>Add a proxy or import a list to start checking.</p>
      </div>
    );
  }
  const all = proxies.every((p) => selected.has(p.id));
  return (
    <div className="table-scroll">
      <table className="table proxy-table">
        <caption className="visually-hidden">Proxies</caption>
        <thead>
          <tr>
            <th scope="col" className="col-select">
              <input type="checkbox" aria-label="Select all proxies" checked={all} onChange={(e) => onToggleAll(e.target.checked)} />
            </th>
            <th scope="col">Proxy</th>
            <th scope="col">Status</th>
            <th scope="col">Exit IP</th>
            <th scope="col">Location</th>
            <th scope="col">Network</th>
            <th scope="col">Latency</th>
            <th scope="col">Speed</th>
            <th scope="col">Uptime, 24 h</th>
            <th scope="col" className="col-actions">
              Actions
            </th>
          </tr>
        </thead>
        <tbody>
          {proxies.map((p) => (
            <tr key={p.id} className={selected.has(p.id) ? 'row--selected' : undefined}>
              <td className="col-select" data-label="">
                <input type="checkbox" aria-label={`Select ${p.name}`} checked={selected.has(p.id)} onChange={(e) => onToggle(p.id, e.target.checked)} />
              </td>
              <td data-label="Proxy" className="cell-proxy">
                <Link to={`/proxies/${p.id}`} className="proxy-name">
                  {p.name}
                </Link>
                <div className="mono muted">
                  {p.protocol}://{p.host}:{p.port}
                </div>
                {p.auth_enabled ? (
                  <div className="muted">
                    User {p.username}
                    {p.has_password ? ', password set' : ', no password'}
                    <span className="visually-hidden">. Password set: {p.has_password ? 'yes' : 'no'}</span>
                  </div>
                ) : null}
                {p.tags.length > 0 ? (
                  <ul className="tags" aria-label="Tags">
                    {p.tags.map((t) => (
                      <li key={t} className="chip">
                        {t}
                      </li>
                    ))}
                  </ul>
                ) : null}
              </td>
              <td data-label="Status">
                <StatusBadge state={p.status.state} />
                <div className="muted" title={p.status.checked_at ? formatDateTime(p.status.checked_at) : undefined}>
                  {p.status.checked_at ? formatRelative(p.status.checked_at) : 'Never checked'}
                </div>
                {p.status.failure ? <div className="failure">{p.status.failure.message}</div> : null}
              </td>
              <td data-label="Exit IP" aria-label="Exit IP" title="The public address the internet sees when traffic goes through this proxy">
                {p.status.exit_ip ? <span className="mono">{p.status.exit_ip}</span> : '—'}
              </td>
              <td data-label="Location">
                <Location proxy={p} geoConfigured={geoConfigured} />
              </td>
              <td data-label="Network" aria-label="Network">
                <Network proxy={p} />
              </td>
              <td data-label="Latency" className="num">
                {p.status.state === 'up' ? formatDuration(p.status.duration_ms) : '—'}
              </td>
              <td data-label="Speed">
                <div aria-label="Download speed">
                  <span className="muted">Download </span>
                  <SpeedValue mbps={p.speed?.download.mbps ?? null} lowConfidence={p.speed?.download.low_confidence ?? false} failure={p.speed?.download.failure ?? null} />
                </div>
                <div aria-label="Upload speed">
                  <span className="muted">Upload </span>
                  <SpeedValue mbps={p.speed?.upload.mbps ?? null} lowConfidence={p.speed?.upload.low_confidence ?? false} failure={p.speed?.upload.failure ?? null} />
                </div>
              </td>
              <td data-label="Uptime, 24 h">
                {p.uptime.percent === null ? (
                  <span className="muted">—</span>
                ) : (
                  <span title="Share of definite checks (up or down) in the last 24 hours">
                    {formatPercent(p.uptime.percent)} of {p.uptime.samples} {p.uptime.samples === 1 ? 'check' : 'checks'}
                  </span>
                )}
              </td>
              <td data-label="Actions" className="col-actions">
                <div className="actions">
                  <CheckButton proxy={p} kind="availability" onCheck={onCheck} />
                  <CheckButton proxy={p} kind="speed" onCheck={onCheck} />
                  <button type="button" className="button button--small" aria-label={`Edit ${p.name}`} onClick={() => onEdit(p)}>
                    Edit
                  </button>
                  <button type="button" className="button button--small button--danger" aria-label={`Delete ${p.name}`} onClick={() => onDelete(p)}>
                    Delete
                  </button>
                </div>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
