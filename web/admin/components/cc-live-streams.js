import { LitElement, html } from 'lit';
import { api } from '/lib/api.js';

// Transcode-specific: RTMP→HLS sessions across all users. Defaults to
// active sessions only; toggle to see history.

class CcLiveStreams extends LitElement {
  static properties = {
    rows:       { state: true },
    activeOnly: { state: true },
    error:      { state: true },
  };

  constructor() {
    super();
    this.rows = [];
    this.activeOnly = true;
    this.error = '';
  }

  createRenderRoot() { return this; }

  async connectedCallback() {
    super.connectedCallback();
    await this.#load();
  }

  async #load() {
    try {
      const data = await api(`/admin/live-streams?active_only=${this.activeOnly}&limit=200`);
      this.rows = data?.items ?? [];
    } catch (err) {
      this.error = err.message;
    }
  }

  #toggle = async (e) => {
    this.activeOnly = e.target.checked;
    await this.#load();
  };

  render() {
    return html`
      <div class="card">
        <h2>Live streams (RTMP → HLS)</h2>
        <p class="msg">
          Each row is a session allocated via <code>POST /v1/live</code>.
          Status flows <code>provisioning → live → ended/failed</code>;
          if a session sits at <code>provisioning</code> with no heartbeat
          for more than a few minutes the runner likely never opened the
          RTMP ingest.
        </p>
        <label class="msg" style="display:flex; gap:8px; align-items:center; margin-bottom:12px">
          <input type="checkbox" .checked=${this.activeOnly} @change=${this.#toggle}>
          Active only (provisioning + live)
        </label>
        ${this.error ? html`<p class="msg error">${this.error}</p>` : ''}
        ${this.rows.length === 0
          ? html`<p class="msg">No sessions in this filter.</p>`
          : html`<table>
              <thead><tr>
                <th>ID</th><th>Status</th><th>Broker</th><th>Playback</th>
                <th>Started</th><th>Heartbeat</th><th>Ended</th>
              </tr></thead>
              <tbody>
                ${this.rows.map(
                  (s) => html`<tr>
                    <td>
                      <code>${s.id.slice(0, 8)}…</code>
                      ${s.name ? html`<br><span class="msg">${s.name}</span>` : ''}
                    </td>
                    <td>${statusPill(s.status)}
                      ${s.error_text
                        ? html`<br><span class="msg error">${s.error_text}</span>`
                        : ''}</td>
                    <td>${s.broker_url
                      ? html`<code>${shortHost(s.broker_url)}</code>`
                      : html`<span class="msg">—</span>`}</td>
                    <td>${s.playback_url
                      ? html`<a href=${s.playback_url} target="_blank">play</a>`
                      : html`<span class="msg">—</span>`}</td>
                    <td>${ts(s.started_at)}</td>
                    <td>${ts(s.last_heartbeat_at)}</td>
                    <td>${ts(s.ended_at)}</td>
                  </tr>`,
                )}
              </tbody>
            </table>`}
      </div>
    `;
  }
}

function ts(v) { return v ? html`<span class="msg">${new Date(v).toLocaleString()}</span>` : html`<span class="msg">—</span>`; }
function shortHost(url) { try { return new URL(url).host; } catch { return url; } }
function statusPill(status) {
  const cls = status === 'live'         ? 'ok'
            : status === 'ended'        ? ''
            : status === 'failed'       ? 'warn'
            : '';
  return html`<span class="pill ${cls}">${status}</span>`;
}

customElements.define('cc-live-streams', CcLiveStreams);
