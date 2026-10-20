// Thin fetch wrapper: same-origin cookies, CSRF header on mutations, typed
// errors with plain-English messages.

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly fields: Record<string, string>;

  constructor(status: number, code: string, message: string, fields: Record<string, string> = {}) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
    this.fields = fields;
  }
}

type QueryValue = string | number | boolean | null | undefined | Array<string | number>;

export interface ApiInit {
  method?: 'GET' | 'POST' | 'PUT' | 'DELETE';
  body?: unknown;
  query?: Record<string, QueryValue>;
  signal?: AbortSignal;
}

let csrfToken: string | null = null;
let unauthenticated: (() => void) | null = null;

export function setCsrfToken(token: string | null): void {
  csrfToken = token;
}

export function getCsrfToken(): string | null {
  return csrfToken;
}

/** Registers the function called when the server says the session is gone. */
export function onUnauthenticated(handler: (() => void) | null): void {
  unauthenticated = handler;
}

function buildQuery(query: ApiInit['query']): string {
  if (!query) return '';
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) {
    const values = Array.isArray(value) ? value : [value];
    for (const v of values) {
      if (v === undefined || v === null || v === '') continue;
      params.append(key, String(v));
    }
  }
  const text = params.toString();
  return text ? `?${text}` : '';
}

function genericMessage(status: number): string {
  if (status >= 500) return `The server returned an error (HTTP ${status}). Try again in a moment.`;
  if (status === 404) return 'The requested item was not found.';
  if (status === 403) return 'The server refused the request.';
  return `The request failed (HTTP ${status}).`;
}

async function parseError(res: Response): Promise<ApiError> {
  try {
    const body = (await res.json()) as { error?: { code?: string; message?: string; fields?: Record<string, string> } };
    const e = body.error;
    if (e && typeof e.code === 'string' && typeof e.message === 'string') {
      return new ApiError(res.status, e.code, e.message, e.fields ?? {});
    }
  } catch {
    // Not JSON (for example a proxy error page): fall through to a generic message.
  }
  return new ApiError(res.status, 'http_error', genericMessage(res.status));
}

export async function api<T = void>(path: string, init: ApiInit = {}): Promise<T> {
  const method = init.method ?? (init.body !== undefined ? 'POST' : 'GET');
  const headers = new Headers({ Accept: 'application/json' });
  let body: string | undefined;
  if (init.body !== undefined) {
    headers.set('Content-Type', 'application/json');
    body = JSON.stringify(init.body);
  }
  if (method !== 'GET' && csrfToken) headers.set('X-CSRF-Token', csrfToken);

  let res: Response;
  try {
    res = await fetch(path + buildQuery(init.query), { method, headers, body, credentials: 'same-origin', signal: init.signal });
  } catch (e) {
    if (e instanceof DOMException && e.name === 'AbortError') throw e;
    throw new ApiError(0, 'network', 'Cannot reach the server. Check your connection and try again.');
  }

  if (!res.ok) {
    const err = await parseError(res);
    if (res.status === 401 && err.code === 'unauthenticated') unauthenticated?.();
    throw err;
  }
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  return (text ? JSON.parse(text) : undefined) as T;
}
