import { LitElement, html } from 'lit';
import { api } from '../lib/api.js';

class CcWaitlistQueue extends LitElement {
  static properties = {
    items:       { state: true },
    filter:      { state: true },
    error:       { state: true },
    busyID:      { state: true },
    approvedKey: { state: true },
  };

  constructor() {
    super();
    this.items = [];
    this.filter = 'pending';
    this.error = '';
    this.busyID = '';
    this.approvedKey = null;
  }

  createRenderRoot() { return this; }

  async connectedCallback() {
    super.connectedCallback();
    await this.#load();
  }

  async #load() {
    try {
      const data = await api(`/admin/waitlist?status=${this.filter}`);
      this.items = data?.items ?? [];
    } catch (err) {
      this.error = err.message;
    }
  }

  async #approve(id) {
    this.busyID = id;
    this.error = '';
    this.approvedKey = null;
    try {
      const out = await api(`/admin/waitlist/${id}/approve`, { method: 'POST' });
      this.approvedKey = out?.plain_key
        ? { id, key: out.plain_key, prefix: out.key_prefix ?? '' }
        : null;
      await this.#load();
    } catch (err) {
      this.error = err.message;
    } finally {
      this.busyID = '';
    }
  }

  async #reject(id) {
    if (!confirm('Reject this signup?')) return;
    try {
      await api(`/admin/waitlist/${id}/reject`, { method: 'POST' });
      await this.#load();
    } catch (err) {
      this.error = err.message;
    }
  }

  async #resend(id) {
    try {
      await api(`/admin/waitlist/${id}/resend-verification`, { method: 'POST' });
    } catch (err) {
      this.error = err.message;
    }
  }

  render() {
    return html`
      <div class="card">
        <h2>Waitlist</h2>
        <p class="msg">Filter and act on signups.</p>
        ${this.approvedKey
          ? html`<p class="msg">
              Approved. One-time API key:
              <code>${this.approvedKey.key}</code>
              ${this.approvedKey.prefix ? html`<span>(${this.approvedKey.prefix}…)</span>` : ''}
            </p>`
          : ''}
        <select @change=${(e) => { this.filter = e.target.value; void this.#load(); }}>
          <option value="pending">Pending</option>
          <option value="approved">Approved</option>
          <option value="rejected">Rejected</option>
          <option value="all">All</option>
        </select>
        ${this.error ? html`<p class="msg error">${this.error}</p>` : ''}
        ${this.items.length === 0
          ? html`<p class="msg">Nothing here.</p>`
          : html`<table>
              <thead>
                <tr><th>Email</th><th>Name</th><th>Status</th><th>Verified</th><th>Created</th><th></th></tr>
              </thead>
              <tbody>
                ${this.items.map(
                  (r) => html`<tr>
                    <td>${r.email}</td>
                    <td>${r.name}</td>
                    <td><span class="pill ${r.status === 'approved' ? 'ok' : r.status === 'rejected' ? 'warn' : ''}">${r.status}</span></td>
                    <td>${r.email_verified_at ? '✓' : '—'}</td>
                    <td>${new Date(r.created_at).toLocaleString()}</td>
                    <td>
                      ${r.status === 'pending'
                        ? html`
                          <button class="primary" ?disabled=${this.busyID === r.id}
                                  @click=${() => this.#approve(r.id)}>Approve</button>
                          <button class="ghost danger" @click=${() => this.#reject(r.id)}>Reject</button>
                          <button class="ghost" @click=${() => this.#resend(r.id)}>Resend</button>`
                        : ''}
                    </td>
                  </tr>`,
                )}
              </tbody>
            </table>`}
      </div>
    `;
  }
}

customElements.define('cc-waitlist-queue', CcWaitlistQueue);
