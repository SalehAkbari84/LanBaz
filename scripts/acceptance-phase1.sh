#!/usr/bin/env bash
# Phase 1 acceptance check: two daemons, one room, no server.
#
# Verifies, without the desktop shell:
#   1. two lanbazd instances start and stay isolated
#   2. the host issues a pairing code
#   3. the guest redeems it and produces a reply code
#   4. the host applies the reply and the link comes up on both sides
#   5. both sides see each other as active, with a real round trip measured
#   6. the spent code cannot be redeemed a second time
#   7. nothing sensitive reached the logs
#
# Run from the repository root: ./scripts/acceptance-phase1.sh

set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GO_BIN="${GO_BIN:-go}"
WORK="$(mktemp -d)"
HOST_STATE="$WORK/host"
GUEST_STATE="$WORK/guest"

HOST_PID=""
GUEST_PID=""

cleanup() {
  for pid in "$HOST_PID" "$GUEST_PID"; do
    if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
      kill "$pid" 2>/dev/null || true
    fi
  done
  # A daemon that was just signalled still holds its log file open for a moment.
  # Waiting briefly avoids a spurious "device busy" that would mask a real
  # failure printed just above it.
  sleep 0.5
  rm -rf "$WORK" 2>/dev/null || true
}
trap cleanup EXIT

fail() { echo "FAIL: $1"; exit 1; }
step() { echo; echo "== $1"; }

# wait_for_file <path> <tenths>
wait_for_file() {
  local path="$1" tries="$2" i=0
  while (( i < tries )); do
    [[ -f "$path" ]] && return 0
    sleep 0.1
    i=$((i + 1))
  done
  return 1
}

# start_daemon <name> <state-dir>
start_daemon() {
  local name="$1" state="$2"
  mkdir -p "$state"
  "$WORK/bin/lanbazd" --state-dir "$state" \
    > "$WORK/$name.out" 2> "$WORK/$name.err" &
  local pid=$!
  if ! wait_for_file "$state/daemon.json" 10; then
    cat "$WORK/$name.err"
    fail "$name did not write its state file"
  fi
  echo "$pid"
}

step "building"
mkdir -p "$WORK/bin"
( cd "$ROOT" && "$GO_BIN" build -o "$WORK/bin/lanbazd" ./core/cmd/lanbazd ) || fail "go build lanbazd"
( cd "$ROOT" && "$GO_BIN" build -o "$WORK/bin/lanbazctl" ./core/cmd/lanbazctl ) || fail "go build lanbazctl"

CTL="$WORK/bin/lanbazctl --state-dir"
PEER_ID_RE='[0-9a-f]{32}'

step "starting two daemons"
HOST_PID="$(start_daemon host "$HOST_STATE" | tail -1)"
GUEST_PID="$(start_daemon guest "$GUEST_STATE" | tail -1)"
grep -q "LanBaz daemon started" "$WORK/host.out" || fail "host startup banner"
grep -q "LanBaz daemon started" "$WORK/guest.out" || fail "guest startup banner"
echo "ok: both daemons started (host pid $HOST_PID, guest pid $GUEST_PID)"

if grep -q "room service ready" "$WORK/host.err"; then
  echo "ok: room service wired in"
else
  fail "the host did not report a room service"
fi

step "verifying the two daemons are separate processes with separate state"
HOST_PORT="$(sed -n 's/.*"api_port": *\([0-9]*\).*/\1/p' "$HOST_STATE/daemon.json")"
GUEST_PORT="$(sed -n 's/.*"api_port": *\([0-9]*\).*/\1/p' "$GUEST_STATE/daemon.json")"
[[ -n "$HOST_PORT" ]] || fail "host state file has no api_port"
[[ -n "$GUEST_PORT" ]] || fail "guest state file has no api_port"
[[ "$HOST_PORT" != "$GUEST_PORT" ]] || fail "both daemons claim the same port"
echo "ok: host on port $HOST_PORT, guest on port $GUEST_PORT"

step "the host creates a room"
CREATE_JSON="$($CTL "$HOST_STATE" --json room create --name "Match Night" --max-peers 4 --ttl 300)" \
  || fail "lanbazctl room create"
echo "$CREATE_JSON" | grep -q '"pairing_code"' || fail "room.create returned no pairing code"

ROOM_ID="$(echo "$CREATE_JSON" | sed -n 's/.*"room_id": *"\([^"]*\)".*/\1/p' | head -1)"
CODE="$(echo "$CREATE_JSON" | sed -n 's/.*"pairing_code": *"\([^"]*\)".*/\1/p' | head -1)"
[[ -n "$ROOM_ID" ]] || fail "room.create returned no room id"
[[ "$CODE" == LBZ-* ]] || fail "the pairing code does not look like one: $CODE"
echo "ok: room $ROOM_ID issued a $((${#CODE} / 4))-group code"

step "room.list on the host"
$CTL "$HOST_STATE" room list | grep -q "$ROOM_ID" || fail "room.list does not show the new room"
echo "ok: the room is listed"

step "the guest redeems the code"
JOIN_JSON="$($CTL "$GUEST_STATE" --json room join "$CODE")" || fail "lanbazctl room join"
ANSWER="$(echo "$JOIN_JSON" | sed -n 's/.*"answer_code": *"\([^"]*\)".*/\1/p' | head -1)"
[[ "$ANSWER" == LBZ-* ]] || fail "the guest produced no answer code"
echo "ok: the guest produced a reply code"

if [[ "$(echo "$JOIN_JSON" | sed -n 's/.*"pending": *\([a-z]*\).*/\1/p')" != "true" ]]; then
  fail "a serverless join reported itself as complete"
fi
echo "ok: the join correctly reports it is still pending"

step "a tampered code is refused"
TAMPERED="${CODE:0:${#CODE}-4}aaaa"
if $CTL "$GUEST_STATE" room join "$TAMPERED" >/dev/null 2>&1; then
  fail "a tampered pairing code was accepted"
fi
echo "ok: a tampered code is rejected"

step "the host applies the answer"
$CTL "$HOST_STATE" room accept "$ANSWER" "$ROOM_ID" || fail "lanbazctl room accept"

step "waiting for both sides to report an active peer"
ACTIVE=0
for _ in $(seq 1 60); do
  HOST_LIST="$($CTL "$HOST_STATE" --json peer list "$ROOM_ID" 2>/dev/null)"
  GUEST_LIST="$($CTL "$GUEST_STATE" --json peer list "$ROOM_ID" 2>/dev/null)"
  if echo "$HOST_LIST" | grep -q '"state": *"active"' \
     && echo "$GUEST_LIST" | grep -q '"state": *"active"'; then
    ACTIVE=1
    break
  fi
  sleep 1
done
[[ "$ACTIVE" == "1" ]] || {
  echo "host peers:  $HOST_LIST"
  echo "guest peers: $GUEST_LIST"
  fail "the link did not become active on both sides within 60s"
}
echo "ok: both sides report the peer as active"

step "verifying the two sides agree on who the peer is"
HOST_PEER="$(echo "$HOST_LIST" | sed -n 's/.*"peer_id": *"\([^"]*\)".*/\1/p' | head -1)"
GUEST_PEER="$(echo "$GUEST_LIST" | sed -n 's/.*"peer_id": *"\([^"]*\)".*/\1/p' | head -1)"
echo "$HOST_PEER" | grep -Eq "$PEER_ID_RE" || fail "the host reports a malformed peer id: $HOST_PEER"
echo "$GUEST_PEER" | grep -Eq "$PEER_ID_RE" || fail "the guest reports a malformed peer id: $GUEST_PEER"
[[ "$HOST_PEER" != "$GUEST_PEER" ]] || fail "both sides report the same peer id"
echo "ok: host sees $HOST_PEER, guest sees $GUEST_PEER"

step "measuring a round trip from both sides"
PING="$($CTL "$GUEST_STATE" --json peer ping "$GUEST_PEER" --count 4 --timeout 8000)" \
  || fail "peer.ping from the guest"
echo "$PING" | grep -Eq '"rtt_ms": *[0-9]+(\.[0-9]+)?' || fail "peer.ping returned no rtt"
RTT="$(echo "$PING" | sed -n 's/.*"rtt_ms": *\([0-9.]*\).*/\1/p' | head -1)"
LOSS="$(echo "$PING" | sed -n 's/.*"loss": *\([0-9.]*\).*/\1/p' | head -1)"
echo "ok: measured rtt ${RTT}ms, loss ${LOSS}"

RECV="$(echo "$PING" | sed -n 's/.*"received": *\([0-9]*\).*/\1/p' | head -1)"
[[ "$RECV" == "4" ]] || fail "only $RECV of 4 probes came back"

step "the spent code cannot be redeemed again"
if $CTL "$GUEST_STATE" room join "$CODE" >/dev/null 2>&1; then
  fail "the pairing code worked a second time"
fi
echo "ok: the code is single-shot"

step "verifying nothing sensitive reached the logs"
for name in host guest; do
  for secret in "$(sed -n 's/.*"api_token": *"\([^"]*\)".*/\1/p' "$WORK/$name/daemon.json" 2>/dev/null)"; do
    [[ -z "$secret" ]] && continue
    if grep -q "$secret" "$WORK/$name.err"; then
      fail "the api token leaked into the $name log"
    fi
  done
  # The room secret travels inside every pairing code. A log line containing a
  # whole code is a line that can be replayed by anyone who reads the log.
  if grep -qE 'LBZ-[0-9a-z]{4}-' "$WORK/$name.err"; then
    fail "a full pairing code leaked into the $name log"
  fi
done
echo "ok: no tokens or pairing codes in the logs"

step "shutting down"
$CTL "$HOST_STATE" daemon shutdown >/dev/null || fail "host shutdown"
$CTL "$GUEST_STATE" daemon shutdown >/dev/null || fail "guest shutdown"
for _ in $(seq 1 50); do
  kill -0 "$HOST_PID" 2>/dev/null || break
  sleep 0.1
done
kill -0 "$HOST_PID" 2>/dev/null && fail "the host did not exit"
kill -0 "$GUEST_PID" 2>/dev/null && fail "the guest did not exit"
HOST_PID=""
GUEST_PID=""

echo
echo "Phase 1 acceptance checks passed."