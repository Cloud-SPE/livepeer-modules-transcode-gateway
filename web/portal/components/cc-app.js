import { LitElement, html } from 'lit';
import { api } from '/lib/api.js';

import './cc-login.js';
import './cc-account.js';
import './cc-api-keys.js';
import './cc-network-health.js';
import './cc-playground.js';
import './cc-live-streams.js';
import './cc-abr-jobs.js';
import './cc-usage.js';

const ROUTES = [
  'account',
  'api-keys',
  'playground',
  'live-streams',
  'abr-jobs',
  'usage',
  'health',
];

class CcApp extends LitElement {
  static properties = {
    route: { state: true },
    session: { state: true },
    checking: { state: true },
  };

  constructor() {
    super();
    this.route = readRoute();
    this.session = null;
    this.checking = true;
  }

  createRenderRoot() { return this; }

  async connectedCallback() {
    super.connectedCallback();
    window.addEventListener('hashchange', this.#onHash);
    window.addEventListener('cc-login', this.#onLogin);
    window.addEventListener('cc-logout', this.#onLogout);
    await this.#refreshSession();
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    window.removeEventListener('hashchange', this.#onHash);
    window.removeEventListener('cc-login', this.#onLogin);
    window.removeEventListener('cc-logout', this.#onLogout);
  }

  #onHash = () => { this.route = readRoute(); };
  #onLogin = () => { void this.#refreshSession(); };
  #onLogout = async () => {
    try { await api('/portal/logout', { method: 'POST' }); } catch {}
    sessionStorage.removeItem('lvp_video_api_key');
    window.__lvpApiKey = '';
    this.session = null;
    location.hash = '#/account';
  };

  async #refreshSession() {
    this.checking = true;
    try {
      this.session = await api('/portal/account');
    } catch {
      this.session = null;
    } finally {
      this.checking = false;
    }
  }

  render() {
    if (this.checking) return html`<p class="msg">Loading…</p>`;
    if (!this.session) return html`<cc-login></cc-login>`;
    return html`
      <div class="shell">
        <header class="topbar">
          <h1>Livepeer Video Gateway — Portal</h1>
          <nav>
            ${ROUTES.map(
              (r) => html`<a href="#/${r}" class=${r === this.route ? 'active' : ''}>${pretty(r)}</a>`,
            )}
          </nav>
          <span class="who">${this.session.email}
            <button class="ghost" @click=${() => window.dispatchEvent(new Event('cc-logout'))}>Sign out</button>
          </span>
        </header>
        <main>
          ${this.route === 'account'      ? html`<cc-account .session=${this.session}></cc-account>`
          : this.route === 'api-keys'     ? html`<cc-api-keys></cc-api-keys>`
          : this.route === 'playground'   ? html`<cc-playground></cc-playground>`
          : this.route === 'live-streams' ? html`<cc-live-streams></cc-live-streams>`
          : this.route === 'abr-jobs'     ? html`<cc-abr-jobs></cc-abr-jobs>`
          : this.route === 'health'       ? html`<cc-network-health></cc-network-health>`
          :                                 html`<cc-usage></cc-usage>`}
        </main>
      </div>
    `;
  }
}

function readRoute() {
  const h = window.location.hash.replace(/^#\//, '');
  return ROUTES.includes(h) ? h : 'account';
}

function pretty(r) {
  if (r === 'api-keys')     return 'API keys';
  if (r === 'live-streams') return 'Live streams';
  if (r === 'abr-jobs')     return 'ABR jobs';
  if (r === 'health')       return 'Health';
  if (r === 'playground')   return 'Playground';
  return r[0].toUpperCase() + r.slice(1);
}

customElements.define('cc-app', CcApp);
