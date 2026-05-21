import { LitElement, html } from 'lit';
import { api } from '../lib/api.js';

// Server-side history of ABR ladder transcode jobs the user has
// submitted. Complements the Playground's localStorage list — this
// view survives a browser cache wipe and shows the gateway's commit
// state for each job.

class CcABRJobs extends LitElement {
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
      const data = await api('/portal/abr-jobs?limit=200');
      this.rows = data?.items ?? [];
    } catch (err) {
      this.error = err.message;
    }
  }

  render() {
    return html`
      <div class="card">
        <h2>ABR jobs</h2>
        <p class="msg">
          Each row is a <code>POST /v1/abr</code> the gateway dispatched on
          your behalf. A <code>committed</code> state means the broker
          accepted the job and the gateway minted a payment — but the
          runner-side encode may still be in flight. The Playground tab
          gives you live status + playback for in-progress jobs.
        </p>
        ${this.error ? html`<p class="msg error">${this.error}</p>` : ''}
        ${this.rows.length === 0
          ? html`<p class="msg">No ABR jobs yet — try the Playground's Transcode tab.</p>`
          : html`<table>
              <thead><tr>
                <th>Work id</th><th>Runner job</th><th>State</th>
                <th>Broker</th><th>Latency</th><th>Created</th>
              </tr></thead>
              <tbody>
                ${this.rows.map(
                  (j) => html`<tr>
                    <td><code>${j.work_id.slice(0,8)}…</code></td>
                    <td>${j.runner_job_id
                      ? html`<code>${j.runner_job_id}</code>`
                      : html`<span class="msg">—</span>`}</td>
                    <td>${pill(j.state)}
                      ${j.error_text
                        ? html`<br><span class="msg error">${j.error_text}</span>`
                        : ''}</td>
                    <td>${j.broker_url
                      ? html`<code>${shortHost(j.broker_url)}</code>`
                      : html`<span class="msg">—</span>`}</td>
                    <td>${j.latency_ms ? `${j.latency_ms} ms` : html`<span class="msg">—</span>`}</td>
                    <td>${new Date(j.created_at).toLocaleString()}</td>
                  </tr>`,
                )}
              </tbody>
            </table>`}
      </div>
    `;
  }
}

function pill(state) {
  const cls = state === 'committed' ? 'ok' : state === 'refunded' ? 'warn' : '';
  return html`<span class="pill ${cls}">${state}</span>`;
}
function shortHost(url) { try { return new URL(url).host; } catch { return url; } }

customElements.define('cc-abr-jobs', CcABRJobs);
