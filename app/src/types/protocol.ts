/**
 * Types mirroring the Go DTOs in core/pkg/protocol/dto.go.
 *
 * They are hand-written rather than generated in Phase 0; the field names and
 * shapes must stay in sync with the Go definitions. A later phase can replace
 * this file with generated types without changing any consumer.
 */

export type DaemonState = 'starting' | 'running' | 'stopping' | 'stopped'

export interface DaemonStatus {
  state: DaemonState
  version: string
  commit: string
  build_time: string
  go_version: string
  protocol_version: number
  pid: number
  uptime_seconds: number
  started_at: string
  api_listen: string
  api_port: number
  state_dir: string
  log_level: string
  active_clients: number
  room_count: number
  peer_count: number
}

export interface DaemonVersion {
  daemon: string
  version: string
  commit: string
  build_time: string
  go_version: string
  protocol_version: number
  platform: string
  phase: string
}

export interface ShutdownResponse {
  accepted: boolean
  grace_period_ms: number
  stop_deadline: string
}

export interface StateEvent {
  state: DaemonState
  previous: DaemonState
  timestamp: string
}

export interface HelloResponse {
  daemon: string
  version: string
  protocol_version: number
  session_id: string
  server_time: string
}

/**
 * The link lifecycle, in the order a link passes through it.
 *
 * The distinction the UI must preserve: `network_ready` means the transport
 * opened a channel, while `active` means a round trip actually came back. Only
 * the second one is evidence that traffic will flow. `peer.connected` exists as
 * a separate event for exactly this reason.
 */
export type PeerState =
  | 'new'
  | 'discovering'
  | 'signaling'
  | 'ice_checking'
  | 'connected'
  | 'dtls'
  | 'data_channel'
  | 'network_ready'
  | 'active'
  | 'degraded'
  | 'failed'
  | 'disconnected'

/** States from which no further progress is made. */
export function isTerminal(state: PeerState): boolean {
  return state === 'failed' || state === 'disconnected'
}

/** Whether a peer should be drawn as reachable in the UI. */
export function isUsable(state: PeerState): boolean {
  return state === 'active' || state === 'degraded' || state === 'network_ready'
}

export type LinkKind = 'direct' | 'relay' | 'unknown'

export interface PeerSummary {
  /** The announced identity. Empty until the peer's hello arrives. */
  peer_id: string
  /** Stable per-link key, set even before the hello. Use it to key rows. */
  link_id?: string
  display_name?: string
  state: PeerState
  /**
   * The peer's address inside the room subnet, e.g. `10.200.17.2`.
   *
   * This is the value a user types into a game's server list, so it belongs on
   * the peer row rather than behind a separate screen. It is empty until the
   * peer holds a lease, which is the normal state for a link that is still
   * coming up - render a placeholder rather than a spinner.
   */
  virtual_address?: string
  link_kind: LinkKind
  public_key: string
  fingerprint: string
  is_host: boolean
  is_self: boolean
  rtt_ms: number
  jitter_ms: number
  packet_loss: number
  bytes_sent: number
  bytes_received: number
  /** Completed ping round trips. Zero means the RTT above is not a measurement yet. */
  round_trips: number
  since: string
}

export interface RoomSummary {
  room_id: string
  name: string
  owner: string
  is_host: boolean
  /** The room's address space, e.g. `10.200.17.0/24`. */
  subnet?: string
  /** This machine's address in that subnet. */
  local_address?: string
  peers: PeerSummary[]
  peer_count: number
  max_peers: number
  created_at: string
  game_profile?: string
  pairing_expires_at?: string
  pairing_spent: boolean
  /** 'l3' standard, 'l2' classic LAN (Ethernet). */
  mode?: 'l3' | 'l2'
}

export interface RoomCreateRequest {
  name?: string
  max_peers?: number
  pairing_ttl_seconds?: number
  game_profile?: string
  mode?: 'l3' | 'l2'
}

export interface RoomCreateResponse {
  room: RoomSummary
  pairing_code: string
  pairing_uri: string
  expires_at: string
}

export interface RoomJoinRequest {
  pairing_code: string
  display_name?: string
}

export interface RoomJoinResponse {
  room: RoomSummary
  /**
   * The guest's reply code. It must be shown to the user and sent back to the
   * host; the link is not up until the host applies it.
   */
  answer_code?: string
  /** Always true for a serverless join. Not a failure. */
  pending: boolean
  local_peer: PeerSummary
}

export interface RoomAcceptRequest {
  /** Optional: the answer code names its own room. */
  room_id?: string
  answer_code: string
  guest_peer_id?: string
}

export interface RoomRegeneratePairingRequest {
  room_id: string
  ttl_seconds?: number
}

export interface PairingResponse {
  pairing_code: string
  pairing_uri: string
  expires_at: string
}

export interface PeerPingRequest {
  peer_id: string
  count?: number
  timeout_ms?: number
}

export interface PeerPingResult {
  peer_id: string
  sent: number
  received: number
  rtt_ms: number
  jitter_ms: number
  loss: number
  min_ms: number
  max_ms: number
}

export interface PeerEvent {
  peer_id: string
  room_id: string
  state?: PeerState
  peer?: PeerSummary
  reason?: string
  at: string
}

export interface RoomEvent {
  room_id: string
  name?: string
  reason?: string
  room?: RoomSummary
  at: string
}

/** Control API method names implemented in Phases 0 through 2. */
export const METHOD = {
  hello: 'hello',
  daemonStatus: 'daemon.status',
  daemonVersion: 'daemon.version',
  daemonShutdown: 'daemon.shutdown',

  roomCreate: 'room.create',
  roomJoin: 'room.join',
  roomAccept: 'room.accept',
  roomLeave: 'room.leave',
  roomGet: 'room.get',
  roomList: 'room.list',
  roomRegeneratePairing: 'room.regenerate_pairing',

  roomClose: 'room.close',

  pairingInspect: 'pairing.inspect',
  chatSend: 'chat.send',
  chatHistory: 'chat.history',
  settingsGet: 'settings.get',
  settingsSet: 'settings.set',

  peerList: 'peer.list',
  peerGet: 'peer.get',
  peerKick: 'peer.kick',
  peerPing: 'peer.ping',

  networkStatus: 'network.status',
  networkInterface: 'network.interface',
  networkRoutes: 'network.routes',
} as const

/**
 * Control API event names.
 *
 * `peerConnected` is deliberately distinct from a `peerState` event carrying
 * `active`: the first means a round trip came back, the second means the
 * transport opened a channel. A UI that conflates them shows a green light on a
 * link that carries nothing.
 */
export const EVENT = {
  daemonState: 'daemon.state',
  daemonStopping: 'daemon.stopping',
  daemonLog: 'daemon.log',

  roomCreated: 'room.created',
  roomUpdated: 'room.updated',
  roomClosed: 'room.closed',

  peerJoined: 'peer.joined',
  peerLeft: 'peer.left',
  peerState: 'peer.state',
  peerStats: 'peer.stats',
  peerConnected: 'peer.connected',

  /** The room's addressing changed: a peer was given an address, or lost one. */
  networkChanged: 'network.changed',
  chatMessage: 'chat.message',
  peerPresence: 'peer.presence',
} as const

/** The lifecycle of a virtual LAN, as reported by `network.status`. */
export type NetworkState =
  | 'stopped'
  | 'starting'
  | 'ready'
  | 'degraded'
  | 'stopping'

export interface NetworkMetrics {
  delivered: number
  forwarded: number
  relayed: number
  dropped: number
  malformed: number
  ignored: number
  oversized: number
  /** Packets dropped for claiming an address that is not theirs. Should be 0. */
  spoofed: number
  no_route: number
  rate_limited: number
  expired: number
  bytes_delivered: number
  bytes_forwarded: number
}

export interface NetworkPeer {
  peer_id?: string
  display_name?: string
  address: string
  role: 'self' | 'host' | 'peer'
  is_host: boolean
  local: boolean
}

export interface RouteEntry {
  destination: string
  next_hop?: string
  peer?: string
  managed: boolean
  note?: string
}

export interface NetworkStatus {
  room_id?: string
  state: NetworkState
  is_host: boolean
  /** The adapter backend, e.g. `wintun` or `memory`. */
  adapter?: string
  /** The name Windows shows in its network list. */
  interface?: string
  subnet?: string
  local_address?: string
  mtu?: number
  /**
   * Whether the adapter delivers fan-out itself.
   *
   * Wintun is a layer 3 adapter and reports false. LanBaz relays broadcast and
   * multicast in that case, so the LAN still works — which is exactly why this
   * must be rendered as information rather than as an error.
   */
  broadcast: boolean
  multicast: boolean
  started_at?: string
  peers: NetworkPeer[]
  routes: RouteEntry[]
  metrics: NetworkMetrics
  /**
   * A human readable warning, e.g. the Wintun driver is missing.
   *
   * This is the single most useful field on the payload: it turns "the LAN is
   * not working" into a sentence a user can act on. Render it prominently.
   */
  note?: string
}

export interface NetworkInterface {
  room_id: string
  interface?: string
  adapter?: string
  subnet?: string
  address?: string
  mtu?: number
  broadcast: boolean
  multicast: boolean
  state: NetworkState
  started_at?: string
  is_host: boolean
  checked_at: string
}

/** One chat message (chat.message event, chat.history). */
export interface ChatMessage {
  id: string
  room_id: string
  from: string
  name: string
  text: string
  at: string
  self: boolean
}

/** What a player is running (peer.presence / game.detect). */
export interface Presence {
  peer_id: string
  room_id?: string
  name?: string
  game_id?: string
  game_name?: string
  exe?: string
  hosting: boolean
  /** "ip:port" endpoints the game listens on inside the room. */
  endpoints?: string[]
  join_uri?: string
  self?: boolean
  at: string
}

/** A supported game, from the bundled profiles. */
export interface GameProfile {
  id: string
  name: string
  protocol: string
  ports: number[]
  discovery: string
  join_hint?: string
  needs_l2?: boolean
  notes?: string
  availability?: Array<'free' | 'drm_free'>
  requires?: string
  max_lan_rtt_ms?: number
}

/** What a pasted or opened code is (pairing.inspect). */
export interface PairingInfo {
  kind: 'invite' | 'reply'
  room_id: string
  room_name?: string
  host_name?: string
  guest_name?: string
  mode?: 'l3' | 'l2'
  expires_at: string
  hosted_here: boolean
  joined_here: boolean
  valid: boolean
  problem?: string
}

/** What the engine can do right now (network.capabilities). */
/** One server in a connection check. */
export interface DiagnoseServer {
  server: string
  ok: boolean
  mapped?: string
  rtt_ms?: number
  error?: string
}

/** network.diagnose: public address, NAT type and relay health. */
export interface DiagnoseReport {
  nat: 'open' | 'cone' | 'symmetric' | 'blocked' | 'unknown'
  public_ip?: string
  stun: DiagnoseServer[]
  working: string[]
  turn: DiagnoseServer[]
  advice: 'ok' | 'needs_turn' | 'stun_blocked' | 'turn_broken'
  duration_ms: number
  /** UPnP/NAT-PMP on the home router: "UPnP 85.x.x.x", "disabled", or why not. */
  port_mapping?: string
}

export interface Capabilities {
  classic_lan: boolean
  classic_lan_reason?: string
}

/** One TURN relay server. */
export interface RelayServer {
  url: string
  username?: string
  credential?: string
}

/** Advanced network settings; zero values mean "default". */
export interface NetworkSettings {
  /** Virtual adapter MTU; 0 = default (1200). */
  mtu: number
  /** Give the LanBaz adapter top priority for broadcast/multicast. */
  interface_priority: boolean
  /** Relay LAN discovery (broadcast/multicast) between players. */
  relay_discovery: boolean
  /** Fan-out budget per player, packets/s; 0 = default (100). */
  broadcast_rate: number
  /** Answer <name>.local for players in the room. */
  name_resolution: boolean
  /** Connect guests directly to each other (lower lag). */
  mesh: boolean
  /** Only ever use the TURN relay (hides your IP from players). */
  relay_only: boolean
  /** UDP port range for connections, for port forwarding; 0 = any. */
  port_min: number
  port_max: number
  /** Default room mode for new rooms. */
  room_mode: 'l3' | 'l2'
}

/** User-editable daemon settings (settings.get / settings.set). */
export interface Settings {
  display_name: string
  stun_servers: string[] | null
  turn_servers: RelayServer[] | null
  allow_relay: boolean
  network?: NetworkSettings
  /** A free Metered.ca relay account; LanBaz fetches the relay servers itself. */
  metered?: { app: string; key: string } | null
}

/** A stable key for a peer row: the link id, else the announced id. */
export function peerKey(p: Pick<PeerSummary, 'link_id' | 'peer_id' | 'is_self'>): string {
  if (p.is_self) return 'self'
  return p.link_id || p.peer_id
}

/**
 * Go encodes an unset time as 0001-01-01T00:00:00Z. Treat anything before
 * 2000 as "not set" so the UI never shows a pairing expiry two thousand years
 * in the past.
 */
export function validTime(value?: string | null): Date | null {
  if (!value) return null
  const d = new Date(value)
  if (Number.isNaN(d.getTime()) || d.getUTCFullYear() < 2000) return null
  return d
}

export interface NetworkEvent {
  room_id: string
  status: NetworkStatus
  at: string
}