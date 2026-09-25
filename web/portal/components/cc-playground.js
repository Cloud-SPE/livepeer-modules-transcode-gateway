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
const LIVE_SELECTION = 'lvp_live_selection';
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
    liveSessions: { state: true },
    liveRestoring: { state: true },
    liveBusy:         { state: true },
    livePlaying:      { state: true },
    liveStatusError:  { state: true },

    // transcode (VOD ABR)
    uploads:          { state: true },
    activeUploadID:   { state: true },
    uploading:        { state: true },
    uploadPct:        { state: true },
    expandedJobs:     { state: true },  // Set<uploadId> with variants visible
    copiedURL:        { state: true },  // last URL copied (for transient feedback)
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
    this.liveSessions = [];
    this.liveRestoring = true;
    this._livePortal = false;
    this.liveBusy       = false;
    this.livePlaying    = false;
    this.liveStatusError = "";
    this._livePoller = null;
    this._livePollGeneration = 0;
    this._liveCreateKey = "";
    this.uploads        = readUploads();
    this.activeUploadID = '';
    this.uploading      = false;
    this.uploadPct      = 0;
    this.expandedJobs   = new Set();
    this.copiedURL      = '';
    this._hls           = null;
    this._pollers       = new Map(); // uploadId -> intervalId
  }

  createRenderRoot() { return this; }

  async connectedCallback() {
    super.connectedCallback();
    await Promise.all([this.#loadCaps(), this.#restoreLive()]);
    if (!this.isConnected) return;
    if (needsLivePolling(this.liveSession)) this.#startLivePoll();
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
    this.#stopLivePoll();
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
        ${this.activeTab === 'live' ? html`${this.#renderLiveSelection()}${this.#renderLive()}` : this.#renderTranscode()}
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
  #renderLiveSelection() {
    if (!this.liveSessions.length) return '';
    return html`<label for="live-session-select">Your active streams</label>
      <select id="live-session-select" ?disabled=${this.liveBusy || this.liveRestoring}
        @change=${(e) => this.#selectLive(e.target.value)}>
        <option value="">Select a stream</option>
        ${this.liveSessions.map(s => html`<option value=${s.id} ?selected=${s.id === this.liveSession?.id}>${s.name || s.id} — ${s.status}</option>`)}
      </select>`;
  }

  async #restoreLive() {
    this.liveRestoring = true;
    try {
      const data = await api('/portal/live-streams?limit=500');
      if (!this.isConnected) return;
      this.liveSessions = (data?.items || []).filter(s => needsLivePolling(s));
      const selected = sessionStorage.getItem(LIVE_SELECTION);
      const session = this.liveSessions.find(s => s.id === selected) || this.liveSessions[0];
      if (!this.liveSession && session) await this.#selectLive(session.id);
    } catch (err) {
      if (this.isConnected) this.liveStatusError = `Could not restore streams: ${err.message}`;
    } finally {
      this.liveRestoring = false;
    }
  }

  async #selectLive(id) {
    if (!id || this.liveBusy) return;
    this.liveBusy = true;
    this.#stopLivePoll();
    const generation = this._livePollGeneration;
    try {
      const data = await api(`/portal/live-streams/${encodeURIComponent(id)}`);
      if (!this.isConnected || generation !== this._livePollGeneration) return;
      if (data?.session?.id !== id) throw new Error('Unexpected stream response');
      this.#tearDownHls();
      this.livePlaying = false;
      this._livePortal = true;
      this.liveSession = data.session;
      this.liveStatusError = '';
      sessionStorage.setItem(LIVE_SELECTION, id);
    } catch (err) {
      if (this.isConnected) this.liveStatusError = err.message;
    } finally {
      this.liveBusy = false;
      this.#startLivePoll();
    }
  }

  #renderLive() {
    if (this.liveRestoring) return html`<p class="msg">Restoring your streams…</p>`;
    if (!this.liveSession) {
      if (this.liveStatusError) return html`<p class="msg warn">${this.liveStatusError}</p><button @click=${() => this.#restoreLive()}>Retry restoring streams</button>`;
      if (this.capsLoading) return html`<p class="msg">Checking network…</p>`;
      if (!this.liveOnline) return this.#renderOfflineNotice(LIVE_CAP_NAME, 'Live RTMP→HLS streaming');
      if (!this.apiKey) return html`<p class="msg">Paste an API key above to use the live playground.</p>`;
      return html`
        <p class="msg">Create a stream, then publish with OBS or <code>ffmpeg</code>
          once the ingest details are ready.</p>
        <button class="primary" ?disabled=${this.liveBusy} @click=${this.#createLive}>
          ${this.liveBusy ? 'Allocating…' : this._liveCreateKey ? 'Retry stream creation' : 'Create stream'}
        </button>`;
    }
    const s = this.liveSession;
    const terminal = isLiveTerminal(s);
    const ending = s.status === 'ending';
    const ready = !terminal && !ending && s.status !== 'provisioning' && s.ingest?.rtmp_url && s.ingest?.stream_key;
    return html`
      <p><strong>Stream ID:</strong> <code>${s.id}</code></p>
      <p role="status"><strong>Status:</strong> ${s.status || 'provisioning'}
        ${s.output_state ? html` · Output: ${s.output_state}` : ''}</p>
      ${this.liveStatusError ? html`<p class="msg warn">${this.liveStatusError}</p>` : ''}
      ${s.settlement_pending ? html`<p class="msg">Stream ended. Final usage settlement is pending.</p>` : ''}
      ${s.last_failure_code === 'refill_refused' ? html`<p class="msg warn">The stream ended because its authorization refill was refused.</p>` : ''}
      ${s.last_failure_code || s.close_reason
        ? html`<p class="msg">${s.last_failure_code || s.close_reason}</p>` : ''}
      ${terminal
        ? html`<p class="msg">${s.status === 'failed' ? 'This stream failed.' : 'This stream has ended.'}</p>
            <button class="primary" @click=${this.#resetLive}>Create another stream</button>`
        : html`
          ${ready ? html`
            <p><strong>Ingest URL:</strong> <code>${s.ingest.rtmp_url}</code></p>
            <p><strong>Stream key:</strong> <code>${s.ingest.stream_key}</code></p>
            <details><summary>OBS hint</summary>
              <pre class="key">Settings → Stream → Service: Custom
Server: ${s.ingest.rtmp_url}
Stream Key: ${s.ingest.stream_key}</pre>
            </details>`
            : html`<p class="msg">${ending ? 'Stopping the stream and finalizing usage…' : 'Preparing your stream. Ingest details will appear when it is ready.'}</p>`}
          ${s.playback?.hls_url && !ending ? html`
            <p><strong>Playback:</strong> <code>${s.playback.hls_url}</code></p>
            <video controls muted></video>
            <button class="ghost" @click=${this.#playLive} ?disabled=${this.livePlaying}>
              ${this.livePlaying ? 'Loaded — use video controls' : '▶ Load preview'}
            </button>
            <p class="msg">Load the preview once your encoder is publishing.</p>` : ''}
          <button class="ghost danger" @click=${this.#stopLive} ?disabled=${this.liveBusy || ending}>
            ${this.liveBusy || ending ? 'Stopping…' : 'Stop stream'}
          </button>`}
    `;
  }

  async #createLive() {
    if (this.liveBusy || this.liveSession || !this.apiKey) return;
    this.liveBusy = true;
    this.error = '';
    this.livePlaying = false;
    this.liveStatusError = '';
    this.#tearDownHls();
    const key = this.apiKey;
    // Keep the key after a lost response so retry recovers the same paid session.
    this._liveCreateKey ||= crypto.randomUUID();
    try {
      const data = await api('/v1/live', {
        method: 'POST',
        headers: { Authorization: `Bearer ${key}`, 'Idempotency-Key': this._liveCreateKey },
        body: { name: 'playground' },
      });
      if (!data?.session?.id) throw new Error('Stream response did not include a session ID. Retry to recover it.');
      if (this.apiKey !== key) return;
      this._livePortal = false;
      this.liveSession = data.session;
      sessionStorage.setItem(LIVE_SELECTION, data.session.id);
      this.#startLivePoll();
    } catch (err) {
      this.error = err.message;
    } finally {
      this.liveBusy = false;
    }
  }

  #stopLivePoll() {
    clearTimeout(this._livePoller);
    this._livePoller = null;
    this._livePollGeneration++;
  }

  #startLivePoll() {
    this.#stopLivePoll();
    if (!this.isConnected || !this.liveSession?.id || !needsLivePolling(this.liveSession)) return;
    const sessionID = this.liveSession.id;
    const key = this.apiKey;
    const generation = this._livePollGeneration;
    const current = () => this.isConnected && generation === this._livePollGeneration && this.liveSession?.id === sessionID && this.apiKey === key;
    const poll = async () => {
      if (!current()) return;
      try {
        const data = await api(this._livePortal ? `/portal/live-streams/${sessionID}` : `/v1/live/${sessionID}`, this._livePortal ? {} : { headers: { Authorization: `Bearer ${key}` } });
        if (!current()) return;
        if (data?.session?.id !== sessionID) throw new Error('Unexpected stream status response');
        const previous = this.liveSession;
        this.liveSession = {
          ...previous, ...data.session,
          // GET deliberately omits the secret issued by create.
          ingest: { ...previous.ingest, ...data.session.ingest,
            stream_key: data.session.ingest?.stream_key || previous.ingest?.stream_key },
          playback: { ...previous.playback, ...data.session.playback },
        };
        this.liveStatusError = '';
        if (isLiveTerminal(this.liveSession)) {
          this.#tearDownHls();
          this.livePlaying = false;
          if (!this.liveSession.settlement_pending) {
            this.#stopLivePoll();
            return;
          }
        }
      } catch (err) {
        if (!current()) return;
        this.liveStatusError = `Status refresh failed; retrying: ${err.message}`;
      }
      if (current()) this._livePoller = setTimeout(poll, 3000);
    };
    this._livePoller = setTimeout(poll, 0);
  }

  #resetLive = () => {
    if (!isLiveTerminal(this.liveSession)) return;
    this.#stopLivePoll();
    this.#tearDownHls();
    this.liveSessions = this.liveSessions.filter(s => s.id !== this.liveSession?.id);
    sessionStorage.removeItem(LIVE_SELECTION);
    this.liveSession = null;
    this._liveCreateKey = '';
    this.livePlaying = false;
    this.liveStatusError = '';
    this.error = '';
  };

  #playLive = () => {
    if (!this.liveSession?.playback?.hls_url || isLiveTerminal(this.liveSession)) return;
    this.#attachHls(this.liveSession.playback.hls_url);
    this.livePlaying = true;
  };

  async #stopLive() {
    if (!this.liveSession || this.liveBusy || isLiveTerminal(this.liveSession)) return;
    const sessionID = this.liveSession.id;
    this.liveBusy = true;
    this.error = '';
    this.#stopLivePoll();
    try {
      await api(this._livePortal ? `/portal/live-streams/${sessionID}` : `/v1/live/${sessionID}`, {
        method: 'DELETE', headers: this._livePortal ? {} : { Authorization: `Bearer ${this.apiKey}` },
      });
      if (this.liveSession?.id !== sessionID) return;
      this.liveSession = { ...this.liveSession, status: 'ending' };
      this.#tearDownHls();
      this.livePlaying = false;
    } catch (err) {
      this.error = `Stop failed; the stream is still tracked. Retry stopping: ${err.message}`;
    } finally {
      this.liveBusy = false;
      this.#startLivePoll();
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
    const rejected = status === 'admission_rejected';
    const errCode = u.job?.error_code || '';
    const errText = u.job?.error || '';
    const masterURL = u.job?.master_playlist_url || '';
    const renditions = u.job?.renditions || [];
    const expanded = this.expandedJobs.has(u.id);
    return html`<tr>
      <td>${u.filename}
        ${u.duration_seconds
          ? html`<br><span class="msg">${formatDuration(u.duration_seconds)}</span>`
          : ''}
      </td>
      <td>${new Date(u.uploaded_at).toLocaleString()}</td>
      <td>${u.job?.id ? html`<code>${u.job.id.slice(0, 8)}…</code>` : html`<span class="msg">—</span>`}</td>
      <td><span class="pill ${done ? 'ok' : failed || rejected ? 'warn' : ''}">${rejected ? 'Settlement pending' : status}</span></td>
      <td>
        ${done && masterURL
          ? html`<button class="ghost" @click=${() => this.#play(u.id)}>Play</button>
                 <button class="ghost" @click=${() => this.#copyURL(masterURL)}
                         title="Copy master playlist URL to clipboard">
                   ${this.copiedURL === masterURL ? 'Copied' : 'Copy URL'}
                 </button>`
          : ''}
        ${done && renditions.length > 0
          ? html`<button class="ghost" @click=${() => this.#toggleVariants(u.id)}>
                   ${expanded ? '▾' : '▸'} ${renditions.length} variants
                 </button>`
          : ''}
        ${this.abrOnline && (failed || status === 'not submitted')
          ? html`<button class="primary" @click=${() => this.#submitJob(u.id)}>
              ${status === 'not submitted' ? 'Transcode' : 'Retry'}
            </button>`
          : ''}
        <button class="ghost danger" @click=${() => this.#removeUpload(u.id)}>Delete</button>
      </td>
    </tr>
    ${failed || rejected
      ? html`<tr class="error-row"><td colspan="5">
          ${errCode ? html`<code>${errCode}</code>` : ''}
          ${errCode && errText ? html`<br>` : ''}
          ${errText
            ? html`<span class="msg">${errText}</span>`
            : !errCode
              ? html`<span class="msg">Transcode failed. No detail reported by the runner.</span>`
              : ''}
        </td></tr>`
      : ''}
    ${expanded && done
      ? renditions.map((r) => html`<tr class="variant-row"><td colspan="5">
          <span class="variant-name">${r.name}</span>
          <span class="msg">${formatBitrate(r.bandwidth)}</span>
          <button class="ghost" @click=${() => this.#playURL(u.id, r.playlist_url)}>Play this one</button>
          <button class="ghost" @click=${() => this.#copyURL(r.playlist_url)}
                  title="Copy variant playlist URL to clipboard">
            ${this.copiedURL === r.playlist_url ? 'Copied' : 'Copy URL'}
          </button>
        </td></tr>`)
      : ''}`;
  }

  #toggleVariants(uploadId) {
    const next = new Set(this.expandedJobs);
    if (next.has(uploadId)) next.delete(uploadId);
    else next.add(uploadId);
    this.expandedJobs = next;
  }

  async #copyURL(url) {
    if (!url) return;
    try {
      await navigator.clipboard.writeText(url);
      this.copiedURL = url;
      setTimeout(() => {
        if (this.copiedURL === url) this.copiedURL = '';
        this.requestUpdate();
      }, 1500);
    } catch (err) {
      this.error = `Couldn't copy to clipboard: ${err.message}`;
    }
  }

  async #playURL(uploadId, url) {
    if (!url) return;
    this.activeUploadID = uploadId;
    await this.updateComplete;
    this.#attachHls(url);
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
      // Duration sizes the authorization cap; the runner settles measured
      // delivered frame-megapixels after transcoding.
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
    if (!u || !this.apiKey || (u.job && !['failed', 'not submitted'].includes(u.job.status))) return;
    this.error = '';
    if (this._pollers.has(uploadId)) {
      clearInterval(this._pollers.get(uploadId));
      this._pollers.delete(uploadId);
    }
    const requestKey = (u.job?.id || !u.request_key) ? crypto.randomUUID() : u.request_key;
    this.#updateUpload(uploadId, { request_key: requestKey, job: { status: 'submitting' } });
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
        headers: { Authorization: `Bearer ${this.apiKey}`, 'Idempotency-Key': requestKey },
        body,
      });
      if (!r?.job?.id) throw new Error('Missing job ID. Retry to recover the submission.');
      this.#updateUpload(uploadId, { job: { ...r.job, status: r.job.status || 'running' } });
      this.#startPoll(uploadId);
    } catch (err) {
      this.error = `Transcode dispatch failed: ${err.message}`;
      this.#updateUpload(uploadId, { job: { status: 'not submitted', error: err.message } });
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
        if (!this.isConnected || this._pollers.get(uploadId) !== id || cur?.job?.id !== u.job.id) return;
        const merged = r.job; // Each poll is authoritative, including cleared error fields.
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
    if (this.liveBusy || (this.liveSession && !isLiveTerminal(this.liveSession))) {
      this.error = 'Stop the current stream before changing API keys.';
      return;
    }
    if (this.liveSession) this.#resetLive();
    this._liveCreateKey = '';
    this.apiKey = '';
    sessionStorage.removeItem(KEY_STORAGE);
    window.__lvpApiKey = '';
  };
}

function needsLivePolling(session) { return Boolean(session && (!isLiveTerminal(session) || session.settlement_pending)); }

function isLiveTerminal(session) {
  return Boolean(session && (session.ended_at || ['ended', 'failed', 'closed', 'expired'].includes(session.status)));
}

function formatDuration(sec) {
  if (!sec || sec < 0) return '';
  const m = Math.floor(sec / 60);
  const s = sec % 60;
  return `${m}:${String(s).padStart(2, '0')}`;
}

// Render an HLS variant bandwidth (bits/sec) as a human-readable rate.
// HLS BANDWIDTH attributes are peak bits/sec including container overhead.
function formatBitrate(bps) {
  if (!bps || bps < 0) return '';
  if (bps < 1_000_000) return `~${Math.round(bps / 1000)} kbps`;
  return `~${(bps / 1_000_000).toFixed(1)} Mbps`;
}

function readUploads() {
  try {
    const raw = localStorage.getItem(UPLOADS_STORAGE);
    if (!raw) return [];
    const arr = JSON.parse(raw);
    return Array.isArray(arr) ? arr.map(u => u.job?.status === 'submitting' && !u.job?.id
      ? { ...u, job: { status: 'not submitted' } } : u) : [];
  } catch { return []; }
}

function writeUploads(uploads) {
  try { localStorage.setItem(UPLOADS_STORAGE, JSON.stringify(uploads)); }
  catch {/* quota — non-fatal */}
}

customElements.define('cc-playground', CcPlayground);
