package daemon

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/lanbaz/lanbaz/core/internal/pairing"
	"github.com/lanbaz/lanbaz/core/internal/room"
	"github.com/lanbaz/lanbaz/core/internal/social"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// Friend joins: the invite and reply codes that people used to copy by hand
// travel as encrypted friend messages instead, and the daemon applies them
// itself. Nothing here touches game traffic.

const (
	// askedFor is how long a join request we sent makes the friend's invite
	// acceptable without asking again.
	askedFor = 3 * time.Minute
	// reconnectFor is how long a dropped guest keeps asking to rejoin.
	reconnectFor = 2 * time.Minute
	// inviteTTL is the lifetime of an invite sent to a friend.
	inviteTTL = 10 * time.Minute
)

type prompt struct {
	friend   social.Friend
	kind     string // "request" or "invite"
	room     string
	roomName string
	code     string // the invite, for an invite prompt
	at       time.Time
}

// friends bridges the social service and the room manager.
type friends struct {
	d   *Daemon
	svc *social.Service

	mu       sync.Mutex
	prompts  map[string]prompt          // pending user decisions
	asked    map[string]time.Time       // friend pub -> when we asked to join
	invited  map[string]string          // friend pub -> room we sent an invite for
	accepted map[string]map[string]bool // room -> friend pubs accepted into it
	hostOf   map[string]string          // guest room -> host's friend pub
	game     string
	keep     *keeper
}

func (d *Daemon) initFriends(bus social.Bus) error {
	keys, err := social.LoadOrCreateKeys(d.cfg.StateDir)
	if err != nil {
		return err
	}
	store, err := social.OpenStore(d.cfg.StateDir)
	if err != nil {
		return err
	}
	f := &friends{
		d: d, prompts: map[string]prompt{}, asked: map[string]time.Time{},
		invited: map[string]string{}, accepted: map[string]map[string]bool{}, hostOf: map[string]string{},
		keep: newKeeper(d.cfg.StateDir),
	}
	svc, err := social.New(keys, bus, store, social.Hooks{
		Profile:  f.profile,
		Presence: f.presence,
		Changed:  f.changed,
		Join:     f.onJoin,
	}, d.log)
	if err != nil {
		return err
	}
	f.svc = svc
	d.friends = f
	return f.register()
}

func (f *friends) profile() social.Profile {
	name := f.d.settings.Get().DisplayName
	if name == "" {
		name = hostname()
	}
	if len([]rune(name)) > 40 {
		name = string([]rune(name)[:40])
	}
	var ed string
	if len(f.d.idKey) == 64 {
		ed = base64.RawURLEncoding.EncodeToString(f.d.idKey[32:])
	}
	return social.Profile{Name: name, PeerID: string(f.d.id), EdKey: ed}
}

// presence reports the room we host (if any) and the game we play.
func (f *friends) presence(m *social.Message) {
	if rooms := f.d.Rooms(); rooms != nil {
		for _, r := range rooms.All() {
			if !r.IsHost() {
				continue
			}
			s := r.Summary()
			m.Hosting, m.Room, m.RoomName = true, s.RoomID, s.Name
			if s.MaxPeers > s.PeerCount {
				m.Seats = s.MaxPeers - s.PeerCount
			}
			break
		}
	}
	f.mu.Lock()
	m.Game = f.game
	f.mu.Unlock()
}

func (f *friends) setGame(name string) {
	f.mu.Lock()
	changed := f.game != name
	f.game = name
	f.mu.Unlock()
	if changed {
		f.svc.AnnouncePresence(context.Background())
	}
}

func (f *friends) view(fr social.Friend) protocol.Friend {
	v := protocol.Friend{
		Pub: fr.Pub, Code: fr.Code, Name: fr.Name, PeerID: protocol.PeerID(fr.PeerID),
		State: fr.State, Trusted: fr.Trusted, LastSeen: fr.LastSeen,
	}
	if f.svc.IsOnline(fr) {
		v.Online = true
		p := fr.Presence
		v.Hosting, v.RoomID, v.RoomName, v.Seats, v.Game = p.Hosting, p.Room, p.RoomName, p.Seats, p.Game
	}
	return v
}

func (f *friends) changed(fr social.Friend, why string) {
	f.d.api.PublishEvent(protocol.EventFriendUpdate, protocol.FriendEvent{Friend: f.view(fr), Why: why})
}

func (f *friends) status(fr social.Friend, roomID, status, msg string) {
	f.d.api.PublishEvent(protocol.EventJoinStatus, protocol.JoinStatus{
		Friend: f.view(fr), RoomID: roomID, Status: status, Message: msg,
	})
	f.d.log.Info("friend join", "friend", fr.Name, "room", roomID, "status", status, "detail", msg)
}

func newPromptID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// onJoin handles every join_* message from a confirmed friend.
func (f *friends) onJoin(fr social.Friend, m social.Message) {
	ctx := context.Background()
	switch m.Type {
	case social.TypeJoinRequest:
		f.onRequest(ctx, fr, m)
	case social.TypeJoinInvite:
		f.onInvite(ctx, fr, m)
	case social.TypeJoinAnswer:
		f.onAnswer(ctx, fr, m)
	case social.TypeJoinReject:
		f.mu.Lock()
		delete(f.asked, fr.Pub)
		f.mu.Unlock()
		f.status(fr, m.Room, "rejected", m.Reason)
	}
}

// hostedRoom returns the room id to offer: the requested one if we host it,
// else our only hosted room.
func (f *friends) hostedRoom(want string) (*room.Room, bool) {
	rooms := f.d.Rooms()
	if rooms == nil {
		return nil, false
	}
	if want != "" {
		if r, err := rooms.Get(want); err == nil && r.IsHost() {
			return r, true
		}
	}
	for _, r := range rooms.All() {
		if r.IsHost() {
			return r, true
		}
	}
	return nil, false
}

// Host side: a friend asks to join.
func (f *friends) onRequest(ctx context.Context, fr social.Friend, m social.Message) {
	r, ok := f.hostedRoom(m.Room)
	if !ok {
		_ = f.svc.Send(ctx, fr.Pub, social.Message{Type: social.TypeJoinReject, Room: m.Room, Reason: "not hosting a room right now"})
		return
	}
	f.mu.Lock()
	again := f.accepted[r.ID()][fr.Pub]
	f.mu.Unlock()
	if fr.Trusted || again || f.keep.isMember(r.ID(), fr.Pub) {
		f.sendInvite(ctx, fr, r)
		return
	}
	id := newPromptID()
	s := r.Summary()
	f.mu.Lock()
	f.prompts[id] = prompt{friend: fr, kind: "request", room: s.RoomID, roomName: s.Name, at: time.Now()}
	f.mu.Unlock()
	f.d.api.PublishEvent(protocol.EventJoinPrompt, protocol.JoinPrompt{
		ID: id, Kind: "request", Friend: f.view(fr), RoomID: s.RoomID, RoomName: s.Name,
	})
}

// sendInvite issues a fresh pairing code for this friend and sends it.
func (f *friends) sendInvite(ctx context.Context, fr social.Friend, r *room.Room) {
	rooms := f.d.Rooms()
	resp, err := rooms.RegeneratePairing(ctx, protocol.RoomRegeneratePairingRequest{
		RoomID: r.ID(), TTLSeconds: int(inviteTTL / time.Second),
	})
	if err != nil {
		f.status(fr, r.ID(), "failed", err.Error())
		return
	}
	f.mu.Lock()
	f.invited[fr.Pub] = r.ID()
	if f.accepted[r.ID()] == nil {
		f.accepted[r.ID()] = map[string]bool{}
	}
	f.accepted[r.ID()][fr.Pub] = true
	f.mu.Unlock()
	f.keep.addMember(r.ID(), fr.Pub)
	s := r.Summary()
	if err := f.svc.Send(ctx, fr.Pub, social.Message{
		Type: social.TypeJoinInvite, Room: s.RoomID, RoomName: s.Name, Code: resp.PairingCode,
	}); err != nil {
		f.status(fr, s.RoomID, "failed", err.Error())
		return
	}
	f.status(fr, s.RoomID, "invited", "")
}

// inRoomOf reports whether this PC is in a working room hosted by the friend.
func (f *friends) inRoomOf(pub string) bool {
	rooms := f.d.Rooms()
	if rooms == nil {
		return false
	}
	f.mu.Lock()
	var ids []string
	for roomID, p := range f.hostOf {
		if p == pub {
			ids = append(ids, roomID)
		}
	}
	f.mu.Unlock()
	for _, id := range ids {
		if r, err := rooms.Get(id); err == nil && r.HostAlive() {
			return true
		}
	}
	return false
}

// Guest side: a friend sends an invite (because we asked, or on their own).
func (f *friends) onInvite(ctx context.Context, fr social.Friend, m social.Message) {
	// A second invite for a room we are already in and connected to (two
	// rejoin attempts crossed) is not news.
	if rooms := f.d.Rooms(); rooms != nil {
		if r, err := rooms.Get(m.Room); err == nil && r.HostAlive() {
			return
		}
	}
	code, err := pairing.Decode(m.Code)
	if err != nil || code.Verify() != nil || code.RoomID != m.Room {
		f.status(fr, m.Room, "failed", "the invite was not valid")
		return
	}
	// The invite must come from the friend's own LanBaz identity: their
	// friend message is authenticated, and the code must name the same host.
	if fr.PeerID != "" && code.HostID != fr.PeerID {
		f.status(fr, m.Room, "failed", "the invite names a different host than this friend")
		return
	}
	f.mu.Lock()
	askedAt, asked := f.asked[fr.Pub]
	f.mu.Unlock()
	if (asked && time.Since(askedAt) < askedFor) || fr.Trusted || f.keep.joins(fr.Pub) {
		f.join(ctx, fr, m)
		return
	}
	id := newPromptID()
	f.mu.Lock()
	f.prompts[id] = prompt{friend: fr, kind: "invite", room: m.Room, roomName: m.RoomName, code: m.Code, at: time.Now()}
	f.mu.Unlock()
	f.d.api.PublishEvent(protocol.EventJoinPrompt, protocol.JoinPrompt{
		ID: id, Kind: "invite", Friend: f.view(fr), RoomID: m.Room, RoomName: m.RoomName,
	})
}

// join applies the invite and sends the reply back to the friend.
func (f *friends) join(ctx context.Context, fr social.Friend, m social.Message) {
	f.mu.Lock()
	delete(f.asked, fr.Pub)
	f.mu.Unlock()
	f.status(fr, m.Room, "joining", "")
	resp, err := f.d.Rooms().Join(ctx, protocol.RoomJoinRequest{PairingCode: m.Code, DisplayName: f.profile().Name})
	if err != nil {
		f.status(fr, m.Room, "failed", err.Error())
		return
	}
	f.mu.Lock()
	f.hostOf[m.Room] = fr.Pub
	f.mu.Unlock()
	if err := f.svc.Send(ctx, fr.Pub, social.Message{Type: social.TypeJoinAnswer, Room: m.Room, Code: resp.AnswerCode}); err != nil {
		f.status(fr, m.Room, "failed", err.Error())
	}
}

// Host side: the friend's reply arrives; apply it.
func (f *friends) onAnswer(ctx context.Context, fr social.Friend, m social.Message) {
	f.mu.Lock()
	want := f.invited[fr.Pub]
	if want == m.Room {
		delete(f.invited, fr.Pub)
	}
	f.mu.Unlock()
	if want == "" || want != m.Room {
		return // not an answer to an invite we sent this friend
	}
	err := f.d.Rooms().Accept(ctx, protocol.RoomAcceptRequest{
		RoomID: m.Room, AnswerCode: m.Code, ExpectPeer: protocol.PeerID(fr.PeerID),
	})
	if err != nil {
		f.status(fr, m.Room, "failed", err.Error())
		return
	}
	f.status(fr, m.Room, "joining", "")
	go f.awaitLink(fr, m.Room)
}

// awaitLink reports "connected" only once the friend's link really works, so
// the UI never claims a connection that is still being negotiated. Direct
// paths are tried first; with a relay configured, ICE falls back to it.
func (f *friends) awaitLink(fr social.Friend, roomID string) {
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(time.Second)
		r, err := f.d.Rooms().Get(roomID)
		if err != nil {
			return
		}
		for _, p := range r.Summary().Peers {
			if string(p.PeerID) == fr.PeerID && (p.State == protocol.PeerActive || p.State == protocol.PeerNetworkReady) {
				f.status(fr, roomID, "connected", "")
				f.svc.AnnouncePresence(context.Background())
				return
			}
		}
	}
	f.status(fr, roomID, "failed", "no path between the two PCs worked; see Settings → Developer log, and set up the relay (Settings → Network)")
}

// requestJoin asks a friend to let us into their room.
func (f *friends) requestJoin(ctx context.Context, fr social.Friend, roomID string) error {
	f.mu.Lock()
	f.asked[fr.Pub] = time.Now()
	f.mu.Unlock()
	if err := f.svc.Send(ctx, fr.Pub, social.Message{Type: social.TypeJoinRequest, Room: roomID}); err != nil {
		return err
	}
	f.status(fr, roomID, "sent", "")
	return nil
}

// roomClosed restarts the join when a friend's room drops us without us
// leaving: the host auto-accepts a friend it already accepted into the room.
func (f *friends) roomClosed(roomID, reason string) {
	f.mu.Lock()
	hostPub, ok := f.hostOf[roomID]
	delete(f.hostOf, roomID)
	delete(f.accepted, roomID)
	f.mu.Unlock()
	f.forgetOnLeave(roomID, hostPub, reason)
	if !ok || reason == "left" {
		return
	}
	fr, known := f.svc.Get(hostPub)
	if !known || fr.State != social.StateFriend {
		return
	}
	go func() {
		deadline := time.Now().Add(reconnectFor)
		for wait := 3 * time.Second; time.Now().Before(deadline); wait *= 2 {
			f.status(fr, roomID, "reconnecting", "")
			_ = f.requestJoin(context.Background(), fr, roomID)
			time.Sleep(wait)
			if f.inRoomOf(hostPub) {
				return
			}
		}
	}()
}

// register adds the friend methods to the control API.
func (f *friends) register() error {
	type h = func(context.Context, json.RawMessage) (any, error)
	decode := func(raw json.RawMessage, v any) error {
		if err := json.Unmarshal(raw, v); err != nil {
			return protocol.NewErrorf(protocol.CodeBadRequest, "bad request: %v", err)
		}
		return nil
	}
	friend := func(pub string) (social.Friend, error) {
		fr, ok := f.svc.Get(pub)
		if !ok || fr.State != social.StateFriend {
			return fr, protocol.NewError(protocol.CodeNotFound, "not a friend")
		}
		return fr, nil
	}
	wrap := func(err error) error {
		if err == nil {
			return nil
		}
		var pe *protocol.Error
		if errors.As(err, &pe) {
			return err
		}
		return protocol.NewError(protocol.CodeBadRequest, err.Error())
	}
	methods := map[string]h{
		protocol.MethodFriendsList: func(context.Context, json.RawMessage) (any, error) {
			out := protocol.FriendsList{Code: f.svc.Code(), Enabled: true, Friends: []protocol.Friend{}}
			for _, fr := range f.svc.List() {
				out.Friends = append(out.Friends, f.view(fr))
			}
			return out, nil
		},
		protocol.MethodFriendsAdd: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var req protocol.FriendAddRequest
			if err := decode(raw, &req); err != nil {
				return nil, err
			}
			fr, err := f.svc.Add(ctx, req.Code)
			return f.view(fr), wrap(err)
		},
		protocol.MethodFriendsRespond: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var req protocol.FriendRespondRequest
			if err := decode(raw, &req); err != nil {
				return nil, err
			}
			fr, err := f.svc.Respond(ctx, req.Pub, req.Accept)
			return f.view(fr), wrap(err)
		},
		protocol.MethodFriendsRemove: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var req protocol.FriendKeyRequest
			if err := decode(raw, &req); err != nil {
				return nil, err
			}
			return struct{}{}, wrap(f.svc.Remove(ctx, req.Pub))
		},
		protocol.MethodFriendsTrust: func(_ context.Context, raw json.RawMessage) (any, error) {
			var req protocol.FriendTrustRequest
			if err := decode(raw, &req); err != nil {
				return nil, err
			}
			fr, err := f.svc.SetTrusted(req.Pub, req.Trusted)
			return f.view(fr), wrap(err)
		},
		protocol.MethodJoinRequest: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var req protocol.JoinFriendRequest
			if err := decode(raw, &req); err != nil {
				return nil, err
			}
			fr, err := friend(req.Pub)
			if err != nil {
				return nil, err
			}
			if req.RoomID == "" {
				req.RoomID = fr.Presence.Room
			}
			return struct{}{}, wrap(f.requestJoin(ctx, fr, req.RoomID))
		},
		protocol.MethodJoinInvite: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var req protocol.JoinFriendRequest
			if err := decode(raw, &req); err != nil {
				return nil, err
			}
			fr, err := friend(req.Pub)
			if err != nil {
				return nil, err
			}
			r, ok := f.hostedRoom(req.RoomID)
			if !ok {
				return nil, protocol.NewError(protocol.CodeNotFound, "create a room first, then invite friends into it")
			}
			f.sendInvite(ctx, fr, r)
			return struct{}{}, nil
		},
		protocol.MethodNetworkKeep: func(_ context.Context, raw json.RawMessage) (any, error) {
			var req protocol.KeepRequest
			if err := decode(raw, &req); err != nil {
				return nil, err
			}
			return wrapKept(f.setKeep(req.RoomID, req.Keep))
		},
		protocol.MethodNetworkKept: func(context.Context, json.RawMessage) (any, error) {
			return f.kept(), nil
		},
		protocol.MethodJoinRespond: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var req protocol.JoinRespondRequest
			if err := decode(raw, &req); err != nil {
				return nil, err
			}
			f.mu.Lock()
			p, ok := f.prompts[req.ID]
			delete(f.prompts, req.ID)
			f.mu.Unlock()
			if !ok || time.Since(p.at) > 10*time.Minute {
				return nil, protocol.NewError(protocol.CodeNotFound, "that request has expired")
			}
			if req.Accept && req.Always {
				if fr, err := f.svc.SetTrusted(p.friend.Pub, true); err == nil {
					p.friend = fr
				}
			}
			switch {
			case !req.Accept:
				_ = f.svc.Send(ctx, p.friend.Pub, social.Message{Type: social.TypeJoinReject, Room: p.room, Reason: "declined"})
			case p.kind == "request":
				r, ok := f.hostedRoom(p.room)
				if !ok {
					return nil, protocol.NewError(protocol.CodeNotFound, "that room is closed")
				}
				f.sendInvite(ctx, p.friend, r)
			default:
				go f.join(context.WithoutCancel(ctx), p.friend, social.Message{Room: p.room, Code: p.code})
			}
			return struct{}{}, nil
		},
	}
	for name, fn := range methods {
		if err := f.d.api.Register(name, fn); err != nil {
			return err
		}
	}
	return nil
}

func wrapKept(k protocol.KeptNetworks, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	return k, nil
}
