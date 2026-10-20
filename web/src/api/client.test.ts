import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError, api, onUnauthenticated, setCsrfToken } from './client';

function respond(status: number, body?: unknown) {
  return new Response(body === undefined ? null : JSON.stringify(body), {
    status,
    headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
  });
}

describe('api client', () => {
  const fetchMock = vi.fn();

  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal('fetch', fetchMock);
    setCsrfToken(null);
    onUnauthenticated(null);
  });
  afterEach(() => vi.unstubAllGlobals());

  it('sends same-origin credentials and no CSRF header on reads', async () => {
    setCsrfToken('tok');
    fetchMock.mockResolvedValue(respond(200, { ok: 1 }));
    const out = await api<{ ok: number }>('/api/proxies');
    expect(out).toEqual({ ok: 1 });
    const [url, init] = fetchMock.mock.calls[0]!;
    expect(url).toBe('/api/proxies');
    expect(init.credentials).toBe('same-origin');
    expect(new Headers(init.headers).has('X-CSRF-Token')).toBe(false);
  });

  it('attaches the CSRF token and a JSON content type to mutations', async () => {
    setCsrfToken('tok');
    fetchMock.mockResolvedValue(respond(201, { id: 3 }));
    await api('/api/proxies', { method: 'POST', body: { host: 'h' } });
    const init = fetchMock.mock.calls[0]![1];
    const headers = new Headers(init.headers);
    expect(headers.get('X-CSRF-Token')).toBe('tok');
    expect(headers.get('Content-Type')).toBe('application/json');
    expect(init.body).toBe(JSON.stringify({ host: 'h' }));
  });

  it('returns undefined for empty responses', async () => {
    fetchMock.mockResolvedValue(respond(204));
    await expect(api('/api/proxies/1', { method: 'DELETE' })).resolves.toBeUndefined();
  });

  it('turns error bodies into a typed ApiError', async () => {
    fetchMock.mockResolvedValue(
      respond(422, { error: { code: 'validation_failed', message: 'Some fields are not valid.', fields: { host: 'Host is required.' } } }),
    );
    const err = await api('/api/proxies', { method: 'POST', body: {} }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    const apiErr = err as ApiError;
    expect(apiErr.status).toBe(422);
    expect(apiErr.code).toBe('validation_failed');
    expect(apiErr.fields).toEqual({ host: 'Host is required.' });
    expect(apiErr.message).toBe('Some fields are not valid.');
  });

  it('copes with error responses that are not JSON', async () => {
    fetchMock.mockResolvedValue(new Response('<html>bad gateway</html>', { status: 502 }));
    const err = (await api('/api/proxies').catch((e: unknown) => e)) as ApiError;
    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(502);
    expect(err.message).toMatch(/server/i);
    expect(err.message).not.toContain('<html>');
  });

  it('reports network failures in plain English', async () => {
    fetchMock.mockRejectedValue(new TypeError('Failed to fetch'));
    const err = (await api('/api/proxies').catch((e: unknown) => e)) as ApiError;
    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(0);
    expect(err.code).toBe('network');
    expect(err.message).toMatch(/reach the server/i);
  });

  it('notifies once about 401 responses so the app can show the login page', async () => {
    const handler = vi.fn();
    onUnauthenticated(handler);
    fetchMock.mockResolvedValue(respond(401, { error: { code: 'unauthenticated', message: 'Authentication is required.', fields: {} } }));
    await api('/api/proxies').catch(() => undefined);
    expect(handler).toHaveBeenCalledTimes(1);
  });

  it('does not treat a wrong login password as an expired session', async () => {
    const handler = vi.fn();
    onUnauthenticated(handler);
    fetchMock.mockResolvedValue(respond(401, { error: { code: 'invalid_credentials', message: 'Invalid password.', fields: {} } }));
    await api('/api/auth/login', { method: 'POST', body: { password: 'x' } }).catch(() => undefined);
    expect(handler).not.toHaveBeenCalled();
  });

  it('builds query strings and skips empty values', async () => {
    fetchMock.mockResolvedValue(respond(200, {}));
    await api('/api/events', { query: { limit: 20, kind: ['proxy_down', 'proxy_recovered'], cursor: undefined, proxy_id: '' } });
    expect(fetchMock.mock.calls[0]![0]).toBe('/api/events?limit=20&kind=proxy_down&kind=proxy_recovered');
  });

  it('passes an abort signal through', async () => {
    fetchMock.mockResolvedValue(respond(200, {}));
    const ctl = new AbortController();
    await api('/api/proxies', { signal: ctl.signal });
    expect(fetchMock.mock.calls[0]![1].signal).toBe(ctl.signal);
  });
});
