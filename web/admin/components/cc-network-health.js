import { LitElement, html } from 'lit';
import { api } from '../lib/api.js';

// Operator-facing health: gateway /health + route-health snapshot.

class CcNetworkHealth extends LitElement {
  static properties = {
    health:      { state: true },
    routeHealth: { state: true },
    error:       { state: true },
  };

  constructor() {
    super();
    this.health = null;
    this.routeHealth = null;
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
      this.routeHealth = await api('/admin/registry/health');
    } catch { /* skip */ }
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

      <div class="card">
        <h2>Route health (in-memory)</h2>
        <p class="msg">
          Per-candidate failure counters and active cooldowns. Process-local
          state — resets on gateway restart. After
          <code>${this.routeHealth?.failure_threshold ?? 2}</code> consecutive
          failures a candidate enters a
          <code>${this.routeHealth?.cooldown_seconds ?? 30}</code>-second
          cooldown and is skipped on the failover loop.
        </p>
        ${!this.routeHealth || (this.routeHealth.items?.length ?? 0) === 0
          ? html`<p class="msg ok">No unhealthy candidates right now.</p>`
          : html`<table>
              <thead><tr><th>Candidate</th><th>Consec failures</th><th>Cooling down until</th></tr></thead>
              <tbody>
                ${this.routeHealth.items.map(
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

      ${this.error ? html`<p class="msg error">${this.error}</p>` : ''}
    `;
  }
}

customElements.define('cc-network-health', CcNetworkHealth);
