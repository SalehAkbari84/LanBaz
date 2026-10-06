package room

import (
	"context"
	"encoding/json"

	"github.com/lanbaz/lanbaz/core/internal/peer"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// Live changes to a room's network, without closing the room.
//
// The links between players (the transport) and the room itself stay up; only
// the virtual adapter underneath is swapped: Wintun for a standard room, TAP
// for a classic one, or the same kind rebuilt with new settings (MTU, adapter
// priority, broadcast rate). Players see the LAN blink for a few seconds while
// the adapter is replaced, not a closed room and a new invite.
//
// The host decides the mode. It tells every guest with a "mode" message and
// each guest rebuilds its own network the same way.

const appKindMode = "mode"

type modeBody struct {
	Mode string `json:"m"`
}

func (r *Room) installModeSync() {
	r.onAppKind(appKindMode, func(from *peer.Peer, msg peer.AppMessage) {
		if r.isHost || !from.IsHost() {
			return // only the host's word counts
		}
		var b modeBody
		if json.Unmarshal(msg.Body, &b) != nil {
			return
		}
		if b.Mode != protocol.RoomModeL2 {
			b.Mode = protocol.RoomModeL3
		}
		if b.Mode == r.Mode() || r.onModeChange == nil {
			return
		}
		r.log.Info("the host switched the room's network type; rebuilding the network", "room", r.id, "mode", b.Mode)
		go r.onModeChange(b.Mode)
	})
}

func (r *Room) setMode(mode string) {
	r.mu.Lock()
	r.mode = mode
	r.mu.Unlock()
}

// announceMode tells every guest the room's mode.
func (r *Room) announceMode() error {
	return r.broadcastApp(appKindMode, modeBody{Mode: r.Mode()}, "")
}

// RebuildNetwork replaces the room's virtual network with a fresh one from
// factory and gives every player their address back. The room and its links
// are untouched.
func (r *Room) RebuildNetwork(ctx context.Context, factory NetworkFactory) {
	r.detachNetwork(ctx)
	// The new network has no peers yet: forget the bindings (not the leases in
	// the allocator, so everybody gets the same address back) and rebind.
	r.leasesMu.Lock()
	for id := range r.leases {
		delete(r.leases, id)
	}
	r.leasesMu.Unlock()
	r.netMu.Lock()
	r.netErr = nil
	r.netMu.Unlock()
	r.attachNet(ctx, factory)
	r.reconcile()
}

// SetMode switches a hosted room between standard ("l3") and classic ("l2")
// LAN while it stays open, and tells the guests to follow.
func (m *Manager) SetMode(ctx context.Context, roomID, mode string) (protocol.RoomSummary, error) {
	r, err := m.Get(roomID)
	if err != nil {
		return protocol.RoomSummary{}, err
	}
	if !r.IsHost() {
		return protocol.RoomSummary{}, protocol.NewError(protocol.CodePeerRejected,
			"room: only the host can change the room's network type")
	}
	mode = normMode(mode)
	if mode == r.Mode() {
		return r.Summary(), nil
	}
	if mode == protocol.RoomModeL2 {
		if m.l2Factory == nil || (m.l2Check != nil && m.l2Check() != nil) {
			return protocol.RoomSummary{}, protocol.NewError(protocol.CodeUnsupportedVersion,
				"room: classic LAN (L2) needs the TAP-Windows driver; install it first")
		}
		if err := m.l2Busy(roomID); err != nil {
			return protocol.RoomSummary{}, err
		}
	}
	r.setMode(mode)
	if err := r.announceMode(); err != nil {
		m.log.Warn("could not tell every guest about the new network type", "room", roomID, "error", err)
	}
	m.log.Info("room network type changed live", "room", roomID, "mode", mode)
	r.RebuildNetwork(ctx, m.factoryFor(r))
	s := r.Summary()
	m.emitRoom(protocol.EventRoomUpdated, protocol.RoomEvent{RoomID: roomID, Name: r.name, Room: &s, At: m.now()})
	return s, nil
}

// guestModeChanged follows the host's switch on a guest.
func (m *Manager) guestModeChanged(r *Room, mode string) {
	if mode == protocol.RoomModeL2 && (m.l2Factory == nil || (m.l2Check != nil && m.l2Check() != nil)) {
		r.setMode(mode)
		r.detachNetwork(context.Background())
		r.netMu.Lock()
		r.netErr = protocol.NewError(protocol.CodeUnsupportedVersion,
			"the host switched this room to classic LAN, which needs the TAP-Windows driver on this PC; install it from Settings → Network")
		r.netMu.Unlock()
		m.log.Warn("the host switched to classic LAN but the TAP driver is missing here", "room", r.ID())
	} else {
		r.setMode(mode)
		r.RebuildNetwork(context.Background(), m.factoryFor(r))
	}
	s := r.Summary()
	m.emitRoom(protocol.EventRoomUpdated, protocol.RoomEvent{RoomID: r.ID(), Name: r.name, Room: &s, At: m.now()})
}

// RebuildRoomNetwork rebuilds one room's network in place.
func (m *Manager) RebuildRoomNetwork(ctx context.Context, roomID string) {
	if r, err := m.Get(roomID); err == nil {
		r.RebuildNetwork(ctx, m.factoryFor(r))
	}
}
