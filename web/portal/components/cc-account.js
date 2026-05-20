import { LitElement, html } from 'lit';

class CcAccount extends LitElement {
  static properties = {
    session: { type: Object },
  };

  createRenderRoot() { return this; }

  render() {
    const s = this.session ?? {};
    return html`
      <div class="card">
        <h2>Account</h2>
        <p class="msg">
          The approved account associated with your active portal session.
          API access is controlled by keys, not by username/password.
        </p>
        <p><strong>Email:</strong> ${s.email}</p>
        <p><strong>Name:</strong> ${s.name}</p>
        <p class="msg">
          Manage API access in the <a href="#/api-keys">API keys</a> tab, check
          network availability in <a href="#/health">Health</a>, and exercise
          the API in <a href="#/playground">Playground</a>.
        </p>
      </div>
    `;
  }
}

customElements.define('cc-account', CcAccount);
