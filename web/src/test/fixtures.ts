import type { Proxy, SettingsView } from '../api/types';

export function makeProxy(overrides: Partial<Proxy> = {}): Proxy {
  return {
    id: 1,
    name: 'Office gateway',
    protocol: 'http',
    host: '10.0.0.5',
    port: 3128,
    tags: [],
    auth_enabled: false,
    username: '',
    has_password: false,
    availability_schedule: null,
    speed_schedule: null,
    status: { state: 'unknown', checked_at: null, exit_ip: '', endpoint: '', duration_ms: null, failure: null },
    speed: null,
    geo: null,
    uptime: { percent: null, samples: 0, window_hours: 24 },
    active_jobs: [],
    next_runs: { availability: null, speed: null },
    ...overrides,
  };
}

export function makeUpProxy(overrides: Partial<Proxy> = {}): Proxy {
  return makeProxy({
    status: {
      state: 'up',
      checked_at: new Date(Date.now() - 5 * 60_000).toISOString(),
      exit_ip: '198.51.100.7',
      endpoint: 'ipify',
      duration_ms: 124,
      failure: null,
    },
    speed: {
      checked_at: new Date(Date.now() - 60 * 60_000).toISOString(),
      provider: 'cloudflare',
      method: 'app-stream-v1',
      total_bytes: 17_000_000,
      download: { bytes: 8_388_608, duration_ms: 2000, mbps: 33.5, low_confidence: false, failure: null },
      upload: {
        bytes: 100,
        duration_ms: 10,
        mbps: null,
        low_confidence: true,
        failure: { code: 'invalid_response', message: 'The response was not valid.' },
      },
    },
    geo: {
      status: 'fresh',
      stale: false,
      country_code: 'DE',
      country: 'Germany',
      city: 'Berlin',
      asn: 64500,
      organization: 'Example GmbH',
      fetched_at: new Date().toISOString(),
    },
    uptime: { percent: 75, samples: 4, window_hours: 24 },
    ...overrides,
  });
}

export function makeSettings(overrides: Partial<SettingsView> = {}): SettingsView {
  return {
    monitoring: {
      availability: { enabled: false, interval_minutes: 5 },
      speed: { enabled: false, interval_minutes: 360 },
      availability_concurrency: 5,
      availability_timeout_seconds: 10,
      speed_sample_mib: 8,
      speed_direction_timeout_seconds: 30,
      down_threshold: 3,
      retention_days: 30,
    },
    geo: { has_key: false, used_today: 0, daily_limit: 1000, paused: false, pause_reason: '', retry_at: null },
    telegram: {
      has_token: false,
      chat_id: '',
      configured: false,
      delivery: { pending: 0, failed: 0, last_delivered_at: null, last_error: '' },
    },
    speed_provider: { name: 'cloudflare', paused: false, reason: '', until: null },
    ...overrides,
  };
}
