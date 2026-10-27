#!/usr/bin/env bash
# Checks that scripts/smoke.sh stops the vite preview it started (issue #238).
#
# Runs the smoke once per exit path and fails if any process of that run's
# preview survives or port 4173 is still held afterwards:
#
#   pass       the full smoke (needs Chromium, see smoke.sh)
#   fail       the smoke with a Chromium path that does not exist
#   interrupt  SIGINT to the smoke's process group once the preview listens
#              (what Ctrl-C in a terminal sends)
#   term       SIGTERM to the smoke shell alone (what a timeout or kill sends)
#
#   bash scripts/smoke-cleanup-check.sh [pass] [fail] [interrupt] [term]
#
# With no arguments all four run. The preview is identified by the PID that
# listens on the port, and its process group, not by name.
set -uo pipefail
cd "$(dirname "$0")/.."
port=4173

listener_pid() {
  ss -Hltnp "sport = :$port" 2>/dev/null | grep -o 'pid=[0-9]*' | head -n1 | cut -d= -f2
}

if [ -n "$(ss -Hltn "sport = :$port")" ]; then
  echo "port $port is already in use; stop that server first" >&2
  exit 2
fi

check() {
  local mode=$1 smoke_pid preview_pid='' preview_pgid='' status survivors
  set -m
  case $mode in
    fail) ROUTEWEFT_SMOKE_CHROME=/nonexistent/chrome bash scripts/smoke.sh >"/tmp/routeweft-smoke-$mode.log" 2>&1 & ;;
    *) bash scripts/smoke.sh >"/tmp/routeweft-smoke-$mode.log" 2>&1 & ;;
  esac
  smoke_pid=$!
  set +m
  while kill -0 "$smoke_pid" 2>/dev/null; do
    preview_pid=$(listener_pid)
    [ -n "$preview_pid" ] && break
    sleep 0.2
  done
  if [ -z "$preview_pid" ]; then
    wait "$smoke_pid"
    echo "$mode: smoke exited before the preview listened; see /tmp/routeweft-smoke-$mode.log" >&2
    return 1
  fi
  preview_pgid=$(ps -o pgid= -p "$preview_pid" | tr -d ' ')
  case $mode in
    interrupt) sleep 1; kill -INT -- "-$smoke_pid" ;;
    term) sleep 1; kill -TERM "$smoke_pid" ;;
  esac
  wait "$smoke_pid"
  status=$?
  survivors=$(ps -eo pid=,pgid=,args= | awk -v pid="$preview_pid" -v pgid="$preview_pgid" '$1 == pid || $2 == pgid')
  if [ -n "$survivors" ] || [ -n "$(ss -Hltn "sport = :$port")" ]; then
    echo "$mode: FAIL smoke exit=$status, preview pid=$preview_pid pgid=$preview_pgid left behind:" >&2
    [ -n "$survivors" ] && echo "$survivors" >&2
    ss -Hltnp "sport = :$port" >&2
    # Stop what this run leaked, by PID and group, so the host stays clean.
    kill -- "-$preview_pgid" "$preview_pid" 2>/dev/null
    return 1
  fi
  echo "$mode: ok smoke exit=$status, preview pid=$preview_pid pgid=$preview_pgid gone, port $port free"
  case $mode in
    pass) [ "$status" -eq 0 ] || { echo "pass: smoke itself failed; see /tmp/routeweft-smoke-$mode.log" >&2; return 1; } ;;
    *) [ "$status" -ne 0 ] || { echo "$mode: smoke exited 0 on a path that should fail" >&2; return 1; } ;;
  esac
}

modes=("$@")
[ ${#modes[@]} -gt 0 ] || modes=(pass fail interrupt term)
failed=0
for mode in "${modes[@]}"; do
  check "$mode" || failed=1
done
exit "$failed"
