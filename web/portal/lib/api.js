// Portal API wrapper. Uses cookie sessions (credentials: include).
//
// All gateway API routes live under /api/*. Legacy unprefixed paths
// (/v1/..., /portal/...) are auto-prefixed so existing call sites
// keep working without a sweep through every component. /health stays
// at the root (LB convention).

function apiPath(p) {
  if (p.startsWith('/api/') || p === '/health' || p.startsWith('/metrics')) {
    return p;
  }
  return '/api' + p;
}

export async function api(path, opts = {}) {
  const headers = {
    ...(opts.headers ?? {}),
  };
  if (!(opts.body instanceof FormData) && !opts.rawBody) {
    headers['Content-Type'] = 'application/json';
  }
  const res = await fetch(apiPath(path), {
    method: opts.method ?? 'GET',
    headers,
    credentials: 'include',
    body: opts.body && !(opts.body instanceof FormData) && !opts.rawBody
      ? JSON.stringify(opts.body)
      : opts.body,
  });
  const ct = res.headers.get('content-type') || '';
  let payload = null;
  if (ct.includes('application/json')) {
    try { payload = await res.json(); } catch { payload = null; }
  }
  if (!res.ok) {
    const message =
      payload?.detail ?? payload?.title ?? payload?.error ?? `HTTP ${res.status}`;
    const err = new Error(typeof message === 'string' ? message : 'request failed');
    err.status = res.status;
    err.payload = payload;
    throw err;
  }
  if (payload === null && ct && !ct.includes('application/json')) {
    // Likely the dev-server static fallback served index.html for an
    // un-proxied path. Surface it instead of silently returning null.
    const err = new Error(`unexpected non-JSON response (content-type: ${ct})`);
    err.status = res.status;
    throw err;
  }
  return payload;
}
