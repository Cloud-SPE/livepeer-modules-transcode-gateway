#!/usr/bin/env bash
# End-to-end smoke test for `make smoke`.
#
# Assumes `make dev` (or `make dev-livepeer`) is up: db + rustfs + bootstrap
# + gateway. Daemons-up state lets the /v1/* path actually dispatch; without
# them, /v1/abr and /v1/live return 503 (expected).

set -euo pipefail

GATEWAY="${GATEWAY:-http://localhost:4000}"
ADMIN_TOKEN="${ADMIN_TOKEN:-${SMOKE_ADMIN_TOKEN:-smoke-admin-token}}"
EMAIL="smoke+$(date +%s)@example.com"
NAME="Smoke Tester"

pass() { printf "  \033[32m✓\033[0m %s\n" "$1"; }
fail() { printf "  \033[31m✗\033[0m %s\n" "$1"; exit 1; }
section() { printf "\n\033[1m%s\033[0m\n" "$1"; }

require_status() {
  local expected="$1" actual="$2" what="$3"
  if [[ "$expected" != "$actual" ]]; then
    fail "$what — expected $expected, got $actual"
  fi
  pass "$what ($actual)"
}

# ── 0. health ─────────────────────────────────────────────────────
section "health"
status=$(curl -s -o /dev/null -w "%{http_code}" "$GATEWAY/health")
case "$status" in
  200|503) pass "GET /health ($status)" ;;
  *) fail "GET /health unexpected $status" ;;
esac

# ── 1. /v1/capabilities — registry-backed catalog (may be empty) ──
section "catalog"
status=$(curl -s -o /tmp/smoke-caps.json -w "%{http_code}" "$GATEWAY/v1/capabilities" -H "Authorization: Bearer smoke-no-auth-yet")
case "$status" in
  401|200|503) pass "GET /v1/capabilities responds ($status)" ;;
  *) fail "unexpected status $status" ;;
esac

# ── 2. signup ─────────────────────────────────────────────────────
section "signup → verify → approve"
status=$(curl -s -o /tmp/smoke-signup.json -w "%{http_code}" -X POST "$GATEWAY/api/waitlist" \
  -H "Content-Type: application/json" \
  -d "{\"name\":\"$NAME\",\"email\":\"$EMAIL\"}")
require_status 200 "$status" "POST /api/waitlist"

# Flip email_verified_at via DB (in real life: click the email link).
docker compose exec -T db \
  psql -U "${POSTGRES_USER:-video_gateway}" -d "${POSTGRES_DB:-video_gateway}" \
  -c "UPDATE waitlist SET email_verified_at=now() WHERE email='$EMAIL';" >/dev/null \
  || fail "db psql exec failed (compose db container not up?)"
pass "marked email_verified_at via db"

# Admin approve
wid=$(docker compose exec -T db \
  psql -U "${POSTGRES_USER:-video_gateway}" -d "${POSTGRES_DB:-video_gateway}" \
  -tAc "SELECT id FROM waitlist WHERE email='$EMAIL';" | tr -d '[:space:]')
[[ -n "$wid" ]] || fail "waitlist row id"
pass "waitlist row id ($wid)"

status=$(curl -s -o /tmp/smoke-approve.json -w "%{http_code}" \
  -X POST -H "X-Admin-Token: $ADMIN_TOKEN" "$GATEWAY/admin/waitlist/$wid/approve")
require_status 200 "$status" "POST /admin/waitlist/:id/approve"

# Pull the plaintext key out of the gateway logs (email is disabled in
# default compose; the key was logged with "would have sent").
key=$(docker compose logs --tail=200 gateway 2>/dev/null | grep -oE 'sk-[A-Za-z0-9_-]{40,}' | head -1 || true)
[[ -n "$key" ]] || fail "no plaintext API key found in gateway logs"
pass "extracted plaintext key (${key:0:11}…)"

# ── 3. portal login + account ────────────────────────────────────
section "portal cookie flow"
jar=$(mktemp)
trap 'rm -f $jar' EXIT
status=$(curl -s -c "$jar" -o /dev/null -w "%{http_code}" \
  -X POST "$GATEWAY/portal/login" \
  -H "Content-Type: application/json" \
  -d "{\"apiKey\":\"$key\"}")
require_status 200 "$status" "POST /portal/login"

acc=$(curl -fsS -b "$jar" "$GATEWAY/portal/account")
echo "$acc" | grep -q "$EMAIL" || fail "/portal/account email mismatch"
pass "GET /portal/account returns session user"

# ── 4. /v1/* bearer auth ─────────────────────────────────────────
section "/v1/* bearer auth"
status=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$GATEWAY/v1/abr" \
  -H "Content-Type: application/json" -d '{"input_url":"https://x"}')
require_status 401 "$status" "POST /v1/abr without auth → 401"

# With a valid key — accept 200 or 503 (daemons may be down in default dev)
status=$(curl -s -o /tmp/smoke-abr.json -w "%{http_code}" -X POST "$GATEWAY/v1/abr" \
  -H "Authorization: Bearer $key" \
  -H "Content-Type: application/json" -d '{"input_url":"https://example.com/sample.mp4"}')
case "$status" in
  200|502|503) pass "POST /v1/abr with valid key ($status)" ;;
  *) fail "unexpected status $status" ;;
esac

# ── 5. /v1/abr/upload-url ─────────────────────────────────────────
section "/v1/abr/upload-url (RustFS)"
status=$(curl -s -o /tmp/smoke-upload.json -w "%{http_code}" -X POST "$GATEWAY/v1/abr/upload-url" \
  -H "Authorization: Bearer $key" \
  -H "Content-Type: application/json" -d '{"filename":"smoke.mp4","content_type":"video/mp4"}')
case "$status" in
  200) pass "presigned upload URL minted" ;;
  503) pass "RustFS unavailable (smoke OK in non-rustfs envs)" ;;
  *) fail "unexpected $status" ;;
esac

# ── 6. metrics ──────────────────────────────────────────────────
section "/metrics"
status=$(curl -s -o /tmp/smoke-metrics.txt -w "%{http_code}" "$GATEWAY/metrics")
require_status 200 "$status" "GET /metrics"
grep -q "video_gateway_" /tmp/smoke-metrics.txt \
  || fail "/metrics missing video_gateway_* counters"
pass "/metrics exposes video_gateway_* counters"

# ── done ────────────────────────────────────────────────────────
section "smoke passed"
