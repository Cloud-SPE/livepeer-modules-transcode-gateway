import { LitElement, html } from 'lit';
import Hls from 'hls.js';
import { api } from '/lib/api.js';

// Portal-side history of the user's RTMP→HLS sessions. Server-side
// state — survives clearing localStorage. Playback URLs are live for
// sessions that completed; for active sessions, the broker URL ticks
// segments live.

class CcLiveStreams extends LitElement {
  static properties = {
    rows:           { state: true },
    activePlayId:   { state: true },
    error:          { state: true },
  };

  constructor() {
    super();
    this.rows = [];
    this.activePlayId = '';
    this.error = '';
    this._hls = null;
  }

  createRenderRoot() { return this; }

  async connectedCallback() {
    super.connectedCallback();
    try {
      const data = await api('/portal/live-streams?limit=100');
      this.rows = data?.items ?? [];
    } catch (err) {
      this.error = err.message;
    }
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    if (this._hls) { this._hls.destroy(); this._hls = null; }
  }

  async #play(id, url) {
    if (!url) return;
    this.activePlayId = id;
    await this.updateComplete;
    const video = this.renderRoot.querySelector('video');
    if (!video) return;
    if (this._hls) { this._hls.destroy(); this._hls = null; }
    if (Hls.isSupported()) {
      const hls = new Hls();
      hls.loadSource(url);
      hls.attachMedia(video);
      this._hls = hls;
    } else if (video.canPlayType('application/vnd.apple.mpegurl')) {
      video.src = url;
    }
  }

  render() {
    return html`
      <div class="card">
        <h2>Live streams</h2>
        <p class="msg">
          RTMP ingest sessions you've allocated via the Playground or
          <code>POST /v1/live</code>. Stream keys are not stored here —
          they're shown exactly once when you create the session.
        </p>
        ${this.error ? html`<p class="msg error">${this.error}</p>` : ''}
        ${this.rows.length === 0
          ? html`<p class="msg">No live sessions yet.</p>`
          : html`<table>
              <thead><tr>
                <th>Name</th><th>Status</th>
                <th>Created</th><th>Ended</th><th></th>
              </tr></thead>
              <tbody>
                ${this.rows.map(
                  (s) => html`<tr>
                    <td>${s.name || html`<span class="msg">(unnamed)</span>`}
                      <br><span class="msg"><code>${s.id.slice(0,8)}…</code></span></td>
                    <td>${pill(s.status)}
                      ${s.error_text
                        ? html`<br><span class="msg error">${s.error_text}</span>`
                        : ''}</td>
                    <td>${new Date(s.created_at).toLocaleString()}</td>
                    <td>${s.ended_at
                      ? html`<span class="msg">${new Date(s.ended_at).toLocaleString()}</span>`
                      : html`<span class="msg">—</span>`}</td>
                    <td>${s.playback_url
                      ? html`<button class="ghost" @click=${() => this.#play(s.id, s.playback_url)}>Play</button>`
                      : html`<span class="msg">—</span>`}</td>
                  </tr>`,
                )}
              </tbody>
            </table>`}
        ${this.activePlayId
          ? html`<h3 style="margin-top:24px">Playback</h3>
                 <video controls autoplay muted></video>`
          : ''}
      </div>
    `;
  }
}

function pill(status) {
  const cls = status === 'live'   ? 'ok'
           : status === 'failed'  ? 'warn'
           : '';
  return html`<span class="pill ${cls}">${status}</span>`;
}

customElements.define('cc-live-streams', CcLiveStreams);
