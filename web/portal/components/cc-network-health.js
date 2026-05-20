import { LitElement, html } from 'lit';
import { api } from '/lib/api.js';

class CcNetworkHealth extends LitElement {
  static properties = {
    health: { state: true },
    capabilities: { state: true },
    error: { state: true },
  };

  constructor() {
    super();
    this.health = null;
    this.capabilities = null;
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
    try {
      const key = window.__lvpApiKey || sessionStorage.getItem('lvp_video_api_key') || '';
      const headers = key ? { Authorization: `Bearer ${key}` } : {};
      this.capabilities = await api('/v1/capabilities', { headers });
    } catch { /* expected if no key */ }
  }

  render() {
    const h = this.health;
    const c = this.capabilities;
    return html`
      <div class="card">
        <h2>Gateway health</h2>
        ${h
          ? html`
            <p><strong>Status:</strong>
              <span class="pill ${h.status === 'ok' ? 'ok' : 'warn'}">${h.status}</span></p>
            <ul>
              ${Object.entries(h.checks || {}).map(
                ([k, v]) => html`<li><code>${k}</code>:
                  <span class="pill ${v.status === 'ok' ? 'ok' : v.status === 'skipped' ? '' : 'warn'}">${v.status}</span>
                  ${v.latency_ms != null ? html`<span class="msg">(${v.latency_ms} ms)</span>` : ''}
                  ${v.error ? html`<span class="msg error">— ${v.error}</span>` : ''}
                </li>`,
              )}
            </ul>`
          : html`<p class="msg">Loading…</p>`}
      </div>
      <div class="card">
        <h2>Capabilities</h2>
        ${c
          ? c.data?.length
            ? html`<ul>
                ${c.data.map(
                  (cap) => html`<li><code>${cap.id}</code>
                    — ${cap.interaction_mode || 'unknown mode'}
                    ${cap.price_per_work_unit_wei ? html` <span class="msg">@ ${cap.price_per_work_unit_wei} wei/unit</span>` : ''}
                  </li>`,
                )}
              </ul>`
            : html`<p class="msg">No capabilities advertised yet.</p>`
          : html`<p class="msg">Sign in to /v1/capabilities with your API key from Playground first.</p>`}
      </div>
    `;
  }
}

customElements.define('cc-network-health', CcNetworkHealth);
