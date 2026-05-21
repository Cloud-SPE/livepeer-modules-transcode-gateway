// Tiny fetch wrapper. Same path-based API for site/portal/admin —
// caller decides which paths to hit.
//
// All gateway API routes live under /api/*. Legacy unprefixed paths
// (/v1/..., /admin/..., /portal/...) are auto-prefixed so existing
// call sites keep working without a sweep through every component.
// /health + /metrics stay at the root (LB / Prometheus convention).

function apiPath(p) {
  if (p.startsWith('/api/') || p === '/health' || p.startsWith('/metrics')) {
    return p;
  }
  return '/api' + p;
}

export async function api(path, opts = {}) {
  const res = await fetch(apiPath(path), {
    method: opts.method ?? 'GET',
    headers: {
      'Content-Type': 'application/json',
      ...(opts.headers ?? {}),
    },
    credentials: 'include',
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
