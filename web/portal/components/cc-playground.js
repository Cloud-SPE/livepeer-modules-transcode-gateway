import { LitElement, html } from 'lit';
import Hls from 'hls.js';
import { api } from '../lib/api.js';

// Two tabs: Live RTMP→HLS stream + VOD ABR ladder.
//
// Each tab is gated on /v1/capabilities — if the network isn't currently
// advertising the underlying capability, the action UI is disabled with
// a clear explanation instead of letting the user upload bytes that
// can't be transcoded.
//
// VOD uploads persist in localStorage so a failed dispatch (e.g. the
// network has no abr-ladder broker right now) doesn't lose the file
// the user already pushed to S3. Each entry can be re-submitted
// when the capability comes back online.

const KEY_STORAGE      = 'lvp_video_api_key';
const UPLOADS_STORAGE  = 'lvp_video_uploads';
const ABR_CAP_NAME     = 'video:transcode.abr';
const LIVE_CAP_NAME    = 'video:transcode.live';

class CcPlayground extends LitElement {
  static properties = {
    activeTab:        { state: true },
    apiKey:           { state: true },
    error:            { state: true },

    // capabilities probe
    capsLoading:      { state: true },
    abrOnline:        { state: true },
    liveOnline:       { state: true },

    // live
    liveSession:      { state: true },
    liveBusy:         { state: true },
    livePlaying:      { state: true },

    // transcode (VOD ABR)
    uploads:          { state: true },
    activeUploadID:   { state: true },
    uploading:        { state: true },
    uploadPct:        { state: true },
  };

  constructor() {
    super();
    this.activeTab      = 'live';
    this.apiKey         = window.__lvpApiKey || sessionStorage.getItem(KEY_STORAGE) || '';
    this.error          = '';
    this.capsLoading    = true;
    this.abrOnline      = false;
    this.liveOnline     = false;
    this.liveSession    = null;
    this.liveBusy       = false;
    this.livePlaying    = false;
    this.uploads        = readUploads();
    this.activeUploadID = '';
    this.uploading      = false;
    this.uploadPct      = 0;
    this._hls           = null;
    this._pollers       = new Map(); // uploadId -> intervalId
  }

  createRenderRoot() { return this; }

  async connectedCallback() {
    super.connectedCallback();
    await this.#loadCaps();
    // Resume polling any uploads that have a running job.
    for (const u of this.uploads) {
      if (u.job && u.job.id && u.job.status !== 'succeeded' && u.job.status !== 'failed') {
        this.#startPoll(u.id);
      }
    }
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    this.#tearDownHls();
    for (const id of this._pollers.values()) clearInterval(id);
    this._pollers.clear();
  }

  // ── render ──
  render() {
    return html`
      <div class="card">
        <h2>Playground</h2>
        <p class="msg">
          Exercise <code>/v1/live</code> and <code>/v1/abr</code> against the network.
          Paste your API key once — it's kept in memory for this session only.
        </p>
        ${this.apiKey
          ? html`<p class="msg ok">Using key <code>${this.apiKey.slice(0, 8)}…</code>
                  <button class="ghost" @click=${this.#changeKey}>Change</button></p>`
          : html`<form @submit=${this.#saveKey}>
              <input name="key" type="password" placeholder="sk-…" required>
              <button class="primary" type="submit">Use this key</button>
            </form>`}
        ${this.error ? html`<p class="msg error">${this.error}</p>` : ''}
      </div>

      <div class="card">
        <div class="tabs">
          <button class=${this.activeTab === 'live'      ? 'active' : ''} @click=${() => (this.activeTab = 'live')}>
            Live ${this.#tabBadge(this.liveOnline)}
          </button>
          <button class=${this.activeTab === 'transcode' ? 'active' : ''} @click=${() => (this.activeTab = 'transcode')}>
            Transcode ${this.#tabBadge(this.abrOnline)}
          </button>
        </div>
        ${this.activeTab === 'live' ? this.#renderLive() : this.#renderTranscode()}
      </div>
    `;
  }

  #tabBadge(online) {
    if (this.capsLoading) return html`<span class="pill">…</span>`;
    return online
      ? html`<span class="pill ok">online</span>`
      : html`<span class="pill warn">offline</span>`;
  }

  // ── Live tab ──
  #renderLive() {
    if (this.capsLoading) return html`<p class="msg">Checking network…</p>`;
    if (!this.liveOnline) return this.#renderOfflineNotice(LIVE_CAP_NAME, 'Live RTMP→HLS streaming');
    if (!this.apiKey)     return html`<p class="msg">Paste an API key above to use the live playground.</p>`;

    if (!this.liveSession) {
      return html`
        <p class="msg">Allocate an RTMP ingest + HLS egress session. Push to the
          returned URL with OBS or <code>ffmpeg</code>; playback appears below
          when ingest is detected.</p>
        <button class="primary" ?disabled=${this.liveBusy} @click=${this.#createLive}>
          ${this.liveBusy ? 'Allocating…' : 'Create stream'}
        </button>
      `;
    }
    const s = this.liveSession;
    return html`
      <p><strong>Stream ID:</strong> <code>${s.id}</code></p>
      <p><strong>Status:</strong> ${s.status}</p>
      <p><strong>Ingest URL:</strong> <code>${s.ingest.rtmp_url}</code></p>
      <p><strong>Stream key:</strong> <code>${s.ingest.stream_key}</code>
        <span class="msg">(one-time)</span></p>
      <details>
        <summary>OBS hint</summary>
        <pre class="key">Settings → Stream → Service: Custom
Server: ${s.ingest.rtmp_url}
Stream Key: ${s.ingest.stream_key}</pre>
      </details>
      <p><strong>Playback:</strong> <code>${s.playback.hls_url}</code></p>
      <video controls muted></video>
      <div style="display:flex; gap:8px; margin-top:8px">
        <button class="ghost" @click=${this.#playLive} ?disabled=${this.livePlaying}>
          ${this.livePlaying ? 'Loaded — use video controls' : '▶ Load preview'}
        </button>
        <button class="ghost danger" @click=${this.#stopLive}>Stop stream</button>
      </div>
      <p class="msg" style="margin-top:8px">
        Click <strong>Load preview</strong> once your encoder is actually
        publishing — the playlist isn't written until media starts flowing.
      </p>
    `;
  }

  async #createLive() {
    this.liveBusy = true; this.error = '';
    this.livePlaying = false;
    this.#tearDownHls();
    try {
      const data = await api('/v1/live', {
        method: 'POST',
        headers: { Authorization: `Bearer ${this.apiKey}` },
        body: { name: 'playground' },
      });
      this.liveSession = data.session;
      // Don't auto-attach hls.js — the master.m3u8 doesn't exist until
      // the customer's encoder has been pushing for a few seconds AND
      // the runner has uploaded the first segments. Wait for the user
      // to click "Load preview" so we don't spam 404 retries.
    } catch (err) {
      this.error = err.message;
    } finally {
      this.liveBusy = false;
    }
  }

  #playLive = () => {
    if (!this.liveSession?.playback?.hls_url) return;
    this.#attachHls(this.liveSession.playback.hls_url);
    this.livePlaying = true;
  };

  async #stopLive() {
    if (!this.liveSession) return;
    try {
      await api(`/v1/live/${this.liveSession.id}`, {
        method: 'DELETE',
        headers: { Authorization: `Bearer ${this.apiKey}` },
      });
    } catch (err) {
      this.error = err.message;
    } finally {
      this.#tearDownHls();
      this.liveSession = null;
      this.livePlaying = false;
    }
  }

  // ── Transcode tab ──
  #renderTranscode() {
    if (this.capsLoading) return html`<p class="msg">Checking network…</p>`;
    return html`
      ${this.abrOnline
        ? html`
          <p class="msg">Drop a video file. The portal presigns a S3 PUT,
            uploads bytes directly, then submits <code>/v1/abr</code> for an
            ABR ladder transcode.</p>
          <div class="drop-zone"
               @click=${() => this.renderRoot.querySelector('input[type=file]')?.click()}
               @dragover=${(e) => { e.preventDefault(); e.currentTarget.classList.add('over'); }}
               @dragleave=${(e) => e.currentTarget.classList.remove('over')}
               @drop=${this.#onDrop}>
            ${this.uploading
              ? html`<div>Uploading… ${this.uploadPct}%
                  <div class="progress-bar"><span style="width:${this.uploadPct}%"></span></div>
                </div>`
              : html`<div>Drop a video file here, or click to choose</div>`}
            <input type="file" accept="video/*" hidden @change=${this.#onFile}>
          </div>`
        : this.#renderOfflineNotice(ABR_CAP_NAME, 'VOD ABR ladder transcoding')}

      ${this.uploads.length === 0
        ? ''
        : html`
          <h3 style="margin-top:24px">Your uploads</h3>
          <p class="msg">Stored in this browser. S3 keeps the source bytes;
            jobs can be re-submitted if the capability was offline.</p>
          <table>
            <thead><tr>
              <th>File</th><th>Uploaded</th><th>Job</th><th>Status</th><th></th>
            </tr></thead>
            <tbody>
              ${this.uploads.map((u) => this.#renderUploadRow(u))}
            </tbody>
          </table>`}

      ${this.activeUploadID
        ? html`
          <h3 style="margin-top:24px">Playback</h3>
          <video controls autoplay muted></video>`
        : ''}
    `;
  }

  #renderUploadRow(u) {
    const status = u.job?.status || 'not submitted';
    const done   = status === 'succeeded';
    const failed = status === 'failed';
    const errCode = u.job?.error_code || '';
    const errText = u.job?.error || '';
    return html`<tr>
      <td>${u.filename}
        ${u.duration_seconds
          ? html`<br><span class="msg">${formatDuration(u.duration_seconds)}</span>`
          : ''}
      </td>
      <td>${new Date(u.uploaded_at).toLocaleString()}</td>
      <td>${u.job?.id ? html`<code>${u.job.id.slice(0, 8)}…</code>` : html`<span class="msg">—</span>`}</td>
      <td><span class="pill ${done ? 'ok' : failed ? 'warn' : ''}">${status}</span></td>
      <td>
        ${done && u.job?.master_playlist_url
          ? html`<button class="ghost" @click=${() => this.#play(u.id)}>Play</button>`
          : ''}
        ${this.abrOnline && !done
          ? html`<button class="primary" @click=${() => this.#submitJob(u.id)}>
              ${status === 'not submitted' ? 'Transcode' : 'Retry'}
            </button>`
          : ''}
        <button class="ghost danger" @click=${() => this.#removeUpload(u.id)}>Delete</button>
      </td>
    </tr>
    ${failed
      ? html`<tr class="error-row"><td colspan="5">
          ${errCode ? html`<code>${errCode}</code>` : ''}
          ${errCode && errText ? html`<br>` : ''}
          ${errText
            ? html`<span class="msg">${errText}</span>`
            : !errCode
              ? html`<span class="msg">Transcode failed. No detail reported by the runner.</span>`
              : ''}
        </td></tr>`
      : ''}`;
  }

  #renderOfflineNotice(cap, friendly) {
    return html`
      <div class="card" style="border-color:var(--danger)">
        <h3>${friendly} is offline</h3>
        <p class="msg">No orchestrator on the Livepeer network is currently
          advertising <code>${cap}</code>. This is the on-chain truth — the
          gateway doesn't fake it. Try again later, or watch the
          <a href="#/health">Health</a> tab.</p>
      </div>`;
  }

  // ── upload flow ──
  #onDrop = (e) => {
    e.preventDefault();
    e.currentTarget.classList.remove('over');
    const f = e.dataTransfer.files?.[0];
    if (f) this.#startUpload(f);
  };
  #onFile = (e) => {
    const f = e.target.files?.[0];
    if (f) this.#startUpload(f);
  };

  async #startUpload(file) {
    if (!this.apiKey) { this.error = 'API key required'; return; }
    this.error = ''; this.uploading = true; this.uploadPct = 0;
    try {
      // Probe the video's duration first so the gateway can size the
      // payment to the actual workload (work_unit = seconds for live,
      // jobs for VOD ABR — but we still pass an estimate so the
      // payer-daemon mints a face value proportional to the work).
      const durationSeconds = await this.#probeDuration(file);
      const presign = await api('/v1/abr/upload-url', {
        method: 'POST',
        headers: { Authorization: `Bearer ${this.apiKey}` },
        body: { filename: file.name, content_type: file.type || 'video/mp4' },
      });
      await this.#putBytes(presign.upload_url, file);
      const entry = {
        id: crypto.randomUUID(),
        filename: file.name,
        object_url: presign.object_url,
        uploaded_at: new Date().toISOString(),
        duration_seconds: durationSeconds,
        job: null,
      };
      this.uploads = [entry, ...this.uploads];
      writeUploads(this.uploads);
      // Auto-submit when the capability is online.
      if (this.abrOnline) await this.#submitJob(entry.id);
    } catch (err) {
      this.error = err.message;
    } finally {
      this.uploading = false;
    }
  }

  // #probeDuration reads the video's metadata locally to figure out how
  // many seconds of content the runner will have to process. Falls back
  // to 60 if the browser can't decode the file's headers (e.g. uncommon
  // container) so the payment is at least funded for a minute of work.
  #probeDuration(file) {
    return new Promise((resolve) => {
      const v = document.createElement('video');
      v.preload = 'metadata';
      const url = URL.createObjectURL(file);
      const cleanup = () => { URL.revokeObjectURL(url); };
      v.onloadedmetadata = () => {
        const sec = Math.ceil(v.duration);
        cleanup();
        resolve(Number.isFinite(sec) && sec > 0 ? sec : 60);
      };
      v.onerror = () => { cleanup(); resolve(60); };
      v.src = url;
    });
  }

  #putBytes(url, file) {
    return new Promise((resolve, reject) => {
      const xhr = new XMLHttpRequest();
      xhr.open('PUT', url);
      xhr.setRequestHeader('Content-Type', file.type || 'video/mp4');
      xhr.upload.onprogress = (ev) => {
        if (ev.lengthComputable) this.uploadPct = Math.round((ev.loaded / ev.total) * 100);
      };
      xhr.onload  = () => (xhr.status >= 200 && xhr.status < 300 ? resolve() : reject(new Error(`upload failed: ${xhr.status}`)));
      xhr.onerror = () => reject(new Error('upload failed: network'));
      xhr.send(file);
    });
  }

  async #submitJob(uploadId) {
    const u = this.uploads.find((x) => x.id === uploadId);
    if (!u) return;
    this.error = '';
    try {
      const body = {
        input_url: u.object_url,
        preset: 'abr-standard',
      };
      if (u.duration_seconds && u.duration_seconds > 0) {
        body.estimated_input_seconds = u.duration_seconds;
      }
      const r = await api('/v1/abr', {
        method: 'POST',
        headers: { Authorization: `Bearer ${this.apiKey}` },
        body,
      });
      this.#updateUpload(uploadId, { job: { ...r.job, status: r.job.status || 'running' } });
      this.#startPoll(uploadId);
    } catch (err) {
      this.error = `Transcode dispatch failed: ${err.message}`;
      this.#updateUpload(uploadId, { job: { ...(u.job || {}), status: 'failed', error: err.message } });
    }
  }

  #startPoll(uploadId) {
    if (this._pollers.has(uploadId)) return;
    const u = this.uploads.find((x) => x.id === uploadId);
    if (!u || !u.job?.id) return;
    const id = setInterval(async () => {
      try {
        const r = await api(`/v1/abr/${u.job.id}`, {
          headers: { Authorization: `Bearer ${this.apiKey}` },
        });
        const cur = this.uploads.find((x) => x.id === uploadId);
        const merged = { ...(cur?.job || {}), ...r.job };
        this.#updateUpload(uploadId, { job: merged });
        if (merged.status === 'succeeded' || merged.status === 'failed') {
          clearInterval(id);
          this._pollers.delete(uploadId);
        }
      } catch {/* transient */}
    }, 3000);
    this._pollers.set(uploadId, id);
  }

  #updateUpload(uploadId, patch) {
    this.uploads = this.uploads.map((u) => (u.id === uploadId ? { ...u, ...patch } : u));
    writeUploads(this.uploads);
  }

  async #removeUpload(uploadId) {
    const u = this.uploads.find((x) => x.id === uploadId);
    if (!u) return;
    if (!confirm(`Delete ${u.filename}? This removes the uploaded source and any transcode outputs from storage.`)) {
      return;
    }
    if (this._pollers.has(uploadId)) {
      clearInterval(this._pollers.get(uploadId));
      this._pollers.delete(uploadId);
    }
    // Best-effort GC the bucket objects before clearing local state. We
    // do this BEFORE local removal so a failure leaves the entry visible
    // (and retry-able) instead of orphaning bytes in S3.
    if (u.object_url || u.job?.id) {
      try {
        await api('/v1/abr/objects', {
          method: 'DELETE',
          headers: { Authorization: `Bearer ${this.apiKey}` },
          body: {
            object_url: u.object_url || undefined,
            work_id:    u.job?.id   || undefined,
          },
        });
      } catch (err) {
        // 4xx is rare here (auth/namespace); 5xx means the bucket call
        // failed. Either way, surface and bail so the user can retry.
        this.error = `Delete failed: ${err.message}`;
        return;
      }
    }
    this.uploads = this.uploads.filter((u) => u.id !== uploadId);
    writeUploads(this.uploads);
    if (this.activeUploadID === uploadId) {
      this.activeUploadID = '';
      this.#tearDownHls();
    }
  }

  async #play(uploadId) {
    const u = this.uploads.find((x) => x.id === uploadId);
    if (!u?.job?.master_playlist_url) return;
    this.activeUploadID = uploadId;
    await this.updateComplete;
    this.#attachHls(u.job.master_playlist_url);
  }

  // ── capability probe ──
  async #loadCaps() {
    this.capsLoading = true;
    try {
      const key = this.apiKey;
      const headers = key ? { Authorization: `Bearer ${key}` } : {};
      const r = await api('/v1/capabilities', { headers });
      const ids = new Set((r?.data ?? []).map((c) => c.capability));
      this.abrOnline  = ids.has(ABR_CAP_NAME);
      this.liveOnline = ids.has(LIVE_CAP_NAME);
    } catch {
      this.abrOnline = false;
      this.liveOnline = false;
    } finally {
      this.capsLoading = false;
    }
  }

  // ── HLS player helper ──
  #attachHls(url) {
    const video = this.renderRoot.querySelector('video');
    if (!video) return;
    this.#tearDownHls();
    if (Hls.isSupported()) {
      const hls = new Hls();
      hls.loadSource(url);
      hls.attachMedia(video);
      this._hls = hls;
    } else if (video.canPlayType('application/vnd.apple.mpegurl')) {
      video.src = url;
    }
  }
  #tearDownHls() {
    if (this._hls) { this._hls.destroy(); this._hls = null; }
  }

  // ── API key handling ──
  #saveKey = (ev) => {
    ev.preventDefault();
    const fd = new FormData(ev.currentTarget);
    const key = String(fd.get('key') || '').trim();
    if (!key) return;
    this.apiKey = key;
    window.__lvpApiKey = key;
    sessionStorage.setItem(KEY_STORAGE, key);
    void this.#loadCaps();
  };
  #changeKey = () => {
    this.apiKey = '';
    sessionStorage.removeItem(KEY_STORAGE);
    window.__lvpApiKey = '';
  };
}

function formatDuration(sec) {
  if (!sec || sec < 0) return '';
  const m = Math.floor(sec / 60);
  const s = sec % 60;
  return `${m}:${String(s).padStart(2, '0')}`;
}

function readUploads() {
  try {
    const raw = localStorage.getItem(UPLOADS_STORAGE);
    if (!raw) return [];
    const arr = JSON.parse(raw);
    return Array.isArray(arr) ? arr : [];
  } catch { return []; }
}

function writeUploads(uploads) {
  try { localStorage.setItem(UPLOADS_STORAGE, JSON.stringify(uploads)); }
  catch {/* quota — non-fatal */}
}

customElements.define('cc-playground', CcPlayground);
