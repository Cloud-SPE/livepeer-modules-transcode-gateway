// Tiny fetch wrapper. Same path-based API for site/portal/admin —
// caller decides which paths to hit.

export async function api(path, opts = {}) {
  const res = await fetch(path, {
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
