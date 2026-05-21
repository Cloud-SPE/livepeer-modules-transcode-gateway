import { LitElement, html } from 'lit';
import { api } from '../lib/api.js';

class CcLogin extends LitElement {
  static properties = {
    state: { state: true },
    error: { state: true },
  };

  constructor() {
    super();
    this.state = 'idle';
    this.error = '';
  }

  createRenderRoot() { return this; }

  render() {
    return html`
      <main>
        <div class="card centered">
          <h2>Sign in</h2>
          <p class="msg">
            Use an active API key from an approved account to access the portal.
            The portal gives you account visibility, key management, usage history,
            and a live playground for VOD ABR + RTMP→HLS streaming.
          </p>
          <form class="stack-sm" @submit=${this.#onSubmit}>
            <input
              name="apiKey"
              type="password"
              placeholder="sk-..."
              autocomplete="current-password"
              required
            >
            <button class="primary" type="submit" ?disabled=${this.state === 'submitting'}>
              ${this.state === 'submitting' ? 'Signing in…' : 'Sign in'}
            </button>
          </form>
          ${this.error ? html`<p class="msg error">${this.error}</p>` : ''}
        </div>
      </main>
    `;
  }

  async #onSubmit(ev) {
    ev.preventDefault();
    const fd = new FormData(ev.currentTarget);
    this.state = 'submitting';
    this.error = '';
    try {
      // Remember the API key so the playground + network-health tabs can
      // call /v1/* with Bearer auth. sessionStorage survives reloads within
      // this tab; window.__lvpApiKey is the in-memory fast path.
      const key = String(fd.get('apiKey'));
      window.__lvpApiKey = key;
      sessionStorage.setItem('lvp_video_api_key', key);
      await api('/portal/login', {
        method: 'POST',
        body: { apiKey: key },
      });
      window.dispatchEvent(new Event('cc-login'));
    } catch (err) {
      this.error = err.message;
      this.state = 'idle';
    }
  }
}

customElements.define('cc-login', CcLogin);
