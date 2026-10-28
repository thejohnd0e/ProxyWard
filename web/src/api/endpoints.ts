// One function per API route, so screens never build URLs or bodies by hand.

import { api } from './client';
import type {
  AvailabilityItem,
  AvailabilityPoint,
  Chart,
  CheckKind,
  EnqueueResult,
  EventPage,
  HistoryPage,
  ImportPreview,
  Job,
  Protocol,
  Proxy,
  ProxyInput,
  ProxyList,
  SessionInfo,
  SettingsUpdate,
  SettingsView,
  SpeedItem,
  SpeedPoint,
} from './types';

export const session = (signal?: AbortSignal) => api<SessionInfo>('/api/auth/session', { signal });
export const login = (password: string) => api<{ csrf_token: string }>('/api/auth/login', { method: 'POST', body: { password } });
export const setupAdmin = (password: string) =>
  api<{ csrf_token: string }>('/api/auth/setup', { method: 'POST', body: { password } });
export const logout = () => api('/api/auth/logout', { method: 'POST' });
export const changePassword = (current_password: string, new_password: string) =>
  api('/api/auth/password', { method: 'PUT', body: { current_password, new_password } });

export const listProxies = (signal?: AbortSignal) => api<ProxyList>('/api/proxies', { signal });
export const getProxy = (id: number, signal?: AbortSignal) => api<Proxy>(`/api/proxies/${id}`, { signal });
export const createProxy = (input: ProxyInput) => api<Proxy>('/api/proxies', { method: 'POST', body: input });
export const updateProxy = (id: number, input: ProxyInput) => api<Proxy>(`/api/proxies/${id}`, { method: 'PUT', body: input });
export const deleteProxy = (id: number) => api(`/api/proxies/${id}`, { method: 'DELETE' });
export const setMonitoringPaused = (id: number, paused: boolean) =>
  api<Proxy>(`/api/proxies/${id}/monitoring`, { method: 'PUT', body: { paused } });

export const previewImport = (text: string, default_protocol: Protocol) =>
  api<ImportPreview>('/api/proxies/import/preview', { method: 'POST', body: { text, default_protocol } });
export const importProxies = (text: string, default_protocol: Protocol, selected_lines: number[]) =>
  api<{ imported: number; ids: number[] }>('/api/proxies/import', {
    method: 'POST',
    body: { text, default_protocol, selected_lines },
  });

export const enqueueChecks = (kind: CheckKind, target: { proxyIds: number[] } | { all: true }) =>
  api<EnqueueResult>('/api/jobs', {
    method: 'POST',
    body: 'all' in target ? { kind, all: true } : { kind, proxy_ids: target.proxyIds },
  });
export const listJobs = (signal?: AbortSignal) => api<{ jobs: Job[] }>('/api/jobs', { signal });
export const cancelJob = (id: number) => api<Job>(`/api/jobs/${id}/cancel`, { method: 'POST' });

export const availabilityHistory = (id: number, cursor: string | null, signal?: AbortSignal) =>
  api<HistoryPage<AvailabilityItem>>(`/api/proxies/${id}/history`, {
    query: { kind: 'availability', limit: 25, cursor },
    signal,
  });
export const speedHistory = (id: number, cursor: string | null, signal?: AbortSignal) =>
  api<HistoryPage<SpeedItem>>(`/api/proxies/${id}/history`, { query: { kind: 'speed', limit: 25, cursor }, signal });
export const availabilityChart = (id: number, from: Date, to: Date, points: number, signal?: AbortSignal) =>
  api<Chart<AvailabilityPoint>>(`/api/proxies/${id}/chart`, {
    query: { kind: 'availability', from: from.toISOString(), to: to.toISOString(), points },
    signal,
  });
export const speedChart = (id: number, from: Date, to: Date, points: number, signal?: AbortSignal) =>
  api<Chart<SpeedPoint>>(`/api/proxies/${id}/chart`, {
    query: { kind: 'speed', from: from.toISOString(), to: to.toISOString(), points },
    signal,
  });

export const listEvents = (
  filters: { proxyId?: number | null; kinds?: string[]; cursor?: string | null },
  signal?: AbortSignal,
) =>
  api<EventPage>('/api/events', {
    query: { limit: 50, proxy_id: filters.proxyId ?? undefined, kind: filters.kinds, cursor: filters.cursor },
    signal,
  });

export const getSettings = (signal?: AbortSignal) => api<SettingsView>('/api/settings', { signal });
export const putSettings = (update: SettingsUpdate) => api<SettingsView>('/api/settings', { method: 'PUT', body: update });
export const testTelegram = () => api<{ message_id: number; status: string }>('/api/settings/telegram/test', { method: 'POST' });
export const resumeSpeed = () => api('/api/settings/speed/resume', { method: 'POST' });
