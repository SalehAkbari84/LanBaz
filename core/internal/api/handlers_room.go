package api

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/lanbaz/lanbaz/core/internal/pairing"
	"github.com/lanbaz/lanbaz/core/internal/peer"
	"github.com/lanbaz/lanbaz/core/internal/room"
	"github.com/lanbaz/lanbaz/core/internal/transport"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// This file is the seam between the localhost control API and the room manager.
//
// It translates JSON into typed requests and returns typed responses; it holds
// no state of its own. That matters because every one of these methods is a
// multi-second operation - gathering ICE candidates takes seconds and cannot be
// interrupted by a client disconnecting - so a handler that blocked the API's
// single dispatch goroutine would freeze the UI while a room was being created.
// Handlers therefore do their work on the request context and return promptly
// or not at all.
//
// The room manager is injected rather than constructed here. api must not import
// the transport implementations, or swapping WebRTC for UDP would pull Pion into
// the control plane.

// RoomService is the subset of the room manager the API needs.
//
// It is an interface so the api package can be tested without constructing real
// transports, and so the daemon remains the single place that knows which
// implementation is wired in.
type RoomService interface {
	Create(ctx context.Context, req protocol.RoomCreateRequest) (protocol.RoomCreateResponse, error)
	Join(ctx context.Context, req protocol.RoomJoinRequest) (protocol.RoomJoinResponse, error)
	Accept(ctx context.Context, req protocol.RoomAcceptRequest) error
	RegeneratePairing(ctx context.Context, req protocol.RoomRegeneratePairingRequest) (protocol.PairingResponse, error)
	Get(id string) (*room.Room, error)
	List() []protocol.RoomSummary
	All() []*room.Room
	Leave(ctx context.Context, id string) error
	Counts() (rooms, peers int)
	SendChat(roomID, text string) (protocol.ChatMessage, error)
	ChatHistory(roomID string) ([]protocol.ChatMessage, error)
	SendVoice(roomID, to string, data json.RawMessage) error
}

// SetRoomService installs the room service and registers the room and peer
// methods. It must be called before Start.
//
// Registering here rather than from the daemon's constructor keeps the two
// concerns separate: the daemon decides *which* implementation is wired in, and
// this decides which methods that implementation can serve.
func (s *Server) SetRoomService(svc RoomService) error {
	if svc == nil {
		return protocol.NewError(protocol.CodeConfigInvalid, "api: room service must not be nil")
	}
	s.roomSvc = svc
	for method, h := range map[string]Handler{
		protocol.MethodRoomCreate:            s.handleRoomCreate,
		protocol.MethodRoomJoin:              s.handleRoomJoin,
		protocol.MethodRoomAccept:            s.handleRoomAccept,
		protocol.MethodRoomLeave:             s.handleRoomLeave,
		protocol.MethodRoomClose:             s.handleRoomClose,
		protocol.MethodRoomGet:               s.handleRoomGet,
		protocol.MethodRoomList:              s.handleRoomList,
		protocol.MethodRoomRegeneratePairing: s.handleRoomRegeneratePairing,
		protocol.MethodPeerList:              s.handlePeerList,
		protocol.MethodPeerGet:               s.handlePeerGet,
		protocol.MethodPeerKick:              s.handlePeerKick,
		protocol.MethodPeerPing:              s.handlePeerPing,
		protocol.MethodPairingInspect:        s.handlePairingInspect,
		protocol.MethodChatSend:              s.handleChatSend,
		protocol.MethodVoiceSignal:           s.handleVoiceSignal,
		protocol.MethodChatHistory:           s.handleChatHistory,
	} {
		if err := s.Register(method, h); err != nil {
			return err
		}
	}
	s.log.Info("room and peer methods registered")
	return nil
}

// requireRooms returns the service or a clear error, so a method that was
// somehow dispatched without one reports a configuration problem instead of
// dereferencing nil.
func (s *Server) requireRooms() (RoomService, error) {
	if s.roomSvc == nil {
		return nil, protocol.NewError(protocol.CodeUnsupportedVersion,
			"api: this daemon build has no room service")
	}
	return s.roomSvc, nil
}

// decode unmarshals a request payload into v.
func decode(params json.RawMessage, v any) error {
	if len(params) == 0 {
		params = json.RawMessage("{}")
	}
	if err := json.Unmarshal(params, v); err != nil {
		return protocol.NewErrorf(protocol.CodeBadRequest, "api: malformed request payload: %v", err)
	}
	return nil
}

func (s *Server) handleRoomCreate(ctx context.Context, params json.RawMessage) (any, error) {
	svc, err := s.requireRooms()
	if err != nil {
		return nil, err
	}
	var req protocol.RoomCreateRequest
	if err := decode(params, &req); err != nil {
		return nil, err
	}
	return svc.Create(ctx, req)
}

func (s *Server) handleRoomJoin(ctx context.Context, params json.RawMessage) (any, error) {
	svc, err := s.requireRooms()
	if err != nil {
		return nil, err
	}
	var req protocol.RoomJoinRequest
	if err := decode(params, &req); err != nil {
		return nil, err
	}
	if req.PairingCode == "" {
		return nil, protocol.NewError(protocol.CodeBadRequest, "api: room.join needs a pairing code")
	}
	return svc.Join(ctx, req)
}

func (s *Server) handleRoomAccept(ctx context.Context, params json.RawMessage) (any, error) {
	svc, err := s.requireRooms()
	if err != nil {
		return nil, err
	}
	var req protocol.RoomAcceptRequest
	if err := decode(params, &req); err != nil {
		return nil, err
	}
	if req.AnswerCode == "" {
		return nil, protocol.NewError(protocol.CodeBadRequest, "api: room.accept needs an answer code")
	}
	// The room id may be omitted from the request because the answer code names
	// its own room; accepting either spelling saves the UI from having to track
	// a value it can read off the code.
	if req.RoomID == "" {
		id, err := room.RoomIDFromCode(req.AnswerCode)
		if err != nil {
			return nil, err
		}
		req.RoomID = id
	}
	if err := svc.Accept(ctx, req); err != nil {
		return nil, err
	}
	return protocol.RoomEvent{RoomID: req.RoomID, At: time.Now().UTC()}, nil
}

// transportID narrows a protocol peer id to the transport's own identifier.
func transportID(id protocol.PeerID) transport.PeerID { return transport.PeerID(id) }

func (s *Server) handleRoomLeave(ctx context.Context, params json.RawMessage) (any, error) {
	svc, err := s.requireRooms()
	if err != nil {
		return nil, err
	}
	var req struct {
		RoomID string `json:"room_id"`
	}
	if err := decode(params, &req); err != nil {
		return nil, err
	}
	if req.RoomID == "" {
		return nil, protocol.NewError(protocol.CodeBadRequest, "api: room.leave needs a room id")
	}
	if err := svc.Leave(ctx, req.RoomID); err != nil {
		return nil, err
	}
	return protocol.RoomEvent{RoomID: req.RoomID, Reason: "left", At: time.Now().UTC()}, nil
}

// handleRoomClose ends a hosted room for everyone. Every guest is told goodbye
// over its control channel, so their UI drops the room at once instead of
// waiting for a liveness timeout.
func (s *Server) handleRoomClose(ctx context.Context, params json.RawMessage) (any, error) {
	svc, err := s.requireRooms()
	if err != nil {
		return nil, err
	}
	var req struct {
		RoomID string `json:"room_id"`
	}
	if err := decode(params, &req); err != nil {
		return nil, err
	}
	if req.RoomID == "" {
		return nil, protocol.NewError(protocol.CodeBadRequest, "api: room.close needs a room id")
	}
	r, err := svc.Get(req.RoomID)
	if err != nil {
		return nil, err
	}
	if !r.IsHost() {
		return nil, protocol.NewError(protocol.CodePeerRejected,
			"api: only the host can close a room; use room.leave to step out of it")
	}
	_ = r.Peers().SendBye("room closed by the host")
	if err := svc.Leave(ctx, req.RoomID); err != nil {
		return nil, err
	}
	return protocol.RoomEvent{RoomID: req.RoomID, Reason: "closed", At: time.Now().UTC()}, nil
}

func (s *Server) handleRoomGet(ctx context.Context, params json.RawMessage) (any, error) {
	svc, err := s.requireRooms()
	if err != nil {
		return nil, err
	}
	var req struct {
		RoomID string `json:"room_id"`
	}
	if err := decode(params, &req); err != nil {
		return nil, err
	}
	if req.RoomID == "" {
		return nil, protocol.NewError(protocol.CodeBadRequest, "api: room.get needs a room id")
	}
	r, err := svc.Get(req.RoomID)
	if err != nil {
		return nil, err
	}
	return r.Summary(), nil
}

func (s *Server) handleRoomList(ctx context.Context, params json.RawMessage) (any, error) {
	svc, err := s.requireRooms()
	if err != nil {
		return nil, err
	}
	_ = params
	return svc.List(), nil
}

func (s *Server) handleRoomRegeneratePairing(ctx context.Context, params json.RawMessage) (any, error) {
	svc, err := s.requireRooms()
	if err != nil {
		return nil, err
	}
	var req protocol.RoomRegeneratePairingRequest
	if err := decode(params, &req); err != nil {
		return nil, err
	}
	if req.RoomID == "" {
		return nil, protocol.NewError(protocol.CodeBadRequest,
			"api: room.regenerate_pairing needs a room id")
	}
	return svc.RegeneratePairing(ctx, req)
}

func (s *Server) handlePeerList(ctx context.Context, params json.RawMessage) (any, error) {
	svc, err := s.requireRooms()
	if err != nil {
		return nil, err
	}
	var req struct {
		RoomID string `json:"room_id"`
	}
	if err := decode(params, &req); err != nil {
		return nil, err
	}
	if req.RoomID == "" {
		return nil, protocol.NewError(protocol.CodeBadRequest, "api: peer.list needs a room id")
	}
	r, err := svc.Get(req.RoomID)
	if err != nil {
		return nil, err
	}
	return r.Peers().List(), nil
}

func (s *Server) handlePeerGet(ctx context.Context, params json.RawMessage) (any, error) {
	svc, err := s.requireRooms()
	if err != nil {
		return nil, err
	}
	var req protocol.PeerPingRequest
	if err := decode(params, &req); err != nil {
		return nil, err
	}
	if req.PeerID == "" {
		return nil, protocol.NewError(protocol.CodeBadRequest, "api: peer.get needs a peer id")
	}
	for _, r := range svc.All() {
		p, err := r.Peers().Lookup(string(req.PeerID))
		if err != nil {
			continue
		}
		out := p.Summary()
		if addr, ok := r.Lease(p.ID()); ok {
			out.VirtualAddress = addr.String()
		}
		return out, nil
	}
	return nil, protocol.NewErrorf(protocol.CodeNotFound, "api: no peer %s in any room", req.PeerID)
}

func (s *Server) handlePeerKick(ctx context.Context, params json.RawMessage) (any, error) {
	svc, err := s.requireRooms()
	if err != nil {
		return nil, err
	}
	var req struct {
		RoomID string          `json:"room_id"`
		PeerID protocol.PeerID `json:"peer_id"`
	}
	if err := decode(params, &req); err != nil {
		return nil, err
	}
	if req.RoomID == "" || req.PeerID == "" {
		return nil, protocol.NewError(protocol.CodeBadRequest,
			"api: peer.kick needs a room id and a peer id")
	}
	r, err := svc.Get(req.RoomID)
	if err != nil {
		return nil, err
	}
	if !r.IsHost() {
		return nil, protocol.NewError(protocol.CodePeerRejected, "api: only the host can remove a player")
	}
	p, err := r.Peers().Lookup(string(req.PeerID))
	if err != nil {
		return nil, protocol.NewErrorf(protocol.CodeNotFound, "api: no peer %s in room %s", req.PeerID, req.RoomID)
	}
	// Removing the peer closes its link and stops probing it, which is what
	// "kick" means here. It is told first, so its UI drops the room at once.
	_ = r.Peers().SendByeTo(p.ID(), "removed by the host")
	r.Peers().Remove(p.ID())
	return protocol.PeerEvent{
		PeerID: req.PeerID,
		RoomID: req.RoomID,
		Reason: "kicked",
		At:     time.Now().UTC(),
	}, nil
}

func (s *Server) handlePeerPing(ctx context.Context, params json.RawMessage) (any, error) {
	svc, err := s.requireRooms()
	if err != nil {
		return nil, err
	}
	var req protocol.PeerPingRequest
	if err := decode(params, &req); err != nil {
		return nil, err
	}
	if req.PeerID == "" {
		return nil, protocol.NewError(protocol.CodeBadRequest, "api: peer.ping needs a peer id")
	}
	pm, p := findPeers(svc, req.PeerID)
	if pm == nil {
		return nil, protocol.NewErrorf(protocol.CodeNotFound, "api: no peer %s in any room", req.PeerID)
	}
	res, err := pm.Ping(ctx, p.ID(), req.Count, req.TimeoutMillis)
	out := protocol.PeerPingResult{
		PeerID:    req.PeerID,
		Sent:      res.Sent,
		Received:  res.Received,
		RTTMillis: msOf(res.RTT),
		JitterMs:  msOf(res.Jitter),
		Loss:      res.Loss,
		MinMillis: msOf(res.Min),
		MaxMillis: msOf(res.Max),
	}
	if err != nil {
		// A burst that went out is reported as a result, loss and all: "4 sent,
		// 0 received" is the answer the user asked for. Only a ping that could
		// not be sent at all is an error. (The dispatcher drops the payload of
		// a failed call, so returning both would lose the measurement.)
		if res.Sent == 0 {
			return nil, err
		}
		return out, nil
	}
	return out, nil
}

// findPeers locates the peer manager that owns a peer, searching every room.
//
// A peer id is unique per installation, so scanning is unambiguous. It exists
// because the UI's peer list is per room but its actions are not: a user
// clicking "ping" on a row should not have to tell the daemon which room the row
// came from.
func findPeers(svc RoomService, id protocol.PeerID) (*peer.Manager, *peer.Peer) {
	for _, r := range svc.All() {
		if p, err := r.Peers().Lookup(string(id)); err == nil {
			return r.Peers(), p
		}
	}
	return nil, nil
}

// msOf converts a duration to fractional milliseconds for the protocol DTO.
func msOf(d time.Duration) float64 {
	if d <= 0 {
		return 0
	}
	return float64(d) / float64(time.Millisecond)
}

// SettingsService reads and changes the user-editable daemon settings.
type SettingsService interface {
	Get() protocol.Settings
	Set(protocol.Settings) (protocol.Settings, error)
}

// SetSettingsService registers settings.get and settings.set.
func (s *Server) SetSettingsService(svc SettingsService) error {
	if svc == nil {
		return protocol.NewError(protocol.CodeConfigInvalid, "api: settings service must not be nil")
	}
	if err := s.Register(protocol.MethodSettingsGet, func(context.Context, json.RawMessage) (any, error) {
		return svc.Get(), nil
	}); err != nil {
		return err
	}
	return s.Register(protocol.MethodSettingsSet, func(_ context.Context, params json.RawMessage) (any, error) {
		var req protocol.Settings
		if err := decode(params, &req); err != nil {
			return nil, err
		}
		return svc.Set(req)
	})
}

// handlePairingInspect tells the UI what a pasted or opened code is, without
// redeeming it.
func (s *Server) handlePairingInspect(_ context.Context, params json.RawMessage) (any, error) {
	svc, err := s.requireRooms()
	if err != nil {
		return nil, err
	}
	var req protocol.PairingInspectRequest
	if err := decode(params, &req); err != nil {
		return nil, err
	}
	code, err := pairing.Decode(strings.TrimSpace(req.Code))
	if err != nil {
		return nil, err
	}
	info := protocol.PairingInfo{
		Kind:      code.Kind,
		RoomID:    code.RoomID,
		RoomName:  code.RoomName,
		HostName:  code.HostName,
		GuestName: code.GuestName,
		Mode:      code.Mode,
		ExpiresAt: code.ExpiresAt.UTC().Format(time.RFC3339),
		Valid:     true,
	}
	if r, gerr := svc.Get(code.RoomID); gerr == nil {
		info.HostedHere = r.IsHost()
		info.JoinedHere = !r.IsHost()
	}
	if info.Kind == "" {
		// Codes from before kinds existed: a code for a room this machine
		// hosts can only be a reply.
		info.Kind = pairing.KindInvite
		if info.HostedHere {
			info.Kind = pairing.KindReply
		}
	}
	if verr := code.Verify(); verr != nil {
		info.Valid, info.Problem = false, verr.Error()
	} else if verr := code.Validate(time.Now()); verr != nil {
		info.Valid, info.Problem = false, verr.Error()
	}
	return info, nil
}

func (s *Server) handleChatSend(_ context.Context, params json.RawMessage) (any, error) {
	svc, err := s.requireRooms()
	if err != nil {
		return nil, err
	}
	var req protocol.ChatSendRequest
	if err := decode(params, &req); err != nil {
		return nil, err
	}
	if req.RoomID == "" {
		return nil, protocol.NewError(protocol.CodeBadRequest, "api: chat.send needs a room id")
	}
	return svc.SendChat(req.RoomID, req.Text)
}

func (s *Server) handleVoiceSignal(_ context.Context, params json.RawMessage) (any, error) {
	svc, err := s.requireRooms()
	if err != nil {
		return nil, err
	}
	var req protocol.VoiceSignal
	if err := decode(params, &req); err != nil {
		return nil, err
	}
	if req.RoomID == "" {
		return nil, protocol.NewError(protocol.CodeBadRequest, "api: voice.signal needs a room id")
	}
	return struct{}{}, svc.SendVoice(req.RoomID, string(req.To), req.Data)
}

func (s *Server) handleChatHistory(_ context.Context, params json.RawMessage) (any, error) {
	svc, err := s.requireRooms()
	if err != nil {
		return nil, err
	}
	var req struct {
		RoomID string `json:"room_id"`
	}
	if err := decode(params, &req); err != nil {
		return nil, err
	}
	if req.RoomID == "" {
		return nil, protocol.NewError(protocol.CodeBadRequest, "api: chat.history needs a room id")
	}
	return svc.ChatHistory(req.RoomID)
}

// GameService backs the game methods.
type GameService interface {
	List() []protocol.GameInfo
	Detect() []protocol.Presence
}

// SetGameService registers game.list and game.detect.
func (s *Server) SetGameService(svc GameService) error {
	if svc == nil {
		return protocol.NewError(protocol.CodeConfigInvalid, "api: game service must not be nil")
	}
	if err := s.Register(protocol.MethodGameList, func(context.Context, json.RawMessage) (any, error) {
		return svc.List(), nil
	}); err != nil {
		return err
	}
	return s.Register(protocol.MethodGameDetect, func(context.Context, json.RawMessage) (any, error) {
		out := svc.Detect()
		if out == nil {
			out = []protocol.Presence{}
		}
		return out, nil
	})
}

// SetCapabilities registers network.capabilities.
func (s *Server) SetCapabilities(fn func() protocol.Capabilities) error {
	return s.Register(protocol.MethodCapabilities, func(context.Context, json.RawMessage) (any, error) {
		return fn(), nil
	})
}

// SetDiagnose registers network.diagnose.
func (s *Server) SetDiagnose(fn func(context.Context) protocol.DiagnoseReport) error {
	return s.Register(protocol.MethodNetworkDiagnose, func(ctx context.Context, _ json.RawMessage) (any, error) {
		return fn(ctx), nil
	})
}
