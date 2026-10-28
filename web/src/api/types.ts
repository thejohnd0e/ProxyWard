// Shapes of the JSON API. They mirror the backend DTOs, which never contain
// passwords, tokens or API keys: secrets only appear as has_* flags.

export type Protocol = 'http' | 'https' | 'socks5';
export type State = 'unknown' | 'up' | 'down' | 'indeterminate';
export type CheckKind = 'availability' | 'speed';

export interface Failure {
  code: string;
  message: string;
}

export interface ScheduleOverride {
  enabled: boolean;
  interval_minutes: number;
}

export interface Status {
  state: State;
  checked_at: string | null;
  exit_ip: string;
  endpoint: string;
  duration_ms: number | null;
  failure: Failure | null;
}

export interface Transfer {
  bytes: number;
  duration_ms: number;
  /** null means no completed sample, which is not the same as zero. */
  mbps: number | null;
  low_confidence: boolean;
  failure: Failure | null;
}

export interface SpeedResult {
  checked_at: string;
  provider: string;
  method: string;
  total_bytes: number;
  download: Transfer;
  upload: Transfer;
}

export interface Geo {
  status: string;
  stale: boolean;
  country_code: string;
  country: string;
  city: string;
  asn: number | null;
  organization: string;
  fetched_at: string;
}

export interface Uptime {
  percent: number | null;
  samples: number;
  window_hours: number;
}

export interface ActiveJob {
  id: number;
  kind: CheckKind;
  status: 'pending' | 'running';
}

export interface Proxy {
  id: number;
  monitoring_paused: boolean;
  name: string;
  protocol: Protocol;
  host: string;
  port: number;
  tags: string[];
  auth_enabled: boolean;
  username: string;
  has_password: boolean;
  availability_schedule: ScheduleOverride | null;
  speed_schedule: ScheduleOverride | null;
  status: Status;
  speed: SpeedResult | null;
  geo: Geo | null;
  uptime: Uptime;
  active_jobs: ActiveJob[];
  next_runs: { availability: string | null; speed: string | null };
}

export interface ProxyList {
  proxies: Proxy[];
  limit: number;
  geo_configured: boolean;
}

/** Body of POST /api/proxies and PUT /api/proxies/{id}. */
export interface ProxyInput {
  name?: string;
  protocol: Protocol;
  host: string;
  port: number;
  tags?: string[];
  auth_enabled?: boolean;
  username?: string;
  /** Omit to keep the stored password; an empty string sets an empty password. */
  password?: string;
  availability_schedule?: ScheduleOverride | null;
  speed_schedule?: ScheduleOverride | null;
}

export interface ImportRow {
  line: number;
  masked: string;
  valid: boolean;
  duplicate: boolean;
  error_code: string;
}

export interface ImportPreview {
  rows: ImportRow[];
  existing_count: number;
  limit: number;
  importable_count: number;
}

export interface Job {
  id: number;
  proxy_id: number;
  kind: CheckKind;
  status: 'pending' | 'running' | 'done' | 'canceled' | 'interrupted';
  source: 'manual' | 'scheduled';
  due_at: string;
  created_at: string;
  started_at: string | null;
  finished_at: string | null;
}

export interface EnqueueResult {
  new: Job[];
  existing: Job[];
  missing_proxy_ids: number[];
}

export interface AvailabilityItem {
  id: number;
  checked_at: string;
  state: State;
  exit_ip: string;
  endpoint: string;
  duration_ms: number;
  failure: Failure | null;
}

export interface SpeedItem {
  id: number;
  checked_at: string;
  provider: string;
  method: string;
  total_bytes: number;
  download: Transfer;
  upload: Transfer;
}

export interface HistoryPage<T> {
  kind: CheckKind;
  items: T[];
  next_cursor: string | null;
}

export interface AvailabilityPoint {
  t: string;
  samples: number;
  up: number;
  down: number;
  indeterminate: number;
  avg_duration_ms: number | null;
}

export interface SpeedPoint {
  t: string;
  samples: number;
  download_mbps: number | null;
  upload_mbps: number | null;
  download_samples: number;
  upload_samples: number;
}

export interface Chart<P> {
  kind: CheckKind;
  from: string;
  to: string;
  interval_seconds: number;
  points: P[];
}

export type DeliveryStatus = 'none' | 'pending' | 'delivered' | 'failed';

export interface AppEvent {
  id: number;
  proxy_id: number | null;
  proxy_name: string | null;
  kind: string;
  message: string;
  created_at: string;
  delivery: { status: DeliveryStatus; attempts: number; last_error: string; delivered_at: string | null };
}

export interface EventPage {
  events: AppEvent[];
  next_cursor: string | null;
}

export interface SettingsView {
  monitoring: {
    availability: { enabled: boolean; interval_minutes: number };
    speed: { enabled: boolean; interval_minutes: number };
    availability_concurrency: number;
    availability_timeout_seconds: number;
    speed_sample_mib: number;
    speed_direction_timeout_seconds: number;
    down_threshold: number;
    retention_days: number;
  };
  geo: {
    /** "auto", "off" or the ID of one source. */
    source: string;
    sources: GeoSourceInfo[];
  };
  telegram: {
    has_token: boolean;
    chat_id: string;
    configured: boolean;
    delivery: { pending: number; failed: number; last_delivered_at: string | null; last_error: string };
  };
  speed_provider: { name: string; paused: boolean; reason: string; until: string | null };
}

/** Body of PUT /api/settings. Every part is optional. */
export interface SettingsUpdate {
  monitoring?: {
    availability?: { enabled?: boolean; interval_minutes?: number };
    speed?: { enabled?: boolean; interval_minutes?: number };
    availability_concurrency?: number;
    availability_timeout_seconds?: number;
    speed_sample_mib?: number;
    speed_direction_timeout_seconds?: number;
    down_threshold?: number;
    retention_days?: number;
  };
  geo?: { source?: string };
  telegram?: { bot_token?: string; chat_id?: string };
}

export interface GeoSourceInfo {
  id: string;
  name: string;
  site: string;
}

export interface SessionInfo {
  authenticated: boolean;
  csrf_token?: string;
  /** True while no administrator password exists yet (first run). */
  needs_setup?: boolean;
}
