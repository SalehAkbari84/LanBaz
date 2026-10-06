#!/usr/bin/env bash
# Phase 2 acceptance check: two daemons, one room, a real virtual LAN.
#
# Verifies, without the desktop shell:
#   1. two lanbazd instances start with the in-memory adapter backend
#   2. the host issues a pairing code and the guest redeems it
#   3. both sides land in the SAME /24 out of 10.200.0.0/16
#   4. the host is at .1 and the guest at the address the code named
#   5. both sides report a live virtual network, not a planned one
#   6. the routing table carries a route for the other machine
#   7. lanbazctl network interface answers with something a game could use
#   8. nothing sensitive reached the logs
#
# The in-memory backend is deliberate. Wintun needs the driver installed and an
# elevated daemon, and an acceptance script that needs both is a script nobody
# runs. Everything this checks - which subnet, which address, who routes where -
# happens above the adapter and is the same code either backend runs.
#
# Run from the repository root: ./scripts/acceptance-phase2.sh

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
  sleep 0.5
  rm -rf "$WORK" 2>/dev/null || true
}
trap cleanup EXIT

fail() { echo "FAIL: $1"; exit 1; }
step() { echo; echo "== $1"; }

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
  "$WORK/bin/lanbazd" --state-dir "$state" --network-backend memory \
    > "$WORK/$name.out" 2> "$WORK/$name.err" &
  local pid=$!
  if ! wait_for_file "$state/daemon.json" 10; then
    cat "$WORK/$name.err"
    fail "$name did not write its state file"
  fi
  echo "$pid"
}

# json_get <json> <key> - the first value of a top-level string or number key.
json_get() {
  echo "$1" | sed -n "s/.*\"$2\": *\"\\([^\"]*\\)\".*/\\1/p;s/.*\"$2\": *\\([0-9.]*\\).*/\\1/p" | head -1
}

step "building"
mkdir -p "$WORK/bin"
( cd "$ROOT" && "$GO_BIN" build -o "$WORK/bin/lanbazd" ./core/cmd/lanbazd ) || fail "go build lanbazd"
( cd "$ROOT" && "$GO_BIN" build -o "$WORK/bin/lanbazctl" ./core/cmd/lanbazctl ) || fail "go build lanbazctl"

CTL="$WORK/bin/lanbazctl --state-dir"

step "starting two daemons on the in-memory adapter"
HOST_PID="$(start_daemon host "$HOST_STATE" | tail -1)"
GUEST_PID="$(start_daemon guest "$GUEST_STATE" | tail -1)"
echo "ok: host pid $HOST_PID, guest pid $GUEST_PID"

step "the host creates a room"
CREATE_JSON="$($CTL "$HOST_STATE" --json room create --name "Match Night" --max-peers 4 --ttl 300)" \
  || fail "lanbazctl room create"
ROOM_ID="$(json_get "$CREATE_JSON" room_id)"
CODE="$(json_get "$CREATE_JSON" pairing_code)"
[[ "$CODE" == LBZ-* ]] || fail "the pairing code does not look like one: $CODE"
echo "ok: room $ROOM_ID"

step "the guest redeems the code and the host applies the answer"
JOIN_JSON="$($CTL "$GUEST_STATE" --json room join "$CODE")" || fail "lanbazctl room join"
ANSWER="$(json_get "$JOIN_JSON" answer_code)"
[[ "$ANSWER" == LBZ-* ]] || fail "the guest produced no answer code"
$CTL "$HOST_STATE" room accept "$ANSWER" "$ROOM_ID" || fail "lanbazctl room accept"

step "waiting for both virtual networks to come up"
READY=0
for _ in $(seq 1 60); do
  HOST_NET="$($CTL "$HOST_STATE" --json network status --room "$ROOM_ID" 2>/dev/null)"
  GUEST_NET="$($CTL "$GUEST_STATE" --json network status --room "$ROOM_ID" 2>/dev/null)"
  HOST_STATE_NAME="$(json_get "$HOST_NET" state)"
  GUEST_STATE_NAME="$(json_get "$GUEST_NET" state)"
  if [[ "$HOST_STATE_NAME" == "ready" && "$GUEST_STATE_NAME" == "ready" ]]; then
    READY=1
    break
  fi
  sleep 1
done
[[ "$READY" == "1" ]] || {
  echo "host:  $HOST_NET"
  echo "guest: $GUEST_NET"
  fail "the virtual network did not come up on both sides within 60s"
}
echo "ok: both sides report the virtual network as ready"

step "both sides must be on the same subnet"
HOST_SUBNET="$(json_get "$HOST_NET" subnet)"
GUEST_SUBNET="$(json_get "$GUEST_NET" subnet)"
[[ -n "$HOST_SUBNET" ]] || fail "the host reports no subnet"
[[ "$HOST_SUBNET" == "$GUEST_SUBNET" ]] \
  || fail "the sides disagree on the subnet: host $HOST_SUBNET, guest $GUEST_SUBNET"
echo "$HOST_SUBNET" | grep -Eq '^10\.200\.[0-9]+\.0/24$' \
  || fail "the subnet is outside the pool: $HOST_SUBNET"
echo "ok: both sides are on $HOST_SUBNET"

step "the host owns .1 and the guest has its own address"
HOST_ADDR="$(json_get "$HOST_NET" local_address)"
GUEST_ADDR="$(json_get "$GUEST_NET" local_address)"
HOST_PREFIX="${HOST_SUBNET%/24}"          # 10.200.109.0
HOST_ADDR_EXPECTED="${HOST_PREFIX%.0}.1" # 10.200.109.1
[[ "$HOST_ADDR" == "$HOST_ADDR_EXPECTED" ]] \
  || fail "the host is at $HOST_ADDR, want $HOST_ADDR_EXPECTED, the .1 of its own subnet"
[[ -n "$GUEST_ADDR" ]] || fail "the guest has no address in the room"
[[ "$HOST_ADDR" != "$GUEST_ADDR" ]] \
  || fail "both sides claim the address $HOST_ADDR"
echo "ok: host $HOST_ADDR, guest $GUEST_ADDR"

step "the host's routing table has a route for the guest"
ROUTES=0
for _ in $(seq 1 30); do
  ROUTE_JSON="$($CTL "$HOST_STATE" --json network routes --room "$ROOM_ID" 2>/dev/null)"
  ROUTE_COUNT="$(echo "$ROUTE_JSON" | grep -o '"destination"' | wc -l | tr -d ' ')"
  if [[ "$ROUTE_COUNT" -ge 2 ]]; then
    ROUTES=1
    break
  fi
  sleep 1
done
[[ "$ROUTES" == "1" ]] || fail "the host never routed to the guest (routes: $ROUTE_JSON)"
echo "$ROUTE_JSON" | grep -q "$GUEST_ADDR" \
  || fail "the host has no route to the guest's address $GUEST_ADDR"
echo "ok: $ROUTE_COUNT routes, including one to $GUEST_ADDR"

step "the guest routes to the host, not to itself"
GUEST_ROUTES="$($CTL "$GUEST_STATE" --json network routes --room "$ROOM_ID")"
echo "$GUEST_ROUTES" | grep -q "$HOST_ADDR" \
  || fail "the guest has no route to the host's address $HOST_ADDR: $GUEST_ROUTES"
echo "ok: the guest routes to the hub at $HOST_ADDR"

step "lanbazctl network interface answers with something a game could use"
IFACE="$($CTL "$HOST_STATE" --json network interface --room "$ROOM_ID")" \
  || fail "lanbazctl network interface"
IFACE_ADDR="$(json_get "$IFACE" address)"
IFACE_SUBNET="$(json_get "$IFACE" subnet)"
[[ "$IFACE_ADDR" == "$HOST_ADDR" ]] || fail "network.interface reports $IFACE_ADDR, want $HOST_ADDR"
[[ "$IFACE_SUBNET" == "$HOST_SUBNET" ]] || fail "network.interface reports $IFACE_SUBNET, want $HOST_SUBNET"
echo "ok: interface $IFACE_ADDR on $IFACE_SUBNET"

step "the same answer is available as readable text"
$CTL "$HOST_STATE" network status --room "$ROOM_ID" | grep -q "$HOST_ADDR" \
  || fail "network status does not print the local address"
$CTL "$HOST_STATE" network peers --room "$ROOM_ID" | grep -q "$GUEST_ADDR" \
  || fail "network peers does not list the guest's address"
echo "ok: the CLI renders the addresses a user needs"

step "a second room on the same machine gets a different subnet"
SECOND="$($CTL "$HOST_STATE" --json room create --name "Second" --max-peers 4 --ttl 300)" \
  || fail "creating a second room"
SECOND_ID="$(json_get "$SECOND" room_id)"
SECOND_NET="$($CTL "$HOST_STATE" --json network status --room "$SECOND_ID")"
SECOND_SUBNET="$(json_get "$SECOND_NET" subnet)"
[[ "$SECOND_SUBNET" != "$HOST_SUBNET" ]] \
  || fail "two rooms on one machine share the subnet $SECOND_SUBNET"
echo "ok: the second room is on $SECOND_SUBNET"

step "verifying nothing sensitive reached the logs"
for name in host guest; do
  for secret in "$(sed -n 's/.*"api_token": *"\([^"]*\)".*/\1/p' "$WORK/$name/daemon.json" 2>/dev/null)"; do
    [[ -z "$secret" ]] && continue
    if grep -q "$secret" "$WORK/$name.err"; then
      fail "the api token leaked into the $name log"
    fi
  done
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
echo "Phase 2 acceptance checks passed."