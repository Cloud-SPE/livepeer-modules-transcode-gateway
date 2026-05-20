# FRONTEND

DOM, CSS, and build conventions for the three SPAs under `web/`.

## Zero-build, importmaps, Lit

All three SPAs (`web/site/`, `web/portal/`, `web/admin/`) are
zero-build: no Vite, no esbuild, no bundler. Dependencies load via
`<script type="importmap">` from [esm.sh](https://esm.sh/).

Why: agents can edit and reload without a build step. The runtime is
the source. A small `dev-server.js` per SPA serves static files and
proxies `/api/*`, `/portal/*`, `/admin/*`, and `/v1/*` to the gateway.

## DOM invariants

Hard rules, enforced by reviewer attention (lint coming later):

1. **Light DOM only.** No `attachShadow()`, no `<slot>`, no shadow trees.
   Lit components use `createRenderRoot() { return this; }`.
2. **Semantic HTML only.** `<button>` not `<div role="button">`.
   `<form>` for submissions. `<nav>`, `<main>`, `<header>`, `<footer>`.
3. **No inline CSS.** No `style="…"`. No `<style>` blocks in render.
4. **No inline event handlers.** Use Lit's `@event` binding.

## CSS conventions

- Styles in checked-in `.css` files, one per SPA: `site/index.css`,
  `portal/portal.css`, `admin/admin.css`.
- Use [CSS `@layer`](https://developer.mozilla.org/en-US/docs/Web/CSS/@layer)
  for cascading control. Order: `reset, base, components, utilities, overrides`.
- Custom properties (`--token-name`) for themeable values. Single
  `:root { … }` block per SPA.
- No CSS frameworks. No CSS-in-JS.

## Component shape

```js
// web/portal/components/cc-api-key-row.js
import { LitElement, html } from 'lit';

export class CcApiKeyRow extends LitElement {
  static properties = { apiKey: { type: Object } };
  createRenderRoot() { return this; }
  render() {
    return html`
      <article class="api-key-row">
        <h3>${this.apiKey.label}</h3>
        <button @click=${this.#onRevoke}>Revoke</button>
      </article>
    `;
  }
  #onRevoke() {
    this.dispatchEvent(new CustomEvent('revoke', { detail: this.apiKey.id }));
  }
}
customElements.define('cc-api-key-row', CcApiKeyRow);
```

Component file naming: kebab-case prefix `cc-` (component cluster).

## Routing

History-API or hash-based; no React Router, no framework router. Each
SPA picks one and stays consistent.

## Branding

The marketing site (`web/site/`) shows "Livepeer Video Gateway" as the
product name. Rebrand by editing `site/index.html` + `site/index.css`
— no build-time templating.

## Playground

The portal's playground (`web/portal/components/cc-playground.js`)
exposes **two tabs**:

- **Live** — POST `/v1/live` → render `rtmp://…` + stream key + OBS
  hint → embed `<video>` playing the returned HLS URL via `hls.js`
  loaded from esm.sh. Stop/delete control wired to `DELETE /v1/live/:id`.
- **Transcode** — drag/drop file → POST `/v1/abr/upload-url` →
  PUT bytes to RustFS → POST `/v1/abr` → poll job → play master
  playlist when ready, with a rendition selector.

Both tabs use the user's API key from the cookie session indirectly
(the portal calls `/v1/*` with the bearer key the user pasted at
login).

## Accessibility

Every form input has a `<label>`. Every interactive element is
keyboard-operable. Color contrast WCAG AA. No CSS that disables focus
outlines without an alternative.

## What we accept

- Slightly more verbose markup than a framework-driven SPA.
- Slower initial cold load than a bundled app (esm.sh is CDN-backed).
- No SSR. SPAs are CSR-only.
- No router library — hand-roll per SPA.

## What we do NOT accept

- Heavy frameworks (React, Vue, Angular, Svelte).
- Build steps that produce bundles in source control.
- Inline styling.
- Shadow DOM.
