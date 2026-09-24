import { LitElement, html } from 'lit';
import Hls from 'hls.js';
import { api } from '../lib/api.js';

// Server-backed history and controls survive browser refresh.

class CcLiveStreams extends LitElement {
  static properties = {
    rows:           { state: true },
    stopping:       { state: true },
    activePlayId:   { state: true },
    error:          { state: true },
  };

  constructor() {
    super();
    this.rows = [];
    this.stopping = new Set();
    this._poller = null;
    this._loadGeneration = 0;
    this.activePlayId = '';
    this.error = '';
    this._hls = null;
  }

  createRenderRoot() { return this; }

  async connectedCallback() {
    super.connectedCallback();
    await this.#load(++this._loadGeneration);
  }

  async #load(generation) {
    try {
      const data = await api('/portal/live-streams?limit=100');
      if (!this.isConnected || generation !== this._loadGeneration) return;
      this.rows = data?.items ?? [];
      this.error = '';
      if (this.activePlayId && !this.rows.some(s => s.id === this.activePlayId && !terminal(s) && s.status !== 'ending')) {
        if (this._hls) { this._hls.destroy(); this._hls = null; }
        this.activePlayId = '';
      }
      this.stopping = new Set([...this.stopping].filter(id => this.rows.some(s => s.id === id && !terminal(s))));
    } catch (err) {
      if (this.isConnected && generation === this._loadGeneration) this.error = err.message;
    } finally {
      if (this.isConnected && generation === this._loadGeneration) this._poller = setTimeout(() => this.#load(generation), 3000);
    }
  }

  async #stop(session) {
    if (terminal(session) || session.status === 'ending' || this.stopping.has(session.id)) return;
    this.stopping = new Set([...this.stopping, session.id]);
    this.error = '';
    try {
      await api(`/portal/live-streams/${session.id}`, { method: 'DELETE' });
      this.rows = this.rows.map(s => s.id === session.id ? { ...s, status: 'ending' } : s);
      if (this.activePlayId === session.id) {
        if (this._hls) { this._hls.destroy(); this._hls = null; }
        const video = this.renderRoot.querySelector('video');
        if (video) { video.pause(); video.removeAttribute('src'); video.load(); }
        this.activePlayId = '';
      }
    } catch (err) {
      this.stopping = new Set([...this.stopping].filter(id => id !== session.id));
      this.error = `Stop failed. You can retry: ${err.message}`;
    }
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    clearTimeout(this._poller);
    this._loadGeneration++;
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
          <code>POST /v1/live</code>. Stop ends the session and finalizes usage,
          even if your encoder has already disconnected.
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
                    <td>${pill(this.stopping.has(s.id) && !terminal(s) ? 'ending' : s.status)}
                      ${s.output_state ? html`<br><span class="msg">Output: ${s.output_state}</span>` : ''}
                      ${s.close_reason ? html`<br><span class="msg">${s.close_reason}</span>` : ''}
                      ${s.error_text
                        ? html`<br><span class="msg error">${s.error_text}</span>`
                        : ''}</td>
                    <td>${new Date(s.created_at).toLocaleString()}</td>
                    <td>${s.ended_at
                      ? html`<span class="msg">${new Date(s.ended_at).toLocaleString()}</span>`
                      : html`<span class="msg">—</span>`}</td>
                    <td>${!terminal(s) && s.playback_url && s.status !== 'ending' && !this.stopping.has(s.id)
                      ? html`<button class="ghost" @click=${() => this.#play(s.id, s.playback_url)}>Play</button>`
                      : ''}
                      ${!terminal(s)
                        ? html`<button class="ghost danger"
                            ?disabled=${s.status === 'ending' || this.stopping.has(s.id)}
                            @click=${() => this.#stop(s)}>
                            ${s.status === 'ending' || this.stopping.has(s.id) ? 'Stopping…' : 'Stop'}
                          </button>
                          <button class="ghost" @click=${() => { sessionStorage.setItem('lvp_live_selection', s.id); location.hash = '#/playground'; }}>Manage</button>`
                        : ''}</td>
                  </tr>`,
                )}
              </tbody>
            </table>`}
        ${this.activePlayId
          ? html`<h3>Playback</h3>
                 <video controls autoplay muted></video>`
          : ''}
      </div>
    `;
  }
}

function terminal(s) { return s.status === 'ended' || s.status === 'failed'; }

function pill(status) {
  const cls = status === 'live'   ? 'ok'
           : status === 'failed'  ? 'warn'
           : '';
  return html`<span class="pill ${cls}">${status}</span>`;
}

customElements.define('cc-live-streams', CcLiveStreams);
