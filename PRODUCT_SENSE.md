# PRODUCT_SENSE

Product principles that guide tradeoffs in this repo. When code, design,
or scope decisions are ambiguous, fall back to these.

## What we are

A **transcode-focused entry point to the Livepeer network**. Developers
who want VOD ABR ladder transcoding or RTMP→HLS live streaming get a
small HTTP API + an S3-compatible ingest store, and the work runs on
Livepeer orchestrators.

## What we are not

- A general video platform. We expose ABR ladder transcoding + live
  RTMP→HLS only in v1. No clipping, no thumbnails-as-product, no
  catch-up VOD recording (yet).
- A media CDN. We don't host playback — we hand back broker-issued
  HLS URLs.
- A billing product. Beta is free; pricing is a separate concern.
- A Livepeer Studio clone. Distinct API surface (`/v1/abr`, `/v1/live`)
  by design.

## Principles

1. **Boring API shapes.** `POST /v1/abr`, `POST /v1/live`. Predictable
   bodies, predictable responses. If a Livepeer-savvy developer can't
   read the OpenAPI spec and guess how to use it, that's a bug.
2. **No surprise behavior.** A request that succeeds should commit a
   reservation row + (for live) a `live_streams` row. A failure should
   refund and never leave half-state.
3. **Zero per-request friction for users.** No rate-limit headers, no
   quota messages. Friction lives at signup (waitlist + admin
   approval), not at request time.
4. **Free during beta means truly free.** No "free tier with limits."
   Limits are a billing concern; billing isn't here yet.
5. **Capabilities reflect reality.** `/v1/capabilities` shows what the
   on-chain registry advertises right now. If a capability disappears,
   the API reflects that within one refresh cycle.
6. **Media bytes don't traverse Go.** VOD ingest goes client → RustFS;
   live ingest goes client → broker. The gateway signs URLs and
   tracks reservations — it never proxies bytes.
7. **The portal is a courtesy, not a product.** It exists so users can
   manage API keys, see usage, and exercise the API via the playground.
   Anything beyond that belongs out of the portal.
8. **Admin is a tool, not a product.** Admin keeps the beta running.
   It's not for end-users and doesn't ship polish features.
9. **The marketing site is generic.** It says "Livepeer Video Gateway"
   and signs you up. Rebrand by editing one HTML + one CSS file.

## When in doubt

- **Choose simplicity over completeness.** Ship the path that works
  for 90% of cases; document the 10% as known limitations.
- **Choose deletion over feature flags.** If something doesn't fit v1,
  remove it cleanly. Add it back when we get to v2.
- **Choose live-poll over push.** No SSE, no webhooks in v1 — clients
  poll `GET /v1/live/:id`. Push channels are a v2 concern.
