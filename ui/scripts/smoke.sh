#!/usr/bin/env bash
# Browser smoke check for the Routeweft static UI shell (UI_STYLE.md §4-5, §13-15).
#
# Chromium is launched through playwright-core. Install it without touching the
# committed manifest, then point ROUTEWEFT_SMOKE_CHROME at a Chromium binary if
# the default playwright cache path is unavailable:
#
#   npm ci
#   npm install --no-save --no-package-lock playwright-core@1.49.1
#   npx playwright-core install firefox   # import masking runs in Firefox too
#   npm run smoke
#
# scripts/smoke-cleanup-check.sh verifies that every exit path stops the preview.
set -euo pipefail
cd "$(dirname "$0")/.."
npm run build
# The preview runs in its own process group (set -m) so that cleanup can stop
# npx and the vite child it spawns together, by group ID, on every exit path.
preview_pid=
stop_preview() {
  [ -n "$preview_pid" ] || return 0
  kill -TERM -- "-$preview_pid" 2>/dev/null || return 0
  for _ in $(seq 1 20); do
    kill -0 -- "-$preview_pid" 2>/dev/null || break
    sleep 0.25
  done
  kill -KILL -- "-$preview_pid" 2>/dev/null || true
  wait "$preview_pid" 2>/dev/null || true
}
trap stop_preview EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
set -m
npx vite preview --host 127.0.0.1 --port 4173 >/tmp/routeweft-ui-preview.log 2>&1 &
preview_pid=$!
set +m
for _ in $(seq 1 60); do
  if curl -fsS http://127.0.0.1:4173 >/dev/null; then break; fi
  sleep 0.5
done
node scripts/shell-smoke.cjs
node scripts/oauth-journeys-smoke.cjs
node scripts/device-approval-smoke.cjs
node scripts/import-masking-smoke.cjs
node scripts/connection-status-smoke.cjs
node scripts/endpoint-settings-smoke.cjs
node scripts/kiro-social-smoke.cjs
node scripts/quota-actions-smoke.cjs
node scripts/quota-warming-smoke.cjs
node scripts/token-saver-panels-smoke.cjs
node scripts/usage-page-smoke.cjs
node scripts/systemone-combos-smoke.cjs
node scripts/phase3a-pages-smoke.cjs
node scripts/phase3b-pages-smoke.cjs
node scripts/landing-smoke.cjs
node scripts/phase3ca-pages-smoke.cjs
node scripts/phase3cb-pages-smoke.cjs
node scripts/phase4-figures-smoke.cjs
node scripts/a11y-keyboard-labels-smoke.cjs
node scripts/live-events-smoke.cjs
node scripts/locale-smoke.cjs

