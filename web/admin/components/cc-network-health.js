import { LitElement, html } from 'lit';
import { api } from '../lib/api.js';

// Operator-facing health: gateway /health composite readiness.

class CcNetworkHealth extends LitElement {
  static properties = {
    health:      { state: true },
    error:       { state: true },
  };

  constructor() {
    super();
    this.health = null;
    this.error = '';
  }

  createRenderRoot() { return this; }

  async connectedCallback() {
    super.connectedCallback();
    try {
      this.health = await api('/health');
    } catch (err) {
      this.error = err.message;
    }
  }

  render() {
    return html`
      <div class="card">
        <h2>Gateway health</h2>
        <p class="msg">
          Composite readiness check. <code>degraded</code> means the SaaS
          surface still serves but <code>/v1/*</code> will 500/503 at
          request time until the affected subsystem recovers.
        </p>
        ${this.health
          ? html`
            <p><strong>Status:</strong>
              <span class="pill ${this.health.status === 'ok' ? 'ok' : 'warn'}">${this.health.status}</span></p>
            <ul>
              ${Object.entries(this.health.checks || {}).map(
                ([k, v]) => html`<li><code>${k}</code>:
                  <span class="pill ${v.status === 'ok' ? 'ok' : v.status === 'skipped' ? '' : 'warn'}">${v.status}</span>
                  ${v.latency_ms != null ? html`<span class="msg">(${v.latency_ms} ms)</span>` : ''}
                  ${v.error ? html`<br><span class="msg error">${v.error}</span>` : ''}
                </li>`,
              )}
            </ul>`
          : html`<p class="msg">Loading…</p>`}
      </div>

      ${this.error ? html`<p class="msg error">${this.error}</p>` : ''}
    `;
  }
}

customElements.define('cc-network-health', CcNetworkHealth);
