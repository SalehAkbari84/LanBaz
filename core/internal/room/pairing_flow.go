package room

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"time"

	"github.com/lanbaz/lanbaz/core/internal/pairing"
	"github.com/lanbaz/lanbaz/core/internal/peer"
	"github.com/lanbaz/lanbaz/core/internal/transport"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// joinAlphabet renders generated room ids and guest transport ids. It reuses the
// pairing alphabet so an id copied out of a log line cannot be confused with a
// pairing code character for character.
var joinAlphabet = base32.NewEncoding("0123456789abcdefghjkmnpqrstvwxyz").WithPadding(base32.NoPadding)

// JoinPrefix is the room id scheme. It is a prefix rather than a bare id so that
// a room id pasted into a chat is recognisable, and so an id can never be
// mistaken for a pairing code.
const JoinPrefix = "lbzroom-"

// newRoomID mints a room id.
func newRoomID() (string, error) {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", protocol.NewErrorf(protocol.CodeInternal, "room: generate id: %v", err)
	}
	return JoinPrefix + joinAlphabet.EncodeToString(raw), nil
}

// newGuestPeerID mints the transport id a guest's link will be created under.
//
// It has to be minted before the answer arrives, because the guest's offer and
// its answer must name the same peer on both sides: the host's link is keyed by
// the guest's id, so the id travels inside both pairing codes.
func newGuestPeerID() (transport.PeerID, error) {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", protocol.NewErrorf(protocol.CodeInternal, "room: generate guest id: %v", err)
	}
	return transport.PeerID(joinAlphabet.EncodeToString(raw)), nil
}

// IssuePairing mints or re-mints a pairing code.
//
// It produces a fresh SDP offer every time. That is necessary rather than
// merely tidy: the offer contains fresh ICE credentials and a fresh DTLS
// fingerprint, so replaying a previous offer would offer a peer a link that is
// already compromised to anyone who saw the first code.
func (r *Room) IssuePairing(ctx context.Context, ttl time.Duration) (protocol.PairingResponse, error) {
	return r.IssuePairingFor(ctx, ttl, "")
}

// IssuePairingFor issues a code for a known player (their LanBaz identity), so
// the address it carries is that player's fixed one.
func (r *Room) IssuePairingFor(ctx context.Context, ttl time.Duration, identity string) (protocol.PairingResponse, error) {
	if !r.isHost {
		return protocol.PairingResponse{}, protocol.NewError(protocol.CodePeerRejected,
			"room: only the host issues pairing codes")
	}
	if ttl <= 0 {
		ttl = DefaultPairingTTL
	}
	if ttl > MaxTTL {
		ttl = MaxTTL
	}
	if err := r.makeRoomForCode(); err != nil {
		return protocol.PairingResponse{}, err
	}
	// Every code carries its own secret. Codes handed to different friends are
	// then independent: a host can issue one per guest without waiting for the
	// previous guest to answer, and an answer is only good for the code it
	// actually answers.
	secret, _, err := pairing.NewSecret()
	if err != nil {
		return protocol.PairingResponse{}, err
	}
	expiresAt := r.now().Add(ttl).UTC().Truncate(time.Second)

	sig, ok := r.tr.(transport.Signalling)
	if !ok || !supportsSignalling(r.tr) {
		return protocol.PairingResponse{}, protocol.NewErrorf(protocol.CodeUnsupportedVersion,
			"room: transport %q cannot bootstrap a link without a server", r.tr.Name())
	}

	// The guest's transport id is fixed at issue time so that the host can key
	// the link it will create when the answer arrives. Without this the answer
	// would have to carry the id, and the host would have to trust a field in a
	// blob it is applying - one more thing an attacker gets to choose.
	guestID, err := newGuestPeerID()
	if err != nil {
		return protocol.PairingResponse{}, err
	}
	// The guest's address is allocated at the same moment, for the same reason:
	// the host is the only participant that knows the full membership, so it is
	// the only one that can promise an address is free. Naming it in the code
	// means the guest knows its own address the moment it decodes - no extra
	// round trip in a handshake that already needs a human to carry two blobs.
	guestAddr, err := r.allocateGuestAddr(string(guestID), identity)
	if err != nil {
		return protocol.PairingResponse{}, err
	}
	pending := &pendingJoin{
		peerID:    guestID,
		secret:    secret,
		expiresAt: expiresAt,
		addr:      guestAddr,
		joinedAt:  r.now(),
	}
	r.addPending(pending)

	info := transport.PeerInfo{ID: guestID, RoomID: r.id}
	offer, err := sig.CreateOffer(ctx, info)
	if err != nil {
		r.dropPending(guestID)
		return protocol.PairingResponse{}, err
	}

	local := r.pm.LocalSummary(false)
	code := pairing.Code{
		Version:         pairing.CodeFormatVersion,
		RoomID:          r.id,
		HostID:          string(r.owner),
		HostPublicKey:   local.PublicKey,
		HostFingerprint: local.Fingerprint,
		// The reserved id travels in the offer so the guest can echo it into
		// the answer without inventing one of its own. A guest-chosen id would
		// be a field the host has to trust from an unauthenticated blob.
		GuestID: string(guestID),
		// The addressing travels in the same code, signed by the same secret.
		// The guest verifies the signature before it reads any of it, so these
		// fields are as trustworthy as the host's key rather than merely as
		// trustworthy as whoever handed the code over.
		Subnet:    r.subnetString(),
		GuestAddr: guestAddr.String(),
		Secret:    "",
		Signal:    base64.RawURLEncoding.EncodeToString(offer.Data),
		ExpiresAt: expiresAt,
		Kind:      pairing.KindInvite,
		RoomName:  r.name,
		HostName:  local.DisplayName,
	}
	if r.Mode() == protocol.RoomModeL2 {
		code.Mode = protocol.RoomModeL2
	}
	code.Secret = secret
	nonce, err := pairing.NewNonce()
	if err != nil {
		r.dropPending(guestID)
		return protocol.PairingResponse{}, err
	}
	code.Nonce = nonce
	if err := code.Sign(); err != nil {
		r.dropPending(guestID)
		return protocol.PairingResponse{}, err
	}
	encoded, err := code.Encode()
	if err != nil {
		r.dropPending(guestID)
		return protocol.PairingResponse{}, err
	}
	r.setSecret(secret)

	r.mu.Lock()
	r.pairing = &issued{code: encoded, uri: pairingURI(encoded), expiresAt: code.ExpiresAt, guest: guestID}
	r.pairingSpent = false
	r.mu.Unlock()

	r.log.Info("pairing code issued",
		"expires_at", code.ExpiresAt.UTC().Format(time.RFC3339), "guest", string(guestID))
	return protocol.PairingResponse{
		PairingCode: encoded,
		PairingURI:  pairingURI(encoded),
		ExpiresAt:   code.ExpiresAt,
	}, nil
}

// pairingURI renders the clickable form of a code.
func pairingURI(code string) string { return "lanbaz://join/" + code }

// Join applies a host's pairing code and produces the answer code.
//
// It does not connect the guest to anybody. With no server in the loop the
// answer has to travel back to the host by hand, so the response is explicit
// about that: Pending is true and AnswerCode must be shown to the user.
func (r *Room) Join(ctx context.Context, codeStr, displayName string) (protocol.RoomJoinResponse, error) {
	if r.isHost {
		return protocol.RoomJoinResponse{}, protocol.NewErrorf(protocol.CodePeerRejected,
			"room: this daemon hosts %s; use room.regenerate_pairing instead", r.id)
	}
	code, err := pairing.Decode(codeStr)
	if err != nil {
		return protocol.RoomJoinResponse{}, err
	}
	// Verify before Validate, and Validate before reading anything else. An
	// expiry check on an unauthenticated code would let an attacker extend
	// ExpiresAt freely.
	if err := code.Verify(); err != nil {
		return protocol.RoomJoinResponse{}, err
	}
	if err := code.Validate(r.now()); err != nil {
		return protocol.RoomJoinResponse{}, err
	}
	offer, err := decodeSignal(code.Signal)
	if err != nil {
		return protocol.RoomJoinResponse{}, err
	}
	// Decoding the secret proves its length and encoding before it is adopted as
	// the room's, so a malformed code fails here rather than at the first hello.
	if _, err := code.SecretBytes(); err != nil {
		return protocol.RoomJoinResponse{}, err
	}

	// The addressing is read only now, after the signature has been verified.
	// Reading it earlier would mean trusting a field an attacker could have
	// written into a code they observed, and that field decides which address
	// this machine configures for itself.
	if subnet, guestAddr, ok := code.Addressing(); ok {
		if err := r.adoptAddressing(subnet, guestAddr); err != nil {
			return protocol.RoomJoinResponse{}, err
		}
	}

	r.mu.Lock()
	if code.RoomName != "" {
		r.name = cleanName(code.RoomName)
	}
	// The host decides the room's mode; every member must match it, or one
	// side would send Ethernet frames the other reads as IP packets.
	r.mode = normMode(code.Mode)
	r.mu.Unlock()

	r.mu.Lock()
	if r.secret != "" && r.secret != code.Secret {
		r.mu.Unlock()
		return protocol.RoomJoinResponse{}, protocol.NewError(protocol.CodePairingReplay,
			"room: this pairing code is not the one this room was joined with")
	}
	r.secret = code.Secret
	r.mu.Unlock()

	// From the guest's side the peer it must negotiate with is the host, so its
	// link is keyed by the host's id.
	info := transport.PeerInfo{ID: transport.PeerID(code.HostID), RoomID: code.RoomID}

	sig, ok := r.tr.(transport.Signalling)
	if !ok || !supportsSignalling(r.tr) {
		return protocol.RoomJoinResponse{}, protocol.NewErrorf(protocol.CodeUnsupportedVersion,
			"room: transport %q cannot bootstrap a link without a server", r.tr.Name())
	}
	answer, err := sig.AcceptOffer(ctx, info, transport.Description{
		Kind: transport.DescriptionOffer,
		Data: offer,
	})
	if err != nil {
		return protocol.RoomJoinResponse{}, err
	}
	// The host is registered as a peer now, while the link still exists but is
	// not yet usable. Registering it later would miss the channel-opened
	// transition entirely, and the peer would sit unidentified forever because
	// the greeting is sent in response to that very event.
	if _, err := r.pm.Add(info.ID, true, false); err != nil {
		return protocol.RoomJoinResponse{}, err
	}
	// The code names the host's identity and is signed with the code's secret.
	// The host's hello must match it, or something other than the host is on
	// the far end of this link.
	r.pm.ExpectIdentity(info.ID, protocol.PeerID(code.HostID))

	// The reply reuses the offer's secret and echoes the offer's guest id. That
	// is what tells the host the answer came from somebody who actually saw the
	// offer, and it lets the host apply the answer to a link it created itself.
	reply := pairing.Code{
		Version:         pairing.CodeFormatVersion,
		RoomID:          code.RoomID,
		HostID:          code.HostID,
		HostPublicKey:   code.HostPublicKey,
		HostFingerprint: code.HostFingerprint,
		GuestID:         code.GuestID,
		// The addressing is echoed unchanged. The host allocated it and put it in
		// the code, so there is nothing to negotiate; echoing it is what lets the
		// host check that the guest understood the same LAN it was offered.
		Subnet:    code.Subnet,
		GuestAddr: code.GuestAddr,
		Secret:    code.Secret,
		Signal:    base64.RawURLEncoding.EncodeToString(answer.Data),
		ExpiresAt: code.ExpiresAt,
		Kind:      pairing.KindReply,
		RoomName:  code.RoomName,
		HostName:  code.HostName,
		GuestName: r.pm.LocalSummary(false).DisplayName,
		Mode:      code.Mode,
	}
	nonce, err := pairing.NewNonce()
	if err != nil {
		return protocol.RoomJoinResponse{}, err
	}
	reply.Nonce = nonce
	if err := reply.Sign(); err != nil {
		return protocol.RoomJoinResponse{}, err
	}
	encodedReply, err := reply.Encode()
	if err != nil {
		return protocol.RoomJoinResponse{}, err
	}

	return protocol.RoomJoinResponse{
		Room:       r.Summary(),
		AnswerCode: encodedReply,
		Pending:    true,
		LocalPeer:  r.pm.LocalSummary(false),
	}, nil
}

// Accept completes the handshake by applying the guest's answer.
//
// This is the host's side of the second half of the conversation. The guest id is
// taken from the reply when the request does not pin one, so a client that knows
// nothing about the answer can still complete a join.
func (r *Room) Accept(ctx context.Context, req protocol.RoomAcceptRequest) error {
	if !r.isHost {
		return protocol.NewError(protocol.CodePeerRejected,
			"room: only the host accepts a join")
	}
	code, err := pairing.Decode(req.AnswerCode)
	if err != nil {
		return err
	}
	if err := code.Verify(); err != nil {
		return err
	}
	if err := code.Validate(r.now()); err != nil {
		return err
	}
	if code.RoomID != r.id {
		return protocol.NewErrorf(protocol.CodePairingInvalid,
			"room: the answer belongs to room %s, not %s", code.RoomID, r.id)
	}
	// The answer echoes the guest id the host reserved, so a client that knows
	// nothing about the guest can still complete the join. An explicitly pinned
	// id must match it, which is what stops a client from applying an answer to
	// a link that belongs to somebody else's join.
	reserved := transport.PeerID(code.GuestID)
	if reserved == "" {
		return protocol.NewError(protocol.CodePairingInvalid,
			"room: the answer does not name the guest it belongs to")
	}
	if req.GuestPeerID != "" && transport.PeerID(req.GuestPeerID) != reserved {
		return protocol.NewErrorf(protocol.CodePairingInvalid,
			"room: the request pins guest %s but the answer belongs to %s",
			req.GuestPeerID, code.GuestID)
	}
	pending := r.takePending(reserved)
	if pending == nil {
		return protocol.NewErrorf(protocol.CodePairingInvalid,
			"room: this answer is for a code that was already used or has expired; send your friend a new code")
	}
	link := pending.peerID
	// The answer must carry the secret of the code it answers. The code's own
	// signature only proves it was not altered after signing; the secret match
	// is what proves its author saw the offer this host actually sent.
	if subtle.ConstantTimeCompare([]byte(code.Secret), []byte(pending.secret)) != 1 {
		r.addPending(pending)
		return protocol.NewError(protocol.CodePairingInvalid,
			"room: the answer does not belong to the code issued for this guest")
	}

	// The answer must echo the addressing the host issued. It is signed, so a
	// guest cannot have chosen its own, and checking it costs nothing - but the
	// check is what stops two daemons from configuring themselves into two
	// different LANs and then wondering why nobody can be found.
	if code.Subnet != "" && code.GuestAddr != "" {
		if code.Subnet != r.subnetString() {
			r.addPending(pending)
			return protocol.NewErrorf(protocol.CodePairingInvalid,
				"room: the answer names subnet %s but this room uses %s",
				code.Subnet, r.subnetString())
		}
		want := pending.addr.String()
		if code.GuestAddr != want {
			r.addPending(pending)
			return protocol.NewErrorf(protocol.CodePairingInvalid,
				"room: the answer claims address %s but %s was reserved for %s",
				code.GuestAddr, want, reserved)
		}
	}

	answer, err := decodeSignal(code.Signal)
	if err != nil {
		return err
	}
	sig, ok := r.tr.(transport.Signalling)
	if !ok {
		return protocol.NewErrorf(protocol.CodeUnsupportedVersion,
			"room: transport %q cannot bootstrap a link without a server", r.tr.Name())
	}
	info := transport.PeerInfo{ID: link, RoomID: r.id}
	// The peer record is created before the answer is applied, not after. The
	// link exists already - CreateOffer made it - and its channel-opened
	// transition fires inside ApplyAnswer, so a record added afterwards would
	// miss the one event that triggers the greeting.
	if _, err := r.pm.Add(link, false, false); err != nil {
		r.addPending(pending)
		return err
	}
	r.pm.ExpectIdentity(link, req.ExpectPeer)
	err = sig.ApplyAnswer(ctx, info, transport.Description{
		Kind: transport.DescriptionAnswer,
		Data: answer,
	})
	if err != nil && protocol.CodeOf(err) == protocol.CodeTransportTimeout {
		// The answer is applied and ICE is still trying paths: hole punching
		// through two home routers, or the fall back to a relay, can take
		// longer than the wait above. Tearing the link down here is what
		// failed every field test at exactly 30 s. Keep it; the link's own
		// watchdog gives up if no path is ever found.
		r.log.Info("still connecting to the guest; paths are being tried", "guest", string(link))
		err = nil
	}
	if err != nil {
		// A failed answer leaves the offer's PeerConnection with a remote
		// description it cannot take back, so the same answer can never be
		// applied again. Everything for this guest is torn down instead - the
		// peer record, the link and the address - and the host issues a fresh
		// code. Keeping the half-applied link would make every retry fail with
		// a confusing "wrong state" error.
		r.pm.Remove(link)
		_ = r.tr.Close(link)
		r.releaseGuest(link, pending.addr)
		r.log.Warn("could not complete a join", "guest", string(link), "error", err)
		return protocol.NewErrorf(protocol.CodeOf(err),
			"room: the connection to your friend could not be established (%v). "+
				"Create a new code and try again; if it keeps failing, one of you may be behind a strict NAT "+
				"and needs a TURN relay (Settings)", err)
	}
	// The guest is a real peer now, so its reserved address becomes its lease.
	r.bindPeer(link, pending.addr)

	// The code is spent the moment the link exists. Marking it here rather than
	// at DTLS completion is deliberate: the alternative needs a second, separate
	// replay store with its own lifetime rules, and the window it would leave open
	// buys nothing, because applying a second answer to an already established
	// link cannot improve anything.
	r.mu.Lock()
	if r.pairing != nil && r.pairing.guest == link {
		r.pairingSpent = true
	}
	r.mu.Unlock()

	r.log.Info("peer joined", "peer", peerLabel(protocol.PeerID(link)), "room", r.id)
	return nil
}

// decodeSignal decodes the base64url signalling blob carried by a pairing code.
func decodeSignal(s string) ([]byte, error) {
	if s == "" {
		return nil, protocol.NewError(protocol.CodePairingInvalid,
			"room: the pairing code carries no signalling data")
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, protocol.NewError(protocol.CodePairingInvalid,
			"room: the signalling data in the pairing code is malformed")
	}
	return raw, nil
}

// supportsSignalling reports whether tr can bootstrap a link out of band.
func supportsSignalling(tr transport.Transport) bool {
	c, ok := tr.(transport.SignallingCapable)
	return ok && c.SupportsSignalling()
} // CreateRoom builds a room together with its transport and peer manager.
// It is a constructor rather than a factory method on the manager because the
// transport implementation is chosen through transport.Factory, and that choice
// belongs to the daemon's wiring, not to the room.
func CreateRoom(ctx context.Context, factory transport.Factory, idFactory func() (string, error), opts Options) (*Room, error) {
	if factory == nil {
		return nil, protocol.NewError(protocol.CodeConfigInvalid,
			"room: a transport factory is required")
	}
	if idFactory == nil {
		idFactory = newRoomID
	}
	roomID := opts.RoomID
	if roomID == "" {
		generated, err := idFactory()
		if err != nil {
			return nil, err
		}
		roomID = generated
	}
	opts.RoomID = roomID

	trCfg := transport.Config{
		RoomID:      roomID,
		PrivateKey:  opts.OwnerKey,
		STUNServers: opts.STUNServers,
		TURNServers: opts.TURNServers,
		MTU:         opts.MTU,
		AllowRelay:  opts.AllowRelay,
		RelayOnly:   opts.RelayOnly,
		PortMin:     opts.PortMin,
		PortMax:     opts.PortMax,
		Logger:      opts.Logger,
	}
	tr, err := factory.New(ctx, trCfg)
	if err != nil {
		return nil, err
	}
	if !supportsSignalling(tr) {
		_ = tr.Shutdown(ctx)
		return nil, protocol.NewErrorf(protocol.CodeUnsupportedVersion,
			"room: transport %q cannot bootstrap a link without a server", tr.Name())
	}

	// The peer manager emits peer.Event, which carries the event name; the room
	// and the daemon only ever deal in protocol payloads, so the unwrapping
	// happens here rather than leaking the peer package's event type upward.
	//
	// The handler is deliberately installed *after* the room is constructed, via
	// SetEventHandler. Building it as a closure over a room that does not exist
	// yet would be a data race the first time a Pion callback fired during
	// construction, and nothing may have arrived at that point anyway: no link
	// exists until the first offer.
	pm, err := peer.NewManager(peer.Options{
		RoomID:      roomID,
		LocalID:     opts.Owner,
		LocalPub:    opts.OwnerKey[publicKeyOffset:],
		DisplayName: opts.DisplayName,
		Transport:   tr,
		Logger:      opts.Logger,
	})
	if err != nil {
		_ = tr.Shutdown(ctx)
		return nil, err
	}
	opts.Transport = tr
	opts.Peers = pm
	r, err := Create(opts)
	if err != nil {
		_ = pm.Close(ctx)
		_ = tr.Shutdown(ctx)
		return nil, err
	}
	forward := opts.OnEvent
	pm.SetEventHandler(func(ev peer.Event) {
		r.observePeer(ev)
		if forward != nil {
			body := ev.Body
			// The peer manager does not know the virtual LAN, so the address
			// is stamped in here - otherwise a peer that joins shows "no
			// address" in the UI until something else triggers a reload.
			if body.Peer != nil && body.TransportPeerID != "" {
				if addr, ok := r.Lease(transport.PeerID(body.TransportPeerID)); ok {
					cp := *body.Peer
					cp.VirtualAddress = addr.String()
					body.Peer = &cp
				}
			}
			forward(Notice{Kind: ev.Kind, Peer: &body})
		}
	})
	return r, nil
}

// publicKeyOffset is where the public half begins in a 64-byte Ed25519 private
// key. It is stated rather than computed from an unexported constant of the
// crypto package, and the peer manager rejects any key of the wrong size, so a
// wrong value here fails loudly at the first hello rather than silently.
const publicKeyOffset = 32

// ErrNotFound is returned for a room the manager does not hold.
var ErrNotFound = errors.New("room: not found")
