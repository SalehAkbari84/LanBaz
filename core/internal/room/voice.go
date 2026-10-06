package room

import (
	"encoding/json"
	"sync"

	"github.com/lanbaz/lanbaz/core/internal/peer"
	"github.com/lanbaz/lanbaz/core/pkg/protocol"
)

// Signalling for features that move their data outside the control channel.
//
// Voice: the audio never touches the daemon. The two webviews open a WebRTC
// connection to each other over the room's own addresses (10.200.x), so it
// rides the tunnel LanBaz already built - through NAT, the VPN bypass and the
// relay - with no STUN or server of its own.
//
// Files: the bytes go over TCP between the two daemons, again on the room's
// addresses. Only offers, answers and candidates travel here, addressed to one
// player or announced to everybody.

const (
	// SignalVoice and SignalFile are the signalling kinds a room carries.
	SignalVoice = "voice"
	SignalFile  = "file"
	// maxSignal bounds one signalling message; an SDP is a few KB.
	maxSignal = 32 << 10
)

type signalBody struct {
	Data json.RawMessage `json:"d"`
}

type signalState struct {
	mu   sync.Mutex
	sink func(kind string, s protocol.VoiceSignal)
}

func (r *Room) installSignals() {
	for _, kind := range []string{SignalVoice, SignalFile} {
		r.onAppKind(kind, func(_ *peer.Peer, msg peer.AppMessage) { r.receiveSignal(kind, msg) })
	}
}

// SetSignalHandler installs the sink for incoming signalling of every kind.
func (r *Room) SetSignalHandler(fn func(kind string, s protocol.VoiceSignal)) {
	r.signals.mu.Lock()
	r.signals.sink = fn
	r.signals.mu.Unlock()
}

// SendSignal sends a signalling message of one kind to one player, or to all
// when to is "".
func (r *Room) SendSignal(kind, to string, data json.RawMessage) error {
	if kind != SignalVoice && kind != SignalFile {
		return protocol.NewErrorf(protocol.CodeBadRequest, "room: unknown signal kind %q", kind)
	}
	if len(data) == 0 || len(data) > maxSignal || !json.Valid(data) {
		return protocol.NewError(protocol.CodeBadRequest, "room: a signal must be JSON under 32 KB")
	}
	return r.broadcastApp(kind, signalBody{Data: data}, to)
}

// SendVoice is SendSignal for voice.
func (r *Room) SendVoice(to string, data json.RawMessage) error {
	return r.SendSignal(SignalVoice, to, data)
}

func (r *Room) receiveSignal(kind string, msg peer.AppMessage) {
	var b signalBody
	if len(msg.Body) > maxSignal+64 || json.Unmarshal(msg.Body, &b) != nil || len(b.Data) == 0 {
		return
	}
	r.signals.mu.Lock()
	fn := r.signals.sink
	r.signals.mu.Unlock()
	if fn != nil {
		fn(kind, protocol.VoiceSignal{RoomID: r.id, From: protocol.PeerID(msg.Origin), To: protocol.PeerID(msg.To), Data: b.Data})
	}
}
