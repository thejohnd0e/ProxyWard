// Display formatting. Everything is English with en-US numbers; dates use the
// viewer's local time zone. Missing values are a dash, never zero.

import type { State } from '../api/types';

const DASH = '—';

const oneDecimal = new Intl.NumberFormat('en-US', { minimumFractionDigits: 1, maximumFractionDigits: 1 });
const twoDecimals = new Intl.NumberFormat('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 });
const integer = new Intl.NumberFormat('en-US', { maximumFractionDigits: 0 });

export function formatMbps(mbps: number | null | undefined): string {
  if (mbps === null || mbps === undefined) return DASH;
  return `${mbps < 10 ? twoDecimals.format(mbps) : oneDecimal.format(mbps)} Mbps`;
}

export function formatDuration(ms: number | null | undefined): string {
  if (ms === null || ms === undefined) return DASH;
  if (ms < 1000) return `${integer.format(ms)} ms`;
  return `${oneDecimal.format(ms / 1000)} s`;
}

export function formatBytes(bytes: number): string {
  const KiB = 1024;
  const MiB = KiB * 1024;
  if (bytes < KiB) return `${integer.format(bytes)} B`;
  if (bytes < MiB) return `${integer.format(bytes / KiB)} KiB`;
  return `${oneDecimal.format(bytes / MiB)} MiB`;
}

export function formatPercent(percent: number | null | undefined): string {
  if (percent === null || percent === undefined) return DASH;
  const rounded = Math.round(percent * 10) / 10;
  return `${Number.isInteger(rounded) ? integer.format(rounded) : oneDecimal.format(rounded)}%`;
}

const dateTime = new Intl.DateTimeFormat('en-US', { dateStyle: 'medium', timeStyle: 'short' });

export function formatDateTime(iso: string | null | undefined): string {
  if (!iso) return DASH;
  return dateTime.format(new Date(iso));
}

export function formatRelative(iso: string | null | undefined, now: Date = new Date()): string {
  if (!iso) return 'Never';
  const diff = new Date(iso).getTime() - now.getTime();
  const abs = Math.abs(diff);
  const minute = 60_000;
  const hour = 60 * minute;
  const day = 24 * hour;
  if (abs < minute) return 'just now';
  let amount: string;
  if (abs < hour) amount = `${Math.round(abs / minute)} min`;
  else if (abs < day) amount = `${Math.round(abs / hour)} h`;
  else amount = `${Math.round(abs / day)} d`;
  return diff < 0 ? `${amount} ago` : `in ${amount}`;
}

export type IntervalUnit = 'minutes' | 'hours';

/** Chooses hours when the interval is a whole number of hours. */
export function splitInterval(minutes: number): { value: number; unit: IntervalUnit } {
  if (minutes >= 60 && minutes % 60 === 0) return { value: minutes / 60, unit: 'hours' };
  return { value: minutes, unit: 'minutes' };
}

export function toMinutes(value: number, unit: IntervalUnit): number {
  return unit === 'hours' ? value * 60 : value;
}

export function formatInterval(minutes: number): string {
  const { value, unit } = splitInterval(minutes);
  const noun = unit === 'hours' ? 'hour' : 'minute';
  return `${integer.format(value)} ${noun}${value === 1 ? '' : 's'}`;
}

const STATE_LABELS: Record<State, string> = {
  unknown: 'Unknown',
  up: 'Up',
  down: 'Down',
  indeterminate: 'Indeterminate',
};

export function stateLabel(state: State): string {
  return STATE_LABELS[state] ?? 'Unknown';
}

const IMPORT_ERRORS: Record<string, string> = {
  empty: 'The line is empty.',
  invalid_scheme: 'Unsupported protocol. Use http, https or socks5.',
  invalid_format: 'Use scheme://host:port or host:port. Credentials belong in scheme://user:password@host:port.',
  ambiguous_format: 'Ambiguous format. Write credentials as scheme://user:password@host:port.',
  invalid_url: 'The address contains characters that are not allowed, such as spaces.',
  invalid_host: 'The host is not a valid name or IP address.',
  invalid_port: 'The port is missing or not a number from 1 to 65535.',
  unexpected_path: 'Remove the path after the port.',
  unexpected_query: 'Remove the query string (everything from "?").',
  unexpected_fragment: 'Remove the fragment (everything from "#").',
  empty_username: 'The username is empty. Remove the credentials or add a username.',
  invalid_credentials_encoding: 'The credentials are not valid percent-encoding. Encode special characters, for example "@" as %40.',
};

export function describeImportError(code: string): string {
  if (code === '') return '';
  return IMPORT_ERRORS[code] ?? 'This line is not valid.';
}
