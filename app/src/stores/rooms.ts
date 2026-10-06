/**
 * Rooms store: the live view of every room this daemon hosts or has joined.
 *
 * Everything here is event-driven. The daemon already pushes room.created,
 * room.closed, peer.joined, peer.state, peer.left and network.changed, and each
 * carries the whole room summary - so the UI never polls. A poll loop would look
 * identical to a user and cost a round trip every second forever; the events
 * also arrive within milliseconds of the thing that changed, which is what makes
 * the peer list feel attached to reality.
 *
 * The store owns room data. The daemon store owns the socket, and exposes it for
 * exactly this one purpose, so the app still has a single connection.
 */

import { create } from 'zustand'

import { daemonClient } from './daemon'
import { usePrefs } from './prefs'
import { translate } from '../i18n'
import {
  isUsable,
  peerKey,
  type NetworkEvent,
  type NetworkStatus,
  type PeerEvent,
  type PeerPingResult,
  type PeerSummary,
  type RoomCreateRequest,
  type RoomEvent,
  type RoomSummary,
} from '../types/protocol'

/** What the UI is in the middle of, so a button can show progress and an error can name itself. */
export type RoomActionKind =
  | 'create'
  | 'join'
  | 'accept'
  | 'regenerate'
  | 'leave'
  | 'close'
  | 'kick'
  | 'ping'

export interface RoomAction {
  kind: RoomActionKind
  roomId?: string
  peerId?: string
  label?: string
}

interface RoomsState {
  rooms: Record<string, RoomSummary>
  order: string[]
  /** The virtual LAN per room, or null when it could not be read. */
  networks: Record<string, NetworkStatus | null>
  /** The last finished measurement per peer id. */
  pings: Record<string, PeerPingResult>
  /**
   * Codes this app was issued, kept out of the room summary on purpose.
   *
   * The daemon never puts a pairing code in an event: an event goes to every
   * connected UI, and a code is a bearer credential that anyone holding it can
   * redeem. Only the machine that pressed "create room" ever sees that one.
   *
   * They live in the store rather than in a module-level map so that reading one
   * is a subscription. A map outside the store is not reactive, and a pairing
   * code that appears without the screen updating is a code the user never gets
   * to send.
   */
  pairingCodes: Record<string, string>
  answerCodes: Record<string, string>
  /** Expiry of the code this app holds per room, kept apart from the summary. */
  pairingExpiry: Record<string, string>
  busy: RoomAction | null
  lastError: string | null
  /** Why a room disappeared, e.g. the host closed it. Shown once. */
  notice: string | null
  /** Peers already announced with a join toast, by link id. */
  announced: Set<string>

  list: () => RoomSummary[]
  networkOf: (roomId: string) => NetworkStatus | null
  load: () => Promise<void>
  createRoom: (req: RoomCreateRequest) => Promise<RoomSummary>
  joinRoom: (code: string) => Promise<RoomSummary>
  acceptAnswer: (answerCode: string) => Promise<void>
  regenerate: (roomId: string) => Promise<void>
  leaveRoom: (roomId: string) => Promise<void>
  closeRoom: (roomId: string) => Promise<void>
  kickPeer: (roomId: string, peerId: string) => Promise<void>
  pingPeer: (roomId: string, peerId: string) => Promise<void>
  clearError: () => void
  clearNotice: () => void
  /** Forgets everything, e.g. after the daemon was stopped. */
  reset: () => void

  // Event reducers, exported so the daemon store can wire them to the socket.
  applyRoom: (event: RoomEvent) => void
  applyRoomClosed: (roomId: string, reason?: string) => void
  applyPeer: (event: PeerEvent, opts?: { quiet?: boolean }) => void
  removePeer: (event: PeerEvent) => void
  hasPeer: (event: PeerEvent) => boolean
  applyNetwork: (event: NetworkEvent) => void
}

/**
 * Codes a room was issued, kept outside the room summary.
 */
export const useRoomsStore = create<RoomsState>((set, get) => ({
  rooms: {},
  order: [],
  networks: {},
  pings: {},
  pairingCodes: {},
  answerCodes: {},
  pairingExpiry: {},
  busy: null,
  lastError: null,
  notice: null,
  announced: new Set<string>(),

  list: () =>
    get()
      .order.map((id) => get().rooms[id])
      .filter((room): room is RoomSummary => Boolean(room)),
  networkOf: (roomId) => get().networks[roomId] ?? null,
  clearError: () => set({ lastError: null }),
  clearNotice: () => set({ notice: null }),
  reset: () =>
    set({
      rooms: {},
      order: [],
      networks: {},
      pings: {},
      pairingCodes: {},
      answerCodes: {},
      pairingExpiry: {},
      busy: null,
    }),

  applyRoom: (event) => {
    const summary = event.room
    if (!summary) return
    const room = normalizeRoom(summary)
    set((state) => ({
      rooms: { ...state.rooms, [room.room_id]: room },
      order: state.rooms[room.room_id] ? state.order : [room.room_id, ...state.order],
    }))
    // A room summary carries the addressing but not the packet path. Only
    // network.status says whether the adapter is up, which is the difference
    // between "joined" and "actually playable".
    if (!get().networks[room.room_id]) {
      void loadNetwork(room.room_id)
    }
  },

  applyRoomClosed: (roomId, reason) => {
    const known = get().rooms[roomId]
    if (known && !known.is_host && reason && reason !== 'left') {
      set({ notice: closedReason(known.name, reason) })
    }
    set((state) => {
      const rooms = { ...state.rooms }
      delete rooms[roomId]
      const networks = { ...state.networks }
      delete networks[roomId]
      const pairingCodes = { ...state.pairingCodes }
      delete pairingCodes[roomId]
      const answerCodes = { ...state.answerCodes }
      delete answerCodes[roomId]
      const pairingExpiry = { ...state.pairingExpiry }
      delete pairingExpiry[roomId]
      return {
        rooms,
        networks,
        pairingCodes,
        answerCodes,
        pairingExpiry,
        order: state.order.filter((id) => id !== roomId),
      }
    })
  },

  applyPeer: (event, opts) => {
    const room = get().rooms[event.room_id]
    if (!room) {
      // An event for a room this window has not loaded yet (the overlay,
      // or a room created from the CLI): fetch the whole summary once.
      if (!opts?.quiet) void refreshRoom(event.room_id)
      return
    }
    const peer = event.peer
    if (!peer) return
    // The guest's reply code has done its job once the host is connected.
    const answerCodes = { ...get().answerCodes }
    if (!room.is_host && peer.is_host && isUsable(peer.state)) {
      delete answerCodes[event.room_id]
    }
    set({
      answerCodes,
      rooms: {
        ...get().rooms,
        [event.room_id]: { ...room, peers: mergePeer(room.peers, peer) },
      },
    })
    // A peer arriving or changing state is also a change to the routing table,
    // and the LAN badge must not lag behind the peer list. Stats ticks are not.
    if (!opts?.quiet) void loadNetwork(event.room_id)
  },

  removePeer: (event) => {
    const room = get().rooms[event.room_id]
    if (!room) return
    // Match by link id when the event has a summary, else by announced id.
    // The local "you" row is never removed by a peer event.
    const gone = (p: PeerSummary): boolean => {
      if (p.is_self) return false
      if (event.peer) return peerKey(p) === peerKey(event.peer)
      return event.peer_id !== '' && p.peer_id === event.peer_id
    }
    set({
      rooms: {
        ...get().rooms,
        [event.room_id]: { ...room, peers: (room.peers ?? []).filter((p) => !gone(p)) },
      },
    })
    void loadNetwork(event.room_id)
  },

  hasPeer: (event) => {
    const room = get().rooms[event.room_id]
    if (!room || !event.peer) return false
    const key = peerKey(event.peer)
    return (room.peers ?? []).some((p) => peerKey(p) === key)
  },

  applyNetwork: (event) => {
    set((state) => ({ networks: { ...state.networks, [event.room_id]: event.status } }))
  },

  load: async () => {
    const client = daemonClient()
    if (!client) return
    try {
      const rooms = (await client.listRooms()).map(normalizeRoom)
      const live = new Set(rooms.map((r) => r.room_id))
      set((state) => ({
        rooms: Object.fromEntries(rooms.map((r) => [r.room_id, r])),
        order: rooms.map((r) => r.room_id),
        // Codes for rooms the daemon no longer knows about are dead weight. A
        // code for a live room is kept: reopening this tab must not take the
        // only copy away from the user who created it.
        pairingCodes: keepOnly(state.pairingCodes, live),
        answerCodes: keepOnly(state.answerCodes, live),
        lastError: null,
      }))
      await Promise.all(rooms.map((r) => loadNetwork(r.room_id)))
    } catch (err) {
      set({ lastError: describe(err) })
    }
  },

  createRoom: async (req) => {
    const client = daemonClient()
    if (!client) throw new Error('not connected to the daemon')
    set({ busy: { kind: 'create', label: 'Creating room' }, lastError: null })
    try {
      const res = await client.createRoom(req)
      const room = normalizeRoom(res.room)
      set((state) => ({
        pairingCodes: { ...state.pairingCodes, [room.room_id]: res.pairing_code },
        pairingExpiry: { ...state.pairingExpiry, [room.room_id]: res.expires_at },
      }))
      get().applyRoom({ room_id: room.room_id, room, at: now() })
      await loadNetwork(room.room_id)
      return room
    } catch (err) {
      set({ lastError: describe(err) })
      throw err
    } finally {
      set({ busy: null })
    }
  },

  joinRoom: async (code) => {
    const client = daemonClient()
    if (!client) throw new Error('not connected to the daemon')
    set({ busy: { kind: 'join', label: 'Joining' }, lastError: null })
    try {
      const res = await client.joinRoom({ pairing_code: code.trim() })
      const room = normalizeRoom(res.room)
      if (res.answer_code) {
        set((state) => ({ answerCodes: { ...state.answerCodes, [room.room_id]: res.answer_code! } }))
      }
      get().applyRoom({ room_id: room.room_id, room, at: now() })
      await loadNetwork(room.room_id)
      return room
    } catch (err) {
      set({ lastError: describe(err) })
      throw err
    } finally {
      set({ busy: null })
    }
  },

  acceptAnswer: async (answerCode) => {
    const client = daemonClient()
    if (!client) throw new Error('not connected to the daemon')
    set({ busy: { kind: 'accept', label: 'Applying answer' }, lastError: null })
    try {
      const ev = await client.acceptRoom({ answer_code: answerCode.trim() })
      // The accept response names the room but carries no summary; fetch it so
      // the new player appears now rather than at the next event.
      await refreshRoom(ev.room_id)
      await loadNetwork(ev.room_id)
    } catch (err) {
      set({ lastError: describe(err) })
      throw err
    } finally {
      set({ busy: null })
    }
  },

  regenerate: async (roomId) => {
    const client = daemonClient()
    if (!client) throw new Error('not connected to the daemon')
    set({ busy: { kind: 'regenerate', roomId, label: 'Issuing a new code' }, lastError: null })
    try {
      const res = await client.regeneratePairing({ room_id: roomId })
      set((state) => ({
        pairingCodes: { ...state.pairingCodes, [roomId]: res.pairing_code },
        pairingExpiry: { ...state.pairingExpiry, [roomId]: res.expires_at },
      }))
    } catch (err) {
      set({ lastError: describe(err) })
      throw err
    } finally {
      set({ busy: null })
    }
  },

  leaveRoom: async (roomId) => {
    const client = daemonClient()
    if (!client) throw new Error('not connected to the daemon')
    set({ busy: { kind: 'leave', roomId, label: 'Leaving' }, lastError: null })
    try {
      await client.leaveRoom(roomId)
      get().applyRoomClosed(roomId)
    } catch (err) {
      set({ lastError: describe(err) })
      throw err
    } finally {
      set({ busy: null })
    }
  },

  closeRoom: async (roomId) => {
    const client = daemonClient()
    if (!client) throw new Error('not connected to the daemon')
    set({ busy: { kind: 'close', roomId, label: 'Closing room' }, lastError: null })
    try {
      await client.closeRoom(roomId)
      get().applyRoomClosed(roomId)
    } catch (err) {
      set({ lastError: describe(err) })
      throw err
    } finally {
      set({ busy: null })
    }
  },

  kickPeer: async (roomId, peerId) => {
    const client = daemonClient()
    if (!client) throw new Error('not connected to the daemon')
    set({ busy: { kind: 'kick', roomId, peerId, label: 'Removing player' }, lastError: null })
    try {
      await client.kickPeer(roomId, peerId)
      await refreshRoom(roomId)
    } catch (err) {
      set({ lastError: describe(err) })
      throw err
    } finally {
      set({ busy: null })
    }
  },

  pingPeer: async (roomId, peerId) => {
    const client = daemonClient()
    if (!client) throw new Error('not connected to the daemon')
    set({ busy: { kind: 'ping', roomId, peerId, label: 'Measuring' }, lastError: null })
    try {
      const result = await client.pingPeer({ peer_id: peerId, count: 4, timeout_ms: 4000 })
      set((state) => ({ pings: { ...state.pings, [peerId]: result } }))
    } catch (err) {
      set({ lastError: describe(err) })
      throw err
    } finally {
      set({ busy: null })
    }
  },
}))

/**
 * Reads one room's virtual LAN.
 *
 * A failure is recorded as null rather than raised: a room with no virtual
 * network still has a summary, and "why is there no LAN" is answered by the
 * status itself. A banner over the whole page for one missing adapter would be
 * worse than the note it is trying to explain.
 */
async function loadNetwork(roomId: string): Promise<void> {
  const client = daemonClient()
  if (!client) return
  try {
    const status = await client.networkStatus(roomId)
    useRoomsStore.setState((state) => ({ networks: { ...state.networks, [roomId]: status } }))
  } catch {
    useRoomsStore.setState((state) => ({ networks: { ...state.networks, [roomId]: null } }))
  }
}

/** Re-reads one room's summary and merges it into the store. */
async function refreshRoom(roomId: string): Promise<void> {
  const client = daemonClient()
  if (!client) return
  try {
    const room = normalizeRoom(await client.getRoom(roomId))
    useRoomsStore.getState().applyRoom({ room_id: room.room_id, room, at: now() })
  } catch {
    // The room may have closed in the meantime; its own event handles that.
  }
}

/** Human sentence for why a joined room disappeared. */
function closedReason(name: string, reason: string): string {
  const lang = usePrefs.getState().language
  return translate(lang, reason === 'host_left' || reason === 'closed' ? 'rooms.closedByHost' : 'rooms.closedGeneric', { name })
}

/**
 * Merges a peer event into a room's peer list, keyed by link id.
 *
 * A peer that changes state keeps its row and its measurements; a peer that
 * arrives gets a new one. `is_self` rows are kept: "you" belongs in a peer list.
 */
function mergePeer(peers: PeerSummary[], incoming: PeerSummary): PeerSummary[] {
  const list = peers ?? []
  const key = peerKey(incoming)
  const index = list.findIndex((p) => peerKey(p) === key)
  if (index === -1) return sortPeers([...list, incoming])
  const next = list.slice()
  next[index] = { ...next[index], ...incoming }
  return sortPeers(next)
}

/** "You" first, then by name, so the row a user is looking for does not move. */
export function sortPeers(peers: PeerSummary[]): PeerSummary[] {
  return peers.slice().sort((a, b) => {
    if (a.is_self !== b.is_self) return a.is_self ? -1 : 1
    const an = (a.display_name || a.peer_id).toLowerCase()
    const bn = (b.display_name || b.peer_id).toLowerCase()
    if (an !== bn) return an < bn ? -1 : 1
    return a.peer_id < b.peer_id ? -1 : 1
  })
}

/** The daemon always sends peers; a missing array must not crash a sort. */
function normalizeRoom(room: RoomSummary): RoomSummary {
  return { ...room, peers: room.peers ?? [] }
}

function now(): string {
  return new Date().toISOString()
}

function describe(err: unknown): string {
  if (err instanceof Error) {
    const code = (err as { code?: string }).code
    return code ? `${code}: ${err.message}` : err.message
  }
  return String(err)
}

/** Re-exported so a component can ask "can this link carry a game?" in one word. */
export { isUsable as peerIsUsable }

function keepOnly(
  codes: Record<string, string>,
  live: Set<string>,
): Record<string, string> {
  const kept: Record<string, string> = {}
  for (const [roomId, code] of Object.entries(codes)) {
    if (live.has(roomId)) kept[roomId] = code
  }
  return kept
}