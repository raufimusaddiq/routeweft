#!/usr/bin/env bash
# Browser smoke check for the Routeweft static UI shell (UI_STYLE.md §4-5, §13-15).
#
# Chromium is launched through playwright-core. Install it without touching the
# committed manifest, then point ROUTEWEFT_SMOKE_CHROME at a Chromium binary if
# the default playwright cache path is unavailable:
#
#   npm ci
#   npm install --no-save --no-package-lock playwright-core@1.49.1
#   npm run smoke
set -euo pipefail
cd "$(dirname "$0")/.."
npm run build
npx vite preview --host 127.0.0.1 --port 4173 >/tmp/routeweft-ui-preview.log 2>&1 &
preview_pid=$!
trap 'kill "$preview_pid" 2>/dev/null || true' EXIT
for _ in $(seq 1 60); do
  if curl -fsS http://127.0.0.1:4173 >/dev/null; then break; fi
  sleep 0.5
done
node scripts/shell-smoke.cjs

