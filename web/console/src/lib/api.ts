// api.ts — the single fetch wrapper for the gateway BFFs.
//
// Base URL: VITE_GATEWAY_URL (default http://localhost:8080). In `vite dev` we leave
// it empty so calls go same-origin and Vite's /api proxy forwards to the gateway
// (avoids CORS). Every authenticated call carries the bearer access token.
//
// The gateway returns JSON `{ error: "..." }` with a non-2xx status on failure; we
// surface that as an ApiError the UI can branch on (401 -> re-login, 429 -> slow down).

const RAW_BASE = (import.meta.env.VITE_GATEWAY_URL ?? '').trim();
// In dev, prefer the proxy (empty base). In a built app, use the configured origin.
export const API_BASE = import.meta.env.DEV ? '' : RAW_BASE.replace(/\/$/, '');

export class ApiError extends Error {
  status: number;
  body: unknown;
  constructor(status: number, message: string, body?: unknown) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.body = body;
  }
}

let tokenGetter: () => string | null = () => null;
let onUnauthorized: (() => void) | null = null;

/** Wire the token source (called once by the auth provider). */
export function configureAuth(getter: () => string | null, unauthorized?: () => void) {
  tokenGetter = getter;
  onUnauthorized = unauthorized ?? null;
}

type Method = 'GET' | 'POST' | 'PUT' | 'DELETE';

interface RequestOpts {
  method?: Method;
  body?: unknown;
  query?: Record<string, string | number | boolean | undefined | null>;
  /** Skip the Authorization header (auth start/verify/refresh are public). */
  anonymous?: boolean;
  signal?: AbortSignal;
}

function buildUrl(path: string, query?: RequestOpts['query']): string {
  const url = new URL(API_BASE + path, API_BASE || window.location.origin);
  if (query) {
    for (const [k, v] of Object.entries(query)) {
      if (v !== undefined && v !== null && v !== '') url.searchParams.set(k, String(v));
    }
  }
  // Return relative when same-origin to keep the proxy happy.
  return API_BASE ? url.toString() : url.pathname + url.search;
}

export async function request<T = unknown>(path: string, opts: RequestOpts = {}): Promise<T> {
  const { method = 'GET', body, query, anonymous, signal } = opts;
  const headers: Record<string, string> = { Accept: 'application/json' };
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  if (!anonymous) {
    const token = tokenGetter();
    if (token) headers['Authorization'] = `Bearer ${token}`;
  }

  let res: Response;
  try {
    res = await fetch(buildUrl(path, query), {
      method,
      headers,
      body: body !== undefined ? JSON.stringify(body) : undefined,
      signal,
    });
  } catch (e) {
    if ((e as Error)?.name === 'AbortError') throw e;
    throw new ApiError(0, 'Network error — is the gateway reachable?');
  }

  const text = await res.text();
  let json: unknown = undefined;
  if (text) {
    try {
      json = JSON.parse(text);
    } catch {
      json = { raw: text };
    }
  }

  if (!res.ok) {
    const msg =
      (json && typeof json === 'object' && 'error' in json && String((json as any).error)) ||
      res.statusText ||
      `Request failed (${res.status})`;
    if (res.status === 401 && !anonymous && onUnauthorized) onUnauthorized();
    throw new ApiError(res.status, msg, json);
  }
  return json as T;
}

export const api = {
  get: <T = unknown>(path: string, query?: RequestOpts['query'], signal?: AbortSignal) =>
    request<T>(path, { method: 'GET', query, signal }),
  post: <T = unknown>(path: string, body?: unknown, opts?: Omit<RequestOpts, 'method' | 'body'>) =>
    request<T>(path, { method: 'POST', body, ...opts }),
};

/** Convert a File to base64 (no data: prefix) for the submit-brand logo upload. */
export function fileToBase64(file: File): Promise<{ base64: string; contentType: string }> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => {
      const result = String(reader.result || '');
      const comma = result.indexOf(',');
      resolve({ base64: comma >= 0 ? result.slice(comma + 1) : result, contentType: file.type });
    };
    reader.onerror = () => reject(new Error('Could not read file'));
    reader.readAsDataURL(file);
  });
}
