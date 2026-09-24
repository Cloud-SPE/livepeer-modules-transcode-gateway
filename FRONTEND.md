# FRONTEND

DOM, CSS, and build conventions for the three SPAs under `web/`.

## Zero-build, importmaps, Lit

All three SPAs (`web/site/`, `web/portal/`, `web/admin/`) are
zero-build: no Vite, no esbuild, no bundler. Dependencies load via
`<script type="importmap">` from [esm.sh](https://esm.sh/).

Why: agents can edit and reload without a build step. The runtime is
the source.

## Production vs dev serving

- **Production.** The three SPAs are embedded into the gateway binary
  via `//go:embed`. `make embed-webroot` (also run by the Dockerfile)
  copies `web/{site,portal,admin}` into
  `gateway/internal/server/webroot/`. The gateway serves `/` (site),
  `/portal/` (portal), `/admin/` (admin) from that embedded FS, plus
  the API at `/api/*`, on a single port (default `4000`).
  Bare `/portal` and `/admin` 301 to the trailing-slash form.
- **Dev.** `make web` starts a per-SPA `dev-server.js` so each SPA
  hot-reloads on its own port (3000/3001/3002). Each dev server
  proxies `/api/*` to the gateway at `:4000` so the SPAs see the same
  API surface in both modes.

Asset paths inside SPA component imports use `../lib/api.js` (relative)
so they resolve correctly against the SPA mount point in both modes.

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

- **Live** — POST `/api/v1/live` → render `rtmp://…` + stream key + OBS
  hint → embed `<video>` playing the returned HLS URL via `hls.js`
  loaded from esm.sh. Stop/delete control wired to
  `DELETE /api/v1/live/:id`.
- **Transcode** — drag/drop file → POST `/api/v1/abr/upload-url` →
  PUT bytes to MinIO → POST `/api/v1/abr` → poll job → play master
  playlist when ready. Succeeded rows expose **Copy URL** (master
  playlist) and a `▸ N variants` toggle that expands per-rendition
  rows with **Play this one** + **Copy URL** per variant. Failed rows
  render an inline sub-row with API `error_code` and `error` details.
  Admission rejection displays “Settlement pending” with its explanation,
  continues polling, and offers Retry only after terminal failure. The API
  supplies bounded workflow messages rather than raw upstream errors.

Both tabs use the user's API key from the cookie session indirectly
(the portal calls `/api/v1/*` with the bearer key the user pasted at
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

Live session controls are restored from the authenticated portal API after a
refresh. `/api/portal/live-streams/{id}` returns owner-scoped session details
with `Cache-Control: no-store`; only the selected session ID is kept in
sessionStorage, never ingest credentials. Live streams lists Play, Stop and
Manage together, polls server state, and keeps Stop disabled while termination
is pending. Browser disconnects do not stop the runner. See beads `vgw-2ac`
and `vgw-91i` for the implementation and runner lifecycle coordination.

Portal component behavior tests run with `pnpm --filter
@livepeer-modules-transcode-gateway/portal test` (or `node
--experimental-vm-modules --test web/portal/tests/*.test.mjs` from the root).
They exercise component API interactions using a small Lit template stub;
actual browser playback additionally requires the deployed HTTPS HLS edge.
