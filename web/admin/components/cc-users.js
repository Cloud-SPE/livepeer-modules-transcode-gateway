import { LitElement, html } from 'lit';
import { api } from '/lib/api.js';

// Users list + per-user drilldown. Click "Detail" to load the user's
// API keys + aggregate usage from /admin/users/{id}.

class CcUsers extends LitElement {
  static properties = {
    rows:       { state: true },
    selectedId: { state: true },
    selected:   { state: true },
    error:      { state: true },
  };

  constructor() {
    super();
    this.rows = [];
    this.selectedId = '';
    this.selected = null;
    this.error = '';
  }

  createRenderRoot() { return this; }

  async connectedCallback() {
    super.connectedCallback();
    await this.#load();
  }

  async #load() {
    try {
      const data = await api('/admin/users?limit=200');
      this.rows = data?.items ?? [];
    } catch (err) {
      this.error = err.message;
    }
  }

  async #select(id) {
    this.selectedId = id;
    this.selected = null;
    try {
      this.selected = await api(`/admin/users/${id}`);
    } catch (err) {
      this.error = err.message;
    }
  }

  render() {
    return html`
      <div class="card">
        <h2>Approved users</h2>
        <p class="msg">
          Click "Detail" to see a user's API keys (active/revoked, last used)
          and their aggregate request counts. The Usage tab shows the same
          totals broken out per API key.
        </p>
        ${this.error ? html`<p class="msg error">${this.error}</p>` : ''}
        ${this.rows.length === 0
          ? html`<p class="msg">No approved users yet.</p>`
          : html`<table>
              <thead><tr><th>Email</th><th>Name</th><th>Approved</th><th></th></tr></thead>
              <tbody>
                ${this.rows.map(
                  (u) => html`<tr>
                    <td>${u.email}</td>
                    <td>${u.name}</td>
                    <td>${u.approved_at ? new Date(u.approved_at).toLocaleDateString() : '—'}</td>
                    <td><button class="ghost" @click=${() => this.#select(u.id)}>Detail</button></td>
                  </tr>`,
                )}
              </tbody>
            </table>`}
      </div>
      ${this.selected ? this.#renderDetail() : ''}
    `;
  }

  #renderDetail() {
    const u = this.selected;
    return html`
      <div class="card">
        <h2>${u.name} <span class="msg">(${u.email})</span></h2>
        <p class="msg">
          Status: ${u.status}
          · Verified ${u.email_verified_at ? '✓' : '✗'}
          · Joined ${u.created_at ? new Date(u.created_at).toLocaleDateString() : '—'}
        </p>

        <h3>Usage</h3>
        <p class="msg">
          Total ${u.usage.total_requests}
          · committed ${u.usage.committed_total}
          · refunded ${u.usage.refunded_total}
          · open ${u.usage.open_total}
          ${u.usage.last_used_at ? html` · last used ${new Date(u.usage.last_used_at).toLocaleString()}` : ''}
        </p>

        <h3>API keys (${u.api_keys.length})</h3>
        ${u.api_keys.length === 0
          ? html`<p class="msg">No keys.</p>`
          : html`<table>
              <thead><tr><th>Label</th><th>Prefix</th><th>Created</th><th>Last used</th><th>Status</th></tr></thead>
              <tbody>
                ${u.api_keys.map(
                  (k) => html`<tr>
                    <td>${k.label || html`<span class="msg">(unnamed)</span>`}</td>
                    <td><code>${k.key_prefix}…</code></td>
                    <td>${new Date(k.created_at).toLocaleDateString()}</td>
                    <td>${k.last_used_at ? new Date(k.last_used_at).toLocaleString() : html`<span class="msg">—</span>`}</td>
                    <td>${k.revoked_at
                      ? html`<span class="pill warn">revoked</span>`
                      : html`<span class="pill ok">active</span>`}</td>
                  </tr>`,
                )}
              </tbody>
            </table>`}
      </div>
    `;
  }
}

customElements.define('cc-users', CcUsers);
