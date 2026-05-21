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
                <th>ID</th><th>Status</th>
                <th>Ingest</th><th>Output</th>
                <th>Broker</th><th>Playback</th>
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
                    <td>${ingestCell(s.runner_status)}</td>
                    <td>${outputCell(s.runner_status)}</td>
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

// ── runner-status surface (broker GET /v1/cap/{bsess}.ingest + .output) ──
// Cached by the reconciler each tick; absent for legacy rows or when
// the broker doesn't include the status-hardening surface yet.
function ingestCell(rs) {
  const ingest = rs?.ingest;
  if (!ingest) return html`<span class="msg">—</span>`;
  // connected_publisher is the live "is OBS pushing right now" signal.
  // ListenerBound + Authenticated are setup-time gates.
  const liveSignal = ingest.connected_publisher
    ? html`<span class="pill ok">publishing</span>`
    : ingest.listener_bound
      ? html`<span class="pill">ready</span>`
      : html`<span class="pill warn">not bound</span>`;
  return html`${liveSignal}
    ${ingest.last_packet_at
      ? html`<br><span class="msg">last frame: ${relTime(ingest.last_packet_at)}</span>`
      : ''}`;
}

function outputCell(rs) {
  const output = rs?.output;
  if (!output) return html`<span class="msg">—</span>`;
  const failures = Number(output.put_failure_count || 0);
  const successes = Number(output.put_success_count || 0);
  const healthy = successes > 0 && failures === 0;
  const pill = failures > 0
    ? html`<span class="pill warn">${failures} failed</span>`
    : healthy
      ? html`<span class="pill ok">${successes} ok</span>`
      : html`<span class="pill">idle</span>`;
  return html`${pill}
    ${output.last_put_error
      ? html`<br><span class="msg error" title=${output.last_put_error}>
          ${truncate(output.last_put_error, 60)}
        </span>`
      : output.last_segment_put_at
        ? html`<br><span class="msg">last upload: ${relTime(output.last_segment_put_at)}</span>`
        : ''}`;
}

// relTime: "12s ago", "3m ago", "2h ago", or absolute for old.
function relTime(iso) {
  if (!iso) return '—';
  const t = new Date(iso).getTime();
  if (!Number.isFinite(t)) return iso;
  const s = Math.max(0, Math.floor((Date.now() - t) / 1000));
  if (s < 60)   return s + 's ago';
  if (s < 3600) return Math.floor(s / 60) + 'm ago';
  if (s < 86400) return Math.floor(s / 3600) + 'h ago';
  return new Date(iso).toLocaleString();
}

function truncate(s, n) { return s.length > n ? s.slice(0, n - 1) + '…' : s; }

customElements.define('cc-live-streams', CcLiveStreams);
