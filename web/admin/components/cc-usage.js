import { LitElement, html } from 'lit';
import { api } from '../lib/api.js';

// Aggregate usage by API key — joined to the owning user's email.

class CcUsage extends LitElement {
  static properties = {
    rows:  { state: true },
    error: { state: true },
  };

  constructor() {
    super();
    this.rows = [];
    this.error = '';
  }

  createRenderRoot() { return this; }

  async connectedCallback() {
    super.connectedCallback();
    try {
      const data = await api('/admin/usage?limit=200');
      this.rows = data?.items ?? [];
    } catch (err) {
      this.error = err.message;
    }
  }

  render() {
    return html`
      <div class="card">
        <h2>Usage by API key</h2>
        <p class="msg">
          Counts every reservation row by state. Refunds matter — they're
          the gateway's view of "broker accepted then we couldn't commit"
          (e.g. failover exhausted, runner errored before mintEnvelope).
          Refunds rising sharply against committed → routing issue.
        </p>
        ${this.error ? html`<p class="msg error">${this.error}</p>` : ''}
        ${this.rows.length === 0
          ? html`<p class="msg">No requests yet.</p>`
          : html`<table>
              <thead><tr>
                <th>Email</th><th>API key</th>
                <th>Total</th><th>Committed</th><th>Refunded</th><th>Open</th>
                <th>Last used</th>
              </tr></thead>
              <tbody>
                ${this.rows.map(
                  (r) => html`<tr>
                    <td>${r.email}</td>
                    <td><code>${r.key_prefix}…</code></td>
                    <td>${r.total_requests}</td>
                    <td>${r.committed_total}</td>
                    <td>${r.refunded_total > 0
                      ? html`<span class="pill warn">${r.refunded_total}</span>`
                      : r.refunded_total}</td>
                    <td>${r.open_total}</td>
                    <td>${r.last_used_at ? new Date(r.last_used_at).toLocaleString() : ''}</td>
                  </tr>`,
                )}
              </tbody>
            </table>`}
      </div>
    `;
  }
}

customElements.define('cc-usage', CcUsage);
