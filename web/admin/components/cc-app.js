import { LitElement, html } from 'lit';
import { getToken, api } from '../lib/api.js';

import './cc-token-prompt.js';
import './cc-waitlist-queue.js';
import './cc-users.js';
import './cc-usage.js';
import './cc-live-streams.js';
import './cc-abr-jobs.js';
import './cc-network-health.js';
import './cc-registry.js';

const ROUTES = [
  'waitlist',
  'users',
  'usage',
  'live-streams',
  'abr-jobs',
  'health',
  'registry',
];

class CcApp extends LitElement {
  static properties = {
    route:     { state: true },
    hasToken:  { state: true },
    tokenError:{ state: true },
    probing:   { state: true },
  };

  constructor() {
    super();
    this.route      = readRoute();
    this.hasToken   = !!getToken();
    this.tokenError = '';
    this.probing    = false;
  }

  createRenderRoot() { return this; }

  connectedCallback() {
    super.connectedCallback();
    window.addEventListener('hashchange', this.#onHash);
    window.addEventListener('cc-token', this.#onToken);
    if (this.hasToken) void this.#probeToken();
  }
  disconnectedCallback() {
    super.disconnectedCallback();
    window.removeEventListener('hashchange', this.#onHash);
    window.removeEventListener('cc-token', this.#onToken);
  }

  #onHash  = () => { this.route = readRoute(); };
  #onToken = () => {
    this.hasToken = !!getToken();
    if (this.hasToken) void this.#probeToken();
  };

  // Validate the token before letting the rest of the panels load —
  // matches the openai gateway's behavior. A 401 here means a bad
  // token; we drop back to the prompt instead of letting every panel
  // fail individually.
  async #probeToken() {
    this.probing = true;
    this.tokenError = '';
    try {
      await api('/admin/waitlist?limit=1');
    } catch (err) {
      if (err.status === 401 || err.status === 503) {
        this.tokenError = err.message || 'admin token rejected';
        this.hasToken = false;
      }
    } finally {
      this.probing = false;
    }
  }

  render() {
    if (this.probing) return html`<p class="msg">Checking admin access…</p>`;
    if (!this.hasToken) {
      return html`
        <cc-token-prompt></cc-token-prompt>
        ${this.tokenError
          ? html`<p class="msg error" style="text-align:center">${this.tokenError}</p>`
          : ''}
      `;
    }
    return html`
      <div class="shell">
        <header class="topbar">
          <h1>Livepeer Video Gateway — Admin</h1>
          <nav>
            ${ROUTES.map(
              (r) => html`<a href="#/${r}" class=${r === this.route ? 'active' : ''}>${pretty(r)}</a>`,
            )}
          </nav>
        </header>
        <main>
          ${this.route === 'waitlist'     ? html`<cc-waitlist-queue></cc-waitlist-queue>`
          : this.route === 'users'        ? html`<cc-users></cc-users>`
          : this.route === 'usage'        ? html`<cc-usage></cc-usage>`
          : this.route === 'live-streams' ? html`<cc-live-streams></cc-live-streams>`
          : this.route === 'abr-jobs'     ? html`<cc-abr-jobs></cc-abr-jobs>`
          : this.route === 'health'       ? html`<cc-network-health></cc-network-health>`
          :                                 html`<cc-registry></cc-registry>`}
        </main>
      </div>
    `;
  }
}

function readRoute() {
  const h = window.location.hash.replace(/^#\//, '');
  return ROUTES.includes(h) ? h : 'waitlist';
}
function pretty(r) {
  if (r === 'live-streams') return 'Live streams';
  if (r === 'abr-jobs')     return 'ABR jobs';
  if (r === 'health')       return 'Health';
  if (r === 'registry')     return 'Registry';
  return r[0].toUpperCase() + r.slice(1);
}

customElements.define('cc-app', CcApp);
