import { LitElement, html } from 'lit';
import { api } from '../lib/api.js';

class CcABRJobs extends LitElement {
  static properties = {
    rows:  { state: true },
    hours: { state: true },
    error: { state: true },
  };

  constructor() {
    super();
    this.rows = [];
    this.hours = 168; // 7 days
    this.error = '';
  }

  createRenderRoot() { return this; }

  async connectedCallback() {
    super.connectedCallback();
    await this.#load();
  }

  async #load() {
    try {
      const data = await api(`/admin/abr-jobs?since_hours=${this.hours}&limit=200`);
      this.rows = data?.items ?? [];
    } catch (err) {
      this.error = err.message;
    }
  }

  #onWindow = async (e) => {
    this.hours = Number(e.target.value);
    await this.#load();
  };

  render() {
    return html`
      <div class="card">
        <h2>ABR jobs</h2>
        <p class="msg">
          Job status shows transcode progress; accounting tracks settlement separately.
          Payment admission rejection stays pending until settlement is confirmed.
          A succeeded job requires both a runner success result and completed accounting.
        </p>
        <label class="msg" style="display:flex; gap:8px; align-items:center; margin-bottom:12px">
          Window:
          <select @change=${this.#onWindow}>
            <option value="24"   ?selected=${this.hours === 24}>last 24 h</option>
            <option value="168"  ?selected=${this.hours === 168}>last 7 days</option>
            <option value="720"  ?selected=${this.hours === 720}>last 30 days</option>
          </select>
        </label>
        ${this.error ? html`<p class="msg error">${this.error}</p>` : ''}
        ${this.rows.length === 0
          ? html`<p class="msg">No jobs in this window.</p>`
          : html`<table>
              <thead><tr>
                <th>Work id</th><th>Runner job</th><th>State</th>
                <th>Broker</th><th>Latency</th><th>Created</th><th>Resolved</th>
              </tr></thead>
              <tbody>
                ${this.rows.map(
                  (j) => html`<tr>
                    <td><code>${j.work_id.slice(0, 8)}…</code></td>
                    <td>${j.runner_job_id
                      ? html`<code>${j.runner_job_id}</code>`
                      : html`<span class="msg">—</span>`}</td>
                    <td>${statePill(j.status || j.state)}
                      ${j.accounting_state ? html`<br><span class="msg">Accounting: ${j.accounting_state}</span>` : ''}
                      ${j.error_code ? html`<br><code>${j.error_code}</code>` : ''}
                      ${j.error_text
                        ? html`<br><span class="msg error">${j.error_text}</span>`
                        : ''}</td>
                    <td>${j.broker_url
                      ? html`<code>${shortHost(j.broker_url)}</code>`
                      : html`<span class="msg">—</span>`}</td>
                    <td>${j.latency_ms ? `${j.latency_ms} ms` : html`<span class="msg">—</span>`}</td>
                    <td>${new Date(j.created_at).toLocaleString()}</td>
                    <td>${j.resolved_at ? new Date(j.resolved_at).toLocaleTimeString() : html`<span class="msg">—</span>`}</td>
                  </tr>`,
                )}
              </tbody>
            </table>`}
      </div>
    `;
  }
}

function statePill(state) {
  const cls = state === 'succeeded' ? 'ok' : ['failed', 'admission_rejected', 'refunded'].includes(state) ? 'warn' : '';
  return html`<span class="pill ${cls}">${state === 'admission_rejected' ? 'Settlement pending' : state}</span>`;
}
function shortHost(url) { try { return new URL(url).host; } catch { return url; } }

customElements.define('cc-abr-jobs', CcABRJobs);
