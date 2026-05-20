import { LitElement, html } from 'lit';
import { api } from '/lib/api.js';

class CcVerifyCard extends LitElement {
  static properties = {
    state: { state: true },
    message: { state: true },
  };

  constructor() {
    super();
    this.state = 'pending';
    this.message = '';
  }

  createRenderRoot() { return this; }

  connectedCallback() {
    super.connectedCallback();
    void this.#verify();
  }

  async #verify() {
    const params = new URLSearchParams(window.location.search);
    const token = params.get('token');
    if (!token) {
      this.state = 'error';
      this.message = 'No verification token in the URL.';
      return;
    }
    try {
      const data = await api(`/api/verify?token=${encodeURIComponent(token)}`);
      this.state = 'ok';
      this.message = data?.message ?? 'Email verified.';
    } catch (err) {
      this.state = 'error';
      this.message = err.message;
    }
  }

  render() {
    return html`
      <div class="card">
        ${this.state === 'pending'
          ? html`<p>Working…</p>`
          : this.state === 'ok'
          ? html`<p class="ok">${this.message}</p>
                 <p>Watch your inbox for a follow-up once an admin approves your signup.</p>`
          : html`<p class="err">${this.message}</p>
                 <p>The link may be expired or already used. <a href="/">Sign up again</a>.</p>`}
      </div>
    `;
  }
}

customElements.define('cc-verify-card', CcVerifyCard);
