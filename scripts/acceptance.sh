#!/usr/bin/env bash
# Phase 0 acceptance check.
#
# Verifies, without the desktop shell:
#   1. `lanbazd` starts and prints its banner
#   2. the control API answers on loopback
#   3. `lanbazctl daemon status` returns a valid payload
#   4. `lanbazctl daemon shutdown` stops the daemon gracefully
#
# Run from the repository root: ./scripts/acceptance.sh

set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GO_BIN="${GO_BIN:-go}"
WORK="$(mktemp -d)"
STATE="$WORK/state"
DAEMON_PID=""

cleanup() {
  if [[ -n "$DAEMON_PID" ]] && kill -0 "$DAEMON_PID" 2>/dev/null; then
    kill "$DAEMON_PID" 2>/dev/null || true
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

fail() {
  echo "FAIL: $1"
  exit 1
}

step() { echo; echo "== $1"; }

step "building"
mkdir -p "$WORK/bin"
( cd "$ROOT" && "$GO_BIN" build -o "$WORK/bin/lanbazd" ./core/cmd/lanbazd ) || fail "go build lanbazd"
( cd "$ROOT" && "$GO_BIN" build -o "$WORK/bin/lanbazctl" ./core/cmd/lanbazctl ) || fail "go build lanbazctl"

step "starting lanbazd"
"$WORK/bin/lanbazd" --state-dir "$STATE" > "$WORK/daemon.out" 2> "$WORK/daemon.err" &
DAEMON_PID=$!

for _ in $(seq 1 50); do
  [[ -f "$STATE/daemon.json" ]] && break
  sleep 0.1
done
[[ -f "$STATE/daemon.json" ]] || fail "the daemon did not write its state file"

if grep -q "LanBaz daemon started" "$WORK/daemon.out"; then
  echo "ok: startup banner"
else
  cat "$WORK/daemon.out"
  fail "the startup banner is missing"
fi

if grep -qE "API listening on 127\.0\.0\.1:[0-9]+/api" "$WORK/daemon.out"; then
  echo "ok: API listening banner"
else
  cat "$WORK/daemon.out"
  fail "the API listening banner is missing"
fi

PORT="$(sed -n 's/.*"api_port": *\([0-9]*\).*/\1/p' "$STATE/daemon.json")"
[[ -n "$PORT" ]] || fail "the state file has no api_port"

step "checking the health endpoint"
if command -v curl >/dev/null 2>&1; then
  curl -fsS "http://127.0.0.1:$PORT/healthz" >/dev/null || fail "health endpoint did not answer"
  echo "ok: health endpoint"
fi

step "running lanbazctl daemon status"
"$WORK/bin/lanbazctl" --state-dir "$STATE" --json daemon status > "$WORK/status.json" || fail "lanbazctl daemon status"
cat "$WORK/status.json"
grep -q '"state"' "$WORK/status.json" || fail "the status payload has no state field"
echo "ok: daemon status"

step "running lanbazctl daemon version"
"$WORK/bin/lanbazctl" --state-dir "$STATE" daemon version || fail "lanbazctl daemon version"
echo "ok: daemon version"

step "verifying the token is not in the log"
if grep -q "$(sed -n 's/.*"api_token": *"\([^"]*\)".*/\1/p' "$STATE/daemon.json")" "$WORK/daemon.err"; then
  fail "the api token leaked into the log"
fi
echo "ok: no token in the log"

step "shutting down"
"$WORK/bin/lanbazctl" --state-dir "$STATE" daemon shutdown || fail "lanbazctl daemon shutdown"
for _ in $(seq 1 50); do
  kill -0 "$DAEMON_PID" 2>/dev/null || break
  sleep 0.1
done
if kill -0 "$DAEMON_PID" 2>/dev/null; then
  fail "the daemon did not exit after shutdown"
fi
DAEMON_PID=""

[[ -f "$STATE/daemon.json" ]] && fail "the state file survived shutdown"
echo "ok: graceful shutdown removed the state file"

echo
echo "Phase 0 acceptance checks passed."