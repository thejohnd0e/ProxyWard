import type { Proxy, State } from '../../api/types';

export type SortKey = 'name' | 'status' | 'exit_ip' | 'location' | 'network' | 'latency' | 'speed' | 'uptime';
export type SortDir = 'asc' | 'desc';
export interface Sort {
  key: SortKey;
  dir: SortDir;
}

export const SORT_LABELS: Record<SortKey, string> = {
  name: 'Proxy',
  status: 'Status',
  exit_ip: 'Exit IP',
  location: 'Location',
  network: 'Network',
  latency: 'Latency',
  speed: 'Download speed',
  uptime: 'Uptime',
};

/** Problems first when ascending. */
const STATE_ORDER: Record<State, number> = { down: 0, indeterminate: 1, unknown: 2, up: 3 };

/** IPv4 addresses sort by value and come before IPv6 ones. */
function ipKey(ip: string): string {
  if (/^\d+\.\d+\.\d+\.\d+$/.test(ip)) return ip.split('.').map((o) => o.padStart(3, '0')).join('.');
  return `z${ip}`;
}

/** The value a column sorts by; null means "no data", which always sorts last. */
function value(p: Proxy, key: SortKey): string | number | null {
  switch (key) {
    case 'name':
      return p.name;
    case 'status':
      return STATE_ORDER[p.status.state];
    case 'exit_ip':
      return p.status.exit_ip ? ipKey(p.status.exit_ip) : null;
    case 'location': {
      const g = p.geo;
      const text = g ? (g.country || g.country_code) : '';
      return text ? `${text}, ${g?.city ?? ''}` : null;
    }
    case 'network':
      return p.geo?.asn ?? null;
    case 'latency':
      return p.status.state === 'up' ? p.status.duration_ms : null;
    case 'speed':
      return p.speed?.download.mbps ?? null;
    case 'uptime':
      return p.uptime.percent;
  }
}

function order(a: string | number, b: string | number): number {
  if (typeof a === 'number' && typeof b === 'number') return a - b;
  return String(a).localeCompare(String(b), 'en', { numeric: true });
}

export function compareProxies(a: Proxy, b: Proxy, { key, dir }: Sort): number {
  const va = value(a, key);
  const vb = value(b, key);
  if (va !== null && vb !== null) {
    const d = order(va, vb);
    if (d !== 0) return dir === 'asc' ? d : -d;
  } else if (va !== vb) {
    return va === null ? 1 : -1;
  }
  return a.name.localeCompare(b.name, 'en', { numeric: true });
}

/** Clicking the sorted column flips the direction; another column starts ascending. */
export function nextSort(current: Sort, key: SortKey): Sort {
  if (current.key !== key) return { key, dir: 'asc' };
  return { key, dir: current.dir === 'asc' ? 'desc' : 'asc' };
}
