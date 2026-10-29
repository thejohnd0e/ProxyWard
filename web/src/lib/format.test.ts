import { describe, expect, it } from 'vitest';
import {
  describeImportError,
  formatBytes,
  formatDuration,
  formatInterval,
  formatMbps,
  formatPercent,
  formatRelative,
  splitInterval,
  stateLabel,
} from './format';

describe('formatMbps', () => {
  it('shows a dash, never zero, when there is no completed sample', () => {
    expect(formatMbps(null)).toBe('—');
  });
  it('uses en-US numbers with sensible precision', () => {
    expect(formatMbps(32)).toBe('32.0 Mbps');
    expect(formatMbps(3.14159)).toBe('3.14 Mbps');
    expect(formatMbps(1234.56)).toBe('1,234.6 Mbps');
    expect(formatMbps(0.5)).toBe('0.50 Mbps');
  });
});

describe('formatDuration', () => {
  it('uses ms below a second and seconds above', () => {
    expect(formatDuration(0)).toBe('0 ms');
    expect(formatDuration(124)).toBe('124 ms');
    expect(formatDuration(999)).toBe('999 ms');
    expect(formatDuration(1000)).toBe('1.0 s');
    expect(formatDuration(12_345)).toBe('12.3 s');
    expect(formatDuration(null)).toBe('—');
  });
});

describe('formatBytes', () => {
  it('uses binary units named MiB and KiB', () => {
    expect(formatBytes(512)).toBe('512 B');
    expect(formatBytes(256 * 1024)).toBe('256 KiB');
    expect(formatBytes(8 * 1024 * 1024)).toBe('8.0 MiB');
    expect(formatBytes(10.5 * 1024 * 1024)).toBe('10.5 MiB');
  });
});

describe('formatPercent', () => {
  it('shows a dash without samples and keeps one decimal otherwise', () => {
    expect(formatPercent(null)).toBe('—');
    expect(formatPercent(100)).toBe('100%');
    expect(formatPercent(99.5)).toBe('99.5%');
    expect(formatPercent(0)).toBe('0%');
    expect(formatPercent(66.666)).toBe('66.7%');
  });
});

describe('formatRelative', () => {
  const now = new Date('2026-10-20T12:00:00Z');
  it('describes the past and the future', () => {
    expect(formatRelative('2026-10-20T11:59:50Z', now)).toBe('just now');
    expect(formatRelative('2026-10-20T11:55:00Z', now)).toBe('5 min ago');
    expect(formatRelative('2026-10-20T09:00:00Z', now)).toBe('3 h ago');
    expect(formatRelative('2026-10-18T12:00:00Z', now)).toBe('2 d ago');
    expect(formatRelative('2026-10-20T12:30:00Z', now)).toBe('in 30 min');
    expect(formatRelative('2026-10-20T14:00:00Z', now)).toBe('in 2 h');
    expect(formatRelative(null, now)).toBe('Never');
  });
});

describe('intervals', () => {
  it('converts minutes to the unit a person would choose', () => {
    expect(splitInterval(5)).toEqual({ value: 5, unit: 'minutes' });
    expect(splitInterval(60)).toEqual({ value: 1, unit: 'hours' });
    expect(splitInterval(360)).toEqual({ value: 6, unit: 'hours' });
    expect(splitInterval(90)).toEqual({ value: 90, unit: 'minutes' });
    expect(splitInterval(10080)).toEqual({ value: 168, unit: 'hours' });
  });
  it('writes intervals in plain English', () => {
    expect(formatInterval(1)).toBe('1 minute');
    expect(formatInterval(5)).toBe('5 minutes');
    expect(formatInterval(60)).toBe('1 hour');
    expect(formatInterval(360)).toBe('6 hours');
    expect(formatInterval(90)).toBe('90 minutes');
  });
});

describe('labels', () => {
  it('names every state in words', () => {
    expect(stateLabel('unknown')).toBe('Unknown');
    expect(stateLabel('up')).toBe('Up');
    expect(stateLabel('down')).toBe('Down');
    expect(stateLabel('indeterminate')).toBe('Indeterminate');
  });
  it('explains import problems and falls back for unknown codes', () => {
    expect(describeImportError('invalid_scheme')).toMatch(/http, https or socks5/);
    expect(describeImportError('ambiguous_format')).toMatch(/scheme:\/\/user:password@host:port/);
    expect(describeImportError('invalid_port')).toMatch(/1 to 65535/);
    expect(describeImportError('invalid_fragment_name')).toMatch(/valid proxy name after "#"/);
    expect(describeImportError('something_new')).toBe('This line is not valid.');
    expect(describeImportError('')).toBe('');
  });
});
