import { LitElement, html } from 'lit';
import { api } from '../lib/api.js';

class CcUsage extends LitElement {
  static properties = {
    items: { state: true },
    error: { state: true },
  };

  constructor() {
    super();
    this.items = [];
    this.error = '';
  }

  createRenderRoot() { return this; }

  async connectedCallback() {
    super.connectedCallback();
    try {
      const data = await api('/portal/usage');
      this.items = data?.items ?? [];
    } catch (err) {
      this.error = err.message;
    }
  }

  render() {
    return html`
      <div class="card">
        <h2>Usage</h2>
        <p class="msg">Last 30 days of request reservations across your API keys.</p>
        ${this.items.length === 0
          ? html`<p class="msg">No activity yet.</p>`
          : html`<table>
              <thead>
                <tr><th>When</th><th>Capability</th><th>Offering</th><th>State</th><th>Status</th><th>Latency</th></tr>
              </thead>
              <tbody>
                ${this.items.map(
                  (r) => html`<tr>
                    <td>${new Date(r.created_at).toLocaleString()}</td>
                    <td><code>${r.capability}</code></td>
                    <td>${r.offering}</td>
                    <td><span class="pill ${r.state === 'committed' ? 'ok' : 'warn'}">${r.state}</span></td>
                    <td>${r.status_code || '—'}</td>
                    <td>${r.latency_ms ? `${r.latency_ms} ms` : '—'}</td>
                  </tr>`,
                )}
              </tbody>
            </table>`}
        ${this.error ? html`<p class="msg error">${this.error}</p>` : ''}
      </div>
    `;
  }
}

customElements.define('cc-usage', CcUsage);
