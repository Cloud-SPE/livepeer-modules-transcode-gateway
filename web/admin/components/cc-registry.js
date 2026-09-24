import { LitElement, html } from 'lit';
import { api } from '../lib/api.js';

// Three-panel registry diagnostic:
//   1. Summary       — cache freshness + capability filter + counts
//   2. Live candidates — straight from LOC (uncached, real-time)
//   3. Cached catalog — what /v1/capabilities returns
// (Route health died with the LOC migration — LOC owns route selection.)

class CcRegistry extends LitElement {
  static properties = {
    summary:     { state: true },
    candidates:  { state: true },
    capabilities:{ state: true },
    capFilter:   { state: true },
    error:       { state: true },
  };

  constructor() {
    super();
    this.summary = null;
    this.candidates = null;
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
      const [s, c] = await Promise.all([
        api('/admin/registry/summary'),
        api('/admin/capabilities'),
      ]);
      this.summary = s;
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
          Straight from LOC — not the cache. These are the orchestrators
          LOC can route <code>/api/v1/abr</code> and <code>/api/v1/live</code> to right now.
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
            ? html`<p class="msg warn">Zero candidates. LOC doesn't see anyone advertising this right now.</p>`
            : html`<table>
                <thead><tr>
                  <th>Worker</th><th>Eth address</th><th>Price (wei / units)</th>
                  <th>Work unit</th><th>Protocol</th>
                </tr></thead>
                <tbody>
                  ${c.items.map(
                    (r) => html`<tr>
                      <td><code>${r.worker_url}</code></td>
                      <td><code>${r.eth_address || '—'}</code></td>
                      <td>${r.price_per_work_unit_wei || '—'} / ${r.units_per_price || '—'}</td>
                      <td>${r.work_unit || html`<span class="msg">—</span>`}</td>
                      <td><code>${r.protocol || '—'}</code></td>
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
          The persisted view served by <code>/api/v1/capabilities</code>. Rebuilt
          on every refresh tick; "Live candidates" above is the source of truth.
        </p>
        ${(c.items?.length ?? 0) === 0
          ? html`<p class="msg">No capabilities cached.</p>`
          : html`<table>
              <thead><tr>
                <th>ID</th><th>Capability</th><th>Offering</th>
                <th>Protocol</th><th>Work unit</th><th>Price (wei / units)</th>
                <th>Contract metadata</th>
              </tr></thead>
              <tbody>
                ${c.items.map(
                  (row) => html`<tr>
                    <td><code>${row.id}</code></td>
                    <td>${row.capability}</td>
                    <td>${row.offering}</td>
                    <td><code>${row.protocol || '—'}</code></td>
                    <td><code>${row.work_unit || '—'}</code></td>
                    <td>${row.price_per_work_unit_wei || '—'} / ${row.units_per_price || '—'}</td>
                    <td><details><summary>View</summary><pre>${JSON.stringify({
                      work_unit_estimator: row.work_unit_estimator,
                      job: row.job,
                      session: row.session,
                      extra: row.extra,
                    }, null, 2)}</pre></details></td>
                  </tr>`,
                )}
              </tbody>
            </table>`}
      </div>
    `;
  }
}

customElements.define('cc-registry', CcRegistry);
