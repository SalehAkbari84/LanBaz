package api

import (
	"context"
	"encoding/json"
	"time"

	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// This file is the control API's view of the virtual LAN.
//
// Every method here is read-only and takes an optional room id. That shape is
// deliberate: the network layer is the part most likely to be missing on a given
// machine - no driver, no rights, a container - and a UI that could only ask
// about it once a room existed would have nothing to show a user whose very
// first attempt is the thing that failed. A status call with no room id
// therefore answers with the first room that has one, and with a plain stopped
// status when there are none.
//
// The room manager is reached through an interface so this package still does
// not learn that Wintun, IPAM or the packet router exist.

// NetworkService is the subset of the room manager the network methods need.
//
// RoomIDs rather than All, because the room service already has an All that
// returns room pointers and Go will not let one type satisfy two interfaces that
// disagree about a method's signature. Renaming the network method is cheaper
// than renaming the one every existing caller uses.
type NetworkService interface {
	// First returns the room a room-less network query applies to, or false when
	// no room is open.
	First() (string, bool)
	// RoomIDs returns every open room id, in a stable order.
	RoomIDs() []string
	// NetworkStatus renders one room's virtual LAN.
	NetworkStatus(ctx context.Context, roomID string) (protocol.NetworkStatus, error)
	// NetworkRoutes renders one room's routing table.
	NetworkRoutes(ctx context.Context, roomID string) ([]protocol.RouteEntry, error)
}

// SetNetworkService installs the network service and registers the network
// methods. It is separate from SetRoomService because a build with no adapter
// backend still serves the other methods and must report that fact cleanly
// rather than fail to start.
func (s *Server) SetNetworkService(svc NetworkService) error {
	if svc == nil {
		return protocol.NewError(protocol.CodeConfigInvalid, "api: network service must not be nil")
	}
	s.netSvc = svc
	for method, h := range map[string]Handler{
		protocol.MethodNetworkStatus:    s.handleNetworkStatus,
		protocol.MethodNetworkRoutes:    s.handleNetworkRoutes,
		protocol.MethodNetworkInterface: s.handleNetworkInterface,
	} {
		if err := s.Register(method, h); err != nil {
			return err
		}
	}
	s.log.Info("network methods registered")
	return nil
}

// networkRequest is the shared payload of the three network methods.
type networkRequest struct {
	RoomID string `json:"room_id,omitempty"`
}

// resolveRoom picks the room a request refers to.
func (s *Server) resolveRoom(req networkRequest) (string, error) {
	if req.RoomID != "" {
		return req.RoomID, nil
	}
	if s.netSvc == nil {
		return "", protocol.NewError(protocol.CodeUnsupportedVersion,
			"api: this daemon build has no virtual network")
	}
	roomID, ok := s.netSvc.First()
	if !ok {
		return "", protocol.NewError(protocol.CodeNotFound,
			"api: no room is open, so there is no virtual network to describe")
	}
	return roomID, nil
}

// requireNetwork returns the service or a clear error.
func (s *Server) requireNetwork() (NetworkService, error) {
	if s.netSvc == nil {
		return nil, protocol.NewError(protocol.CodeUnsupportedVersion,
			"api: this daemon build has no virtual network")
	}
	return s.netSvc, nil
}

func (s *Server) handleNetworkStatus(ctx context.Context, params json.RawMessage) (any, error) {
	svc, err := s.requireNetwork()
	if err != nil {
		return nil, err
	}
	var req networkRequest
	if err := decode(params, &req); err != nil {
		return nil, err
	}
	roomID, err := s.resolveRoom(req)
	if err != nil {
		return nil, err
	}
	return svc.NetworkStatus(ctx, roomID)
}

// handleNetworkRoutes returns only the routing table, which is what a user wants
// when they are debugging reachability and do not care about the rest.
func (s *Server) handleNetworkRoutes(ctx context.Context, params json.RawMessage) (any, error) {
	svc, err := s.requireNetwork()
	if err != nil {
		return nil, err
	}
	var req networkRequest
	if err := decode(params, &req); err != nil {
		return nil, err
	}
	roomID, err := s.resolveRoom(req)
	if err != nil {
		return nil, err
	}
	return svc.NetworkRoutes(ctx, roomID)
}

// handleNetworkInterface returns the adapter's addressing on its own, which is
// the smallest answer to "what address do I type into the game".
func (s *Server) handleNetworkInterface(ctx context.Context, params json.RawMessage) (any, error) {
	svc, err := s.requireNetwork()
	if err != nil {
		return nil, err
	}
	var req networkRequest
	if err := decode(params, &req); err != nil {
		return nil, err
	}
	roomID, err := s.resolveRoom(req)
	if err != nil {
		return nil, err
	}
	st, err := svc.NetworkStatus(ctx, roomID)
	if err != nil {
		return nil, err
	}
	return protocol.NetworkInterface{
		RoomID:    st.RoomID,
		Interface: st.Interface,
		Adapter:   st.Adapter,
		Subnet:    st.Subnet,
		Address:   st.LocalAddress,
		MTU:       st.MTU,
		Broadcast: st.Broadcast,
		Multicast: st.Multicast,
		State:     st.State,
		StartedAt: st.StartedAt,
		IsHost:    st.IsHost,
		CheckedAt: time.Now().UTC(),
	}, nil
}
