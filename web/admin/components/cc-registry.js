import { LitElement, html } from 'lit';
import { api } from '/lib/api.js';

// Four-panel registry diagnostic:
//   1. Summary       — cache freshness + capability filter + counts
//   2. Live candidates — straight from the resolver (uncached, real-time)
//   3. Route health  — in-memory failure tracker (process-local)
//   4. Cached catalog — what /v1/capabilities returns

class CcRegistry extends LitElement {
  static properties = {
    summary:     { state: true },
    candidates:  { state: true },
    health:      { state: true },
    capabilities:{ state: true },
    capFilter:   { state: true },
    error:       { state: true },
  };

  constructor() {
    super();
    this.summary = null;
    this.candidates = null;
    this.health = null;
    this.capabilities = null;
    this.capFilter = '';
    this.error = '';
  }

  createRenderRoot() { return this; }

  async connectedCallback() {
    super.connectedCallback();
    await this.#load();
  }

  async #load() {
    try {
      const [s, h, c] = await Promise.all([
        api('/admin/registry/summary'),
        api('/admin/registry/health'),
        api('/admin/capabilities'),
      ]);
      this.summary = s;
      this.health = h;
      this.capabilities = c;
      // Live candidates default to the first capability we know about.
      const firstCap = s?.by_capability?.[0]?.capability || this.capabilities?.items?.[0]?.capability || '';
      this.capFilter = firstCap;
      if (firstCap) {
        this.candidates = await api(`/admin/registry/candidates?capability=${encodeURIComponent(firstCap)}`);
      }
    } catch (err) {
      this.error = err.message;
    }
  }

  #onCapChange = async (e) => {
    this.capFilter = e.target.value;
    try {
      this.candidates = await api(`/admin/registry/candidates?capability=${encodeURIComponent(this.capFilter)}`);
    } catch (err) {
      this.error = err.message;
    }
  };

  render() {
    return html`
      ${this.#renderSummary()}
      ${this.#renderCandidates()}
      ${this.#renderRouteHealth()}
      ${this.#renderCachedCatalog()}
      ${this.error ? html`<p class="msg error">${this.error}</p>` : ''}
    `;
  }

  #renderSummary() {
    const s = this.summary;
    if (!s) return html`<div class="card"><p class="msg">Loading summary…</p></div>`;
    const refreshed = s.last_refresh_at && !s.last_refresh_at.startsWith('1970')
      ? new Date(s.last_refresh_at).toLocaleString()
      : '(never)';
    return html`
      <div class="card">
        <h2>Registry summary</h2>
        <p class="msg">
          Compares the background-refresh result (left) against what the
          gateway is currently serving (right). They should always agree.
        </p>
        <table>
          <tr><th>Last refresh</th><td>${refreshed}</td></tr>
          <tr><th>Outcome</th><td>
            <span class="pill ${s.last_outcome === 'ok' ? 'ok' : 'warn'}">${s.last_outcome || '—'}</span>
            ${s.last_error ? html`<span class="msg error"> — ${s.last_error}</span>` : ''}
          </td></tr>
          <tr><th>Filter</th><td>
            ${(s.capability_filter || []).length === 0
              ? html`<span class="msg">(all)</span>`
              : html`<ul style="margin:0;padding-left:18px">${
                  s.capability_filter.map((c) => html`<li><code>${c}</code></li>`)
                }</ul>`}
          </td></tr>
          <tr><th>Rows matched (last tick)</th><td>${s.rows_matched}</td></tr>
          <tr><th>Active in DB now</th><td>${s.active_count}</td></tr>
          <tr><th>By capability</th><td>
            ${(s.by_capability || []).length === 0
              ? html`<span class="msg">—</span>`
              : html`<ul style="margin:0;padding-left:18px">${
                  s.by_capability.map((b) => html`<li><code>${b.capability}</code> · ${b.count}</li>`)
                }</ul>`}
          </td></tr>
        </table>
      </div>
    `;
  }

  #renderCandidates() {
    const c = this.candidates;
    const caps = this.summary?.by_capability?.map((b) => b.capability) || [];
    return html`
      <div class="card">
        <h2>Live candidates
          <button class="ghost" @click=${this.#load} style="float:right">Refresh</button>
        </h2>
        <p class="msg">
          Straight from the resolver — not the cache. This is what
          <code>/v1/abr</code> would dispatch against right now.
        </p>
        ${caps.length === 0
          ? html`<p class="msg">No capabilities cached; nothing to query.</p>`
          : html`<label class="msg" style="display:flex; gap:8px; align-items:center; margin-bottom:12px">
              Capability:
              <select @change=${this.#onCapChange}>
                ${caps.map((cap) => html`<option value=${cap} ?selected=${cap === this.capFilter}>${cap}</option>`)}
              </select>
            </label>`}
        ${!c
          ? html`<p class="msg">—</p>`
          : (c.items?.length ?? 0) === 0
            ? html`<p class="msg warn">Zero candidates. The resolver doesn't see anyone advertising this right now.</p>`
            : html`<table>
                <thead><tr>
                  <th>Worker</th><th>Eth address</th><th>Price (wei/unit)</th>
                  <th>Work unit</th><th>Quote</th>
                </tr></thead>
                <tbody>
                  ${c.items.map(
                    (r) => html`<tr>
                      <td><code>${r.worker_url}</code></td>
                      <td><code>${r.eth_address || '—'}</code></td>
                      <td>${r.price_per_work_unit_wei || '—'}</td>
                      <td>${r.work_unit || html`<span class="msg">—</span>`}</td>
                      <td>${r.quote_id
                        ? html`<code>${r.quote_id}</code> <span class="msg">v${r.quote_version}</span>`
                        : html`<span class="msg">—</span>`}</td>
                    </tr>`,
                  )}
                </tbody>
              </table>`}
      </div>
    `;
  }

  #renderRouteHealth() {
    const h = this.health;
    if (!h) return '';
    return html`
      <div class="card">
        <h2>Route health (in-memory)</h2>
        <p class="msg">
          Per-candidate failure state. After <code>${h.failure_threshold}</code>
          consecutive failures a candidate cools off for
          <code>${h.cooldown_seconds}s</code>.
        </p>
        ${(h.items?.length ?? 0) === 0
          ? html`<p class="msg ok">All routes healthy.</p>`
          : html`<table>
              <thead><tr><th>Candidate</th><th>Failures</th><th>Cooldown until</th></tr></thead>
              <tbody>
                ${h.items.map(
                  (e) => html`<tr>
                    <td><code>${e.key}</code></td>
                    <td>${e.consec_failures}</td>
                    <td>${e.cooling_down
                      ? html`<span class="pill warn">${new Date(e.cooldown_until).toLocaleTimeString()}</span>`
                      : html`<span class="msg">—</span>`}</td>
                  </tr>`,
                )}
              </tbody>
            </table>`}
      </div>
    `;
  }

  #renderCachedCatalog() {
    const c = this.capabilities;
    if (!c) return '';
    return html`
      <div class="card">
        <h2>Cached catalog (${c.items?.length ?? 0})</h2>
        <p class="msg">
          The persisted view served by <code>/v1/capabilities</code>. Rebuilt
          on every refresh tick; "Live candidates" above is the source of truth.
        </p>
        ${(c.items?.length ?? 0) === 0
          ? html`<p class="msg">No capabilities cached.</p>`
          : html`<table>
              <thead><tr>
                <th>ID</th><th>Capability</th><th>Offering</th>
                <th>Mode</th><th>Broker</th><th>Price (wei/unit)</th>
              </tr></thead>
              <tbody>
                ${c.items.map(
                  (row) => html`<tr>
                    <td><code>${row.id}</code></td>
                    <td>${row.capability}</td>
                    <td>${row.offering}</td>
                    <td><code>${row.interaction_mode || '—'}</code></td>
                    <td><code>${row.broker_url || '—'}</code></td>
                    <td>${row.price_per_work_unit_wei || '—'}</td>
                  </tr>`,
                )}
              </tbody>
            </table>`}
      </div>
    `;
  }
}

customElements.define('cc-registry', CcRegistry);
