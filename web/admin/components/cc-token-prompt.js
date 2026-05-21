import { LitElement, html } from 'lit';
import { setToken } from '../lib/api.js';

class CcTokenPrompt extends LitElement {
  createRenderRoot() { return this; }

  render() {
    return html`
      <main>
        <div class="card centered">
          <h2>Admin token</h2>
          <p class="msg">
            Enter the <code>X-Admin-Token</code> for this gateway. It's stored
            in localStorage — clearing it signs you out.
          </p>
          <form class="stack-sm" @submit=${this.#onSubmit}>
            <input name="token" type="password" placeholder="admin token" required>
            <button class="primary" type="submit">Save</button>
          </form>
        </div>
      </main>
    `;
  }

  #onSubmit = (ev) => {
    ev.preventDefault();
    const fd = new FormData(ev.currentTarget);
    setToken(String(fd.get('token') || '').trim());
    window.dispatchEvent(new Event('cc-token'));
  };
}

customElements.define('cc-token-prompt', CcTokenPrompt);
