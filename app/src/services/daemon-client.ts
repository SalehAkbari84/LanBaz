/**
 * DaemonClient speaks the LanBaz local control API over a loopback WebSocket.
 *
 * The protocol lives here and nowhere else in the frontend: no React component
 * opens a socket, and nothing here knows about rooms, peers or rendering. That
 * separation is deliberate - if the daemon protocol grows, this file is the only
 * place that has to change.
 *
 * Wire format (see docs/protocol.md):
 *   request  { id, type, version, payload? }
 *   response { id, type: 'response', version, success, payload?, error? }
 *   event    { id, type, version, payload? }
 */

import {
  METHOD,
  type NetworkInterface,
  type NetworkStatus,
  type ChatMessage,
  type PairingInfo,
  type PairingResponse,
  type PeerEvent,
  type PeerPingRequest,
  type PeerPingResult,
  type PeerSummary,
  type RoomAcceptRequest,
  type RoomCreateRequest,
  type RoomCreateResponse,
  type RoomEvent,
  type RoomJoinRequest,
  type RoomJoinResponse,
  type RoomRegeneratePairingRequest,
  type RoomSummary,
  type RouteEntry,
  type Settings,
} from '../types/protocol'

export const PROTOCOL_VERSION = 1

export type MessageKind = 'request' | 'response' | 'event'

export interface ApiError {
  code: string
  message: string
  details?: Record<string, unknown>
}

export interface ApiResponse<T = unknown> {
  id: string
  type: string
  version: number
  success: boolean
  payload?: T
  error?: ApiError
}

export interface ApiEvent<T = unknown> {
  id: string
  type: string
  version: number
  payload?: T
}

/** Connection lifecycle as shown in the UI. */
export type ConnectionState =
  | 'idle'
  | 'connecting'
  | 'connected'
  | 'reconnecting'
  | 'disconnected'
  | 'error'

export interface Endpoint {
  url: string
  token: string
  pid: number
  version: string
  stateDir: string
}

/** An error carrying the daemon's machine-readable code. */
export class DaemonError extends Error {
  readonly code: string
  readonly details?: Record<string, unknown>

  constructor(apiError: ApiError) {
    super(apiError.message)
    this.name = 'DaemonError'
    this.code = apiError.code
    this.details = apiError.details
  }
}

interface Pending {
  resolve: (value: unknown) => void
  reject: (reason: Error) => void
  timer: ReturnType<typeof setTimeout>
  method: string
}

export interface DaemonClientOptions {
  /** Per-request timeout in milliseconds. */
  requestTimeoutMs?: number
  /** How long to wait before the first reconnect attempt. */
  reconnectBaseMs?: number
  /** Upper bound for the exponential backoff. */
  reconnectMaxMs?: number
  /** Injected in tests; defaults to a WebSocket factory. */
  socketFactory?: (url: string) => WebSocket
  /**
   * Re-reads the endpoint before every reconnect attempt.
   *
   * A restarted daemon listens on a new port with a new token, so reconnecting
   * to the cached endpoint can never succeed after a restart: it either finds
   * nothing listening or is refused as UNAUTHORIZED, forever.
   */
  endpointProvider?: () => Promise<Endpoint>
}

const DEFAULT_REQUEST_TIMEOUT_MS = 8_000
/**
 * Room calls gather ICE candidates or wait for a data channel to open, which
 * takes seconds and, on a slow NAT, tens of seconds. The default timeout would
 * report a failure for a join that is in fact still succeeding.
 */
const SLOW_REQUEST_TIMEOUT_MS = 45_000
const DEFAULT_RECONNECT_BASE_MS = 500
const DEFAULT_RECONNECT_MAX_MS = 8_000

export class DaemonClient {
  private socket: WebSocket | null = null
  private endpoint: Endpoint | null = null
  private readonly pending = new Map<string, Pending>()
  private readonly listeners = new Map<string, Set<(payload: unknown) => void>>()
  private readonly stateListeners = new Set<(state: ConnectionState, error?: string) => void>()

  private state: ConnectionState = 'idle'
  private lastError: string | undefined
  private reconnectAttempt = 0
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null
  private requestSeq = 0
  private disposed = false

  private readonly requestTimeoutMs: number
  private readonly reconnectBaseMs: number
  private readonly reconnectMaxMs: number
  private readonly socketFactory: (url: string) => WebSocket
  private readonly endpointProvider?: () => Promise<Endpoint>

  constructor(options: DaemonClientOptions = {}) {
    this.endpointProvider = options.endpointProvider
    this.requestTimeoutMs = options.requestTimeoutMs ?? DEFAULT_REQUEST_TIMEOUT_MS
    this.reconnectBaseMs = options.reconnectBaseMs ?? DEFAULT_RECONNECT_BASE_MS
    this.reconnectMaxMs = options.reconnectMaxMs ?? DEFAULT_RECONNECT_MAX_MS
    this.socketFactory =
      options.socketFactory ?? ((url: string) => new WebSocket(url))
  }

  // ------------------------------------------------------------ connection --

  get connectionState(): ConnectionState {
    return this.state
  }

  /**
   * Connects to the daemon and completes the `hello` handshake. Resolves only
   * when the daemon has accepted the token.
   */
  async connect(endpoint: Endpoint): Promise<void> {
    if (this.disposed) {
      throw new Error('client has been disposed')
    }
    this.endpoint = endpoint
    this.reconnectAttempt = 0
    this.setState('connecting')

    await this.openSocket()
  }

  private openSocket(): Promise<void> {
    const endpoint = this.endpoint
    if (!endpoint) {
      return Promise.reject(new Error('no endpoint configured'))
    }

    return new Promise<void>((resolve, reject) => {
      let settled = false
      let socket: WebSocket
      try {
        socket = this.socketFactory(endpoint.url)
      } catch (err) {
        this.failConnection(`cannot open ${endpoint.url}: ${describe(err)}`)
        reject(new Error(describe(err)))
        return
      }
      let connectedOnce = false

      this.socket = socket

      socket.onopen = () => {
        // The first message must be hello; the daemon closes the socket on
        // anything else.
        void this.handshake()
          .then(() => {
            settled = true
            connectedOnce = true
            this.reconnectAttempt = 0
            this.setState('connected')
            resolve()
          })
          .catch((err: unknown) => {
            if (settled) return
            settled = true
            this.closeSocket()
            if (this.state !== 'reconnecting') this.failConnection(describe(err))
            reject(err instanceof Error ? err : new Error(describe(err)))
          })
      }

      socket.onerror = () => {
        if (settled) return
        settled = true
        // While reconnecting, a refused socket is the expected outcome of an
        // attempt, not a new error; the reconnect loop reports its own state.
        if (this.state !== 'reconnecting') this.failConnection('websocket error')
        reject(new Error(`cannot reach the daemon at ${endpoint.url}`))
      }

      socket.onclose = (event: CloseEvent) => {
        this.socket = null
        // Fail every in-flight request: their sockets are gone.
        this.rejectAllPending('the daemon connection closed')
        if (this.disposed) return
        if (connectedOnce) {
          this.setState('disconnected', this.describeClose(event))
          this.scheduleReconnect()
        } else if (!settled) {
          settled = true
          reject(new Error(`cannot reach the daemon at ${endpoint.url}`))
        }
      }

      socket.onmessage = (event: MessageEvent<string>) => {
        this.handleMessage(event.data)
      }
    })
  }

  /**
   * Sends `hello` and waits for the acknowledgement. The token is read from
   * the state file by the Rust shell; it is never stored in React state and
   * never logged.
   */
  private async handshake(): Promise<void> {
    const endpoint = this.endpoint
    if (!endpoint) throw new Error('no endpoint configured')

    await this.rawRequest('hello', { token: endpoint.token, client: 'lanbaz-ui' })
  }

  /** Waits for the socket to be usable before sending, buffering is not used. */
  private ensureOpen(): WebSocket {
    const socket = this.socket
    if (!socket || socket.readyState !== WebSocket.OPEN) {
      throw new Error('not connected to the daemon')
    }
    return socket
  }

  /**
   * Issues a request and resolves with the response payload.
   * Rejects with a DaemonError when the daemon reports a failure.
   */
  async request<T = unknown>(
    method: string,
    payload?: unknown,
    timeoutMs?: number,
  ): Promise<T> {
    const result = await this.rawRequest<T>(method, payload, timeoutMs)
    return result
  }

  private async rawRequest<T = unknown>(
    method: string,
    payload?: unknown,
    timeoutMs?: number,
  ): Promise<T> {
    const socket = this.ensureOpen()

    const id = `ui-${Date.now().toString(36)}-${(this.requestSeq++).toString(36)}`
    const message: Record<string, unknown> = {
      id,
      type: method,
      version: PROTOCOL_VERSION,
    }
    if (payload !== undefined) {
      message.payload = payload
    }

    return new Promise<T>((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(id)
        reject(new Error(`request ${method} timed out after ${timeoutMs ?? this.requestTimeoutMs}ms`))
      }, timeoutMs ?? this.requestTimeoutMs)

      this.pending.set(id, {
        method,
        timer,
        resolve: (value) => resolve(value as T),
        reject,
      })

      try {
        socket.send(JSON.stringify(message))
      } catch (err) {
        clearTimeout(timer)
        this.pending.delete(id)
        reject(new Error(`cannot send ${method}: ${describe(err)}`))
      }
    })
  }

  private handleMessage(raw: string): void {
    let parsed: ApiResponse | ApiEvent
    try {
      parsed = JSON.parse(raw) as ApiResponse | ApiEvent
    } catch {
      // A malformed frame is a daemon bug; ignoring it is safer than tearing
      // down a working connection.
      return
    }

    if (parsed.type === 'response') {
      const response = parsed as ApiResponse
      this.settle(response)
      return
    }

    const event = parsed as ApiEvent
    const handlers = this.listeners.get(event.type)
    if (handlers) {
      for (const handler of handlers) {
        try {
          handler(event.payload)
        } catch (err) {
          console.error(`listener for ${event.type} threw`, err)
        }
      }
    }
  }

  private settle(response: ApiResponse): void {
    const pending = this.pending.get(response.id)
    if (!pending) return
    clearTimeout(pending.timer)
    this.pending.delete(response.id)

    if (response.success) {
      pending.resolve(response.payload ?? {})
      return
    }
    pending.reject(
      new DaemonError(
        response.error ?? { code: 'INTERNAL', message: `request ${pending.method} failed` },
      ),
    )
  }

  private rejectAllPending(reason: string): void {
    for (const [, pending] of this.pending) {
      clearTimeout(pending.timer)
      pending.reject(new Error(reason))
    }
    this.pending.clear()
  }

  // ---------------------------------------------------------------- events --

  /** Subscribes to a server event; returns an unsubscribe function. */
  on<T = unknown>(event: string, callback: (payload: T) => void): () => void {
    let handlers = this.listeners.get(event)
    if (!handlers) {
      handlers = new Set()
      this.listeners.set(event, handlers)
    }
    handlers.add(callback as (payload: unknown) => void)
    return () => {
      handlers?.delete(callback as (payload: unknown) => void)
    }
  }

  /** Subscribes to connection state changes. */
  onState(callback: (state: ConnectionState, error?: string) => void): () => void {
    this.stateListeners.add(callback)
    return () => {
      this.stateListeners.delete(callback)
    }
  }

  // ------------------------------------------------------------ lifecycle ---

  /**
   * Requests a graceful daemon shutdown. Resolves once the daemon has
   * acknowledged; the socket then closes and no reconnect is attempted.
   */
  async shutdown(): Promise<{ accepted: boolean; grace_period_ms: number }> {
    const response = await this.request<{ accepted: boolean; grace_period_ms: number }>(
      'daemon.shutdown',
      { reason: 'shutdown requested from the LanBaz UI' },
    )
    // The daemon is going away; stop treating the close as a failure.
    this.disposed = true
    this.clearReconnect()
    return response
  }

  // -------------------------------------------------------------- network ---

  /**
   * Reads the room's virtual LAN.
   *
   * The room id is optional on purpose: a user who has never created a room still
   * needs to be told *why* there is no LAN, and a call that demanded a room would
   * leave them with nothing to render. The daemon answers with its oldest room,
   * or a clear NOT_FOUND when there is none.
   *
   * Unlike the room calls this is cheap and safe to poll, so a UI should poll it
   * rather than wait for `networkChanged` alone - a driver that fails to load
   * emits no event at all.
   */
  async networkStatus(roomId?: string): Promise<NetworkStatus> {
    return this.request<NetworkStatus>(METHOD.networkStatus, { room_id: roomId })
  }

  /**
   * Reads just the interface addressing, which is the smallest answer to "what
   * do I type into the game".
   */
  async networkInterface(roomId?: string): Promise<NetworkInterface> {
    return this.request<NetworkInterface>(METHOD.networkInterface, {
      room_id: roomId,
    })
  }

  /** Reads the routing table LanBaz installed. */
  async networkRoutes(roomId?: string): Promise<RouteEntry[]> {
    return this.request<RouteEntry[]>(METHOD.networkRoutes, { room_id: roomId })
  }

  // ---------------------------------------------------------------- rooms ---

  /**
   * Creates a hosted room and returns its pairing code.
   *
   * This takes seconds rather than milliseconds: ICE gathering has to finish
   * before the code can be produced, because the code carries the offer and
   * there is no signalling channel to trickle candidates over afterwards.
   */
  async createRoom(
    req: RoomCreateRequest = {},
  ): Promise<RoomCreateResponse> {
    return this.request<RoomCreateResponse>(METHOD.roomCreate, req, SLOW_REQUEST_TIMEOUT_MS)
  }

  /**
   * Redeems a host's pairing code.
   *
   * The result is deliberately `pending`: the link does not exist until the host
   * applies `answer_code`. The caller must show that code to the user.
   */
  async joinRoom(req: RoomJoinRequest): Promise<RoomJoinResponse> {
    return this.request<RoomJoinResponse>(METHOD.roomJoin, req, SLOW_REQUEST_TIMEOUT_MS)
  }

  /** Applies a guest's answer code. `room_id` may be omitted. */
  async acceptRoom(req: RoomAcceptRequest): Promise<RoomEvent> {
    return this.request<RoomEvent>(METHOD.roomAccept, req, SLOW_REQUEST_TIMEOUT_MS)
  }

  async getRoom(roomId: string): Promise<RoomSummary> {
    return this.request<RoomSummary>(METHOD.roomGet, { room_id: roomId })
  }

  async listRooms(): Promise<RoomSummary[]> {
    return this.request<RoomSummary[]>(METHOD.roomList)
  }

  async leaveRoom(roomId: string): Promise<RoomEvent> {
    return this.request<RoomEvent>(METHOD.roomLeave, { room_id: roomId }, 20_000)
  }

  /** Ends a hosted room for every player in it. */
  async closeRoom(roomId: string): Promise<RoomEvent> {
    return this.request<RoomEvent>(METHOD.roomClose, { room_id: roomId }, 20_000)
  }

  /** Mints a fresh code with new ICE credentials, invalidating the old one. */
  async regeneratePairing(
    req: RoomRegeneratePairingRequest,
  ): Promise<PairingResponse> {
    return this.request<PairingResponse>(METHOD.roomRegeneratePairing, req, SLOW_REQUEST_TIMEOUT_MS)
  }

  async sendChat(roomId: string, text: string): Promise<ChatMessage> {
    return this.request<ChatMessage>(METHOD.chatSend, { room_id: roomId, text })
  }

  async chatHistory(roomId: string): Promise<ChatMessage[]> {
    return this.request<ChatMessage[]>(METHOD.chatHistory, { room_id: roomId })
  }

  /** Says what a code is without redeeming it. */
  async inspectPairing(code: string): Promise<PairingInfo> {
    return this.request<PairingInfo>(METHOD.pairingInspect, { code })
  }

  // -------------------------------------------------------------- settings --

  async getSettings(): Promise<Settings> {
    return this.request<Settings>(METHOD.settingsGet)
  }

  async setSettings(settings: Settings): Promise<Settings> {
    return this.request<Settings>(METHOD.settingsSet, settings)
  }

  // ---------------------------------------------------------------- peers ---

  async listPeers(roomId: string): Promise<PeerSummary[]> {
    return this.request<PeerSummary[]>(METHOD.peerList, { room_id: roomId })
  }

  async getPeer(peerId: string): Promise<PeerSummary> {
    return this.request<PeerSummary>(METHOD.peerGet, { peer_id: peerId })
  }

  /** Removes a peer and closes its link. */
  async kickPeer(roomId: string, peerId: string): Promise<PeerEvent> {
    return this.request<PeerEvent>(METHOD.peerKick, {
      room_id: roomId,
      peer_id: peerId,
    })
  }

  /**
   * Measures a peer now. Lost probes are reported in the result (`received` and
   * `loss`); only a ping that could not be sent at all is an error.
   */
  async pingPeer(req: PeerPingRequest): Promise<PeerPingResult> {
    return this.request<PeerPingResult>(METHOD.peerPing, req, 20_000)
  }

  /** Closes the socket and stops reconnecting. */
  dispose(): void {
    this.disposed = true
    this.clearReconnect()
    this.closeSocket()
    this.rejectAllPending('the client was disposed')
    this.listeners.clear()
    this.stateListeners.clear()
    this.setState('idle')
  }

  private closeSocket(): void {
    const socket = this.socket
    this.socket = null
    if (!socket) return
    socket.onopen = null
    socket.onmessage = null
    socket.onerror = null
    socket.onclose = null
    try {
      socket.close(1000, 'client closing')
    } catch {
      // Closing an already dead socket is not an error.
    }
  }

  private scheduleReconnect(): void {
    if (this.disposed || !this.endpoint) return
    this.clearReconnect()

    const delay = Math.min(
      this.reconnectMaxMs,
      this.reconnectBaseMs * Math.pow(2, this.reconnectAttempt),
    )
    this.reconnectAttempt += 1
    this.setState('reconnecting', `retrying in ${Math.round(delay / 100) / 10}s`)

    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null
      if (this.disposed) return
      void this.reconnectOnce()
    }, delay)
  }

  /** One reconnect attempt: fresh endpoint, then a socket. */
  private async reconnectOnce(): Promise<void> {
    try {
      if (this.endpointProvider) {
        this.endpoint = await this.endpointProvider()
      }
      if (this.disposed) return
      await this.openSocket()
    } catch {
      if (this.disposed) return
      // Keep trying with backoff. The state stays `reconnecting` throughout,
      // rather than flickering between error and reconnecting each attempt.
      this.scheduleReconnect()
    }
  }

  private clearReconnect(): void {
    if (this.reconnectTimer !== null) {
      clearTimeout(this.reconnectTimer)
      this.reconnectTimer = null
    }
  }

  private setState(state: ConnectionState, error?: string): void {
    this.state = state
    this.lastError = error
    for (const listener of this.stateListeners) {
      try {
        listener(state, error)
      } catch (err) {
        console.error('state listener threw', err)
      }
    }
  }

  private failConnection(message: string): void {
    this.setState('error', message)
  }

  private describeClose(event: CloseEvent): string | undefined {
    if (event.code === 1000) return undefined
    if (event.reason) return `daemon closed the connection: ${event.reason}`
    return `daemon closed the connection (code ${event.code})`
  }

  get lastErrorMessage(): string | undefined {
    return this.lastError
  }
}

function describe(err: unknown): string {
  if (err instanceof Error) return err.message
  return String(err)
}