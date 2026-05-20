import { LitElement, html } from 'lit';
import { api } from '/lib/api.js';

class CcSignupForm extends LitElement {
  static properties = {
    state: { state: true },
    message: { state: true },
  };

  constructor() {
    super();
    this.state = 'idle';
    this.message = '';
  }

  createRenderRoot() { return this; }

  render() {
    return html`
      <form @submit=${this.#onSubmit}>
        <input name="name" type="text" placeholder="Your name" required minlength="1" maxlength="200" autocomplete="name">
        <input name="email" type="email" placeholder="you@example.com" required autocomplete="email">
        <button type="submit" ?disabled=${this.state === 'submitting'}>
          ${this.state === 'submitting' ? 'Sending…' : 'Join waitlist'}
        </button>
        <p class="msg ${this.state}">${this.message}</p>
      </form>
    `;
  }

  async #onSubmit(ev) {
    ev.preventDefault();
    const form = ev.currentTarget;
    const fd = new FormData(form);
    this.state = 'submitting';
    this.message = '';
    try {
      await api('/api/waitlist', {
        method: 'POST',
        body: {
          name: String(fd.get('name')),
          email: String(fd.get('email')),
        },
      });
      this.state = 'ok';
      this.message = 'Thanks — check your inbox for a verification link.';
      form.reset();
    } catch (err) {
      this.state = 'error';
      this.message = err.message;
    }
  }
}

customElements.define('cc-signup-form', CcSignupForm);
