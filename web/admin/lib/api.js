// Admin API wrapper. Adds X-Admin-Token from localStorage.
//
// All gateway API routes live under /api/*. Legacy unprefixed paths
// (/admin/...) are auto-prefixed so existing call sites keep working
// without a sweep through every component. /health stays at the root
// (LB convention).

const TOKEN_KEY = 'lvp_video_admin_token';

export function getToken() {
  return localStorage.getItem(TOKEN_KEY) || '';
}
export function setToken(tok) {
  if (tok) localStorage.setItem(TOKEN_KEY, tok);
  else localStorage.removeItem(TOKEN_KEY);
}

function apiPath(p) {
  if (p.startsWith('/api/') || p === '/health' || p.startsWith('/metrics')) {
    return p;
  }
  return '/api' + p;
}

export async function api(path, opts = {}) {
  const headers = {
    'Content-Type': 'application/json',
    'X-Admin-Token': getToken(),
    ...(opts.headers ?? {}),
  };
  const res = await fetch(apiPath(path), {
    method: opts.method ?? 'GET',
    headers,
    body: opts.body ? JSON.stringify(opts.body) : undefined,
  });
  let payload = null;
  try { payload = await res.json(); } catch { payload = null; }
  if (!res.ok) {
    const message =
      payload?.detail ?? payload?.title ?? payload?.error ?? `HTTP ${res.status}`;
    const err = new Error(typeof message === 'string' ? message : 'request failed');
    err.status = res.status;
    err.payload = payload;
    throw err;
  }
  return payload;
}
