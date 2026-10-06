package protocol

// Method names for the local control API.
//
// Every method is declared centrally, including the ones that only become
// implementable in later phases. The UI and the CLI are typed against this list
// so that a rename can never drift between the two clients.
const (
	// Handshake (Phase 0).
	MethodHello = "hello"

	// Room (Phase 1+).
	MethodRoomCreate = "room.create"
	MethodRoomJoin   = "room.join"
	MethodRoomAccept = "room.accept"
	MethodRoomLeave  = "room.leave"
	MethodRoomClose  = "room.close"
	// MethodRoomSetMode switches a hosted room between standard and classic LAN
	// while it stays open.
	MethodRoomSetMode           = "room.set_mode"
	MethodRoomGet               = "room.get"
	MethodRoomList              = "room.list"
	MethodRoomRegeneratePairing = "room.regenerate_pairing"

	// Peer (Phase 1+).
	MethodPeerList = "peer.list"
	MethodPeerGet  = "peer.get"
	MethodPeerKick = "peer.kick"
	MethodPeerPing = "peer.ping"

	// Virtual network (Phase 2+).
	MethodNetworkStatus    = "network.status"
	MethodNetworkInterface = "network.interface"
	MethodNetworkRoutes    = "network.routes"
	MethodCapabilities     = "network.capabilities"
	MethodNetworkDiagnose  = "network.diagnose"

	// Game (Phase 5+).
	MethodGameList       = "game.list"
	MethodGameDetect     = "game.detect"
	MethodGameLaunch     = "game.launch"
	MethodGameProfileGet = "game.profile.get"

	// Chat.
	MethodChatSend    = "chat.send"
	MethodVoiceSignal = "voice.signal"
	MethodChatHistory = "chat.history"

	// Pairing helpers.
	MethodPairingInspect = "pairing.inspect"

	// Settings.
	MethodSettingsGet = "settings.get"
	MethodSettingsSet = "settings.set"

	// Daemon (Phase 0).
	MethodDaemonStatus   = "daemon.status"
	MethodDaemonVersion  = "daemon.version"
	MethodDaemonShutdown = "daemon.shutdown"
)

// Event names pushed by the daemon to connected local clients.
const (
	EventDaemonState    = "daemon.state"
	EventDaemonStopping = "daemon.stopping"
	EventDaemonLog      = "daemon.log"

	// Reserved for later phases; declared for a single source of truth.
	EventRoomCreated = "room.created"
	EventRoomClosed  = "room.closed"
	// EventRoomUpdated carries a RoomEvent when a room changed in place
	// (its network type, for one).
	EventRoomUpdated    = "room.updated"
	EventPeerJoined     = "peer.joined"
	EventPeerLeft       = "peer.left"
	EventPeerState      = "peer.state"
	EventPeerStats      = "peer.stats"
	EventPeerConnected  = "peer.connected"
	EventNetworkChanged = "network.changed"
	EventGameDetected   = "game.detected"
	EventChatMessage    = "chat.message"
	EventVoiceSignal    = "voice.signal"
	EventPeerPresence   = "peer.presence"
)

// Methods returns every declared method name. Used by tests and docs.
func Methods() []string {
	return []string{
		MethodHello,
		MethodRoomCreate, MethodRoomJoin, MethodRoomAccept, MethodRoomLeave,
		MethodRoomClose, MethodRoomGet, MethodRoomList, MethodRoomRegeneratePairing,
		MethodPeerList, MethodPeerGet, MethodPeerKick, MethodPeerPing,
		MethodNetworkStatus, MethodNetworkInterface, MethodNetworkRoutes, MethodNetworkDiagnose,
		MethodGameList, MethodGameDetect, MethodGameLaunch, MethodGameProfileGet,
		MethodSettingsGet, MethodSettingsSet, MethodPairingInspect,
		MethodChatSend, MethodChatHistory, MethodVoiceSignal, MethodRoomSetMode,
		MethodFileOffer, MethodFileRespond, MethodFileCancel, MethodFileList, MethodDiagBundle, MethodGameFirewall, MethodGameFirewallFix, MethodGameApply,
		MethodDaemonStatus, MethodDaemonVersion, MethodDaemonShutdown,
		MethodFriendsList, MethodFriendsAdd, MethodFriendsRespond, MethodFriendsRemove,
		MethodFriendsTrust, MethodJoinRequest, MethodJoinInvite, MethodJoinRespond,
		MethodNetworkKeep, MethodNetworkKept,
	}
}

// Events returns every declared event name. Used by tests and docs.
func Events() []string {
	return []string{
		EventRoomUpdated, EventDaemonState, EventDaemonStopping, EventDaemonLog,
		EventRoomCreated, EventRoomClosed,
		EventPeerJoined, EventPeerLeft, EventPeerState, EventPeerStats, EventPeerConnected,
		EventNetworkChanged, EventGameDetected, EventChatMessage, EventVoiceSignal, EventFileUpdate, EventGameFirewall, EventPeerPresence,
		EventFriendUpdate, EventJoinPrompt, EventJoinStatus,
	}
}
