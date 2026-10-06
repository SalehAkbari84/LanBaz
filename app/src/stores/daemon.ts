/**
 * Daemon store: the single place that owns a DaemonClient instance and the
 * status snapshot the UI renders.
 *
 * Components read state from here and call the actions; they never construct a
 * DaemonClient themselves, so there is exactly one socket per window.
 */

import { create } from 'zustand'

import { useChatStore } from './chat'
import { useFriendsStore, wireFriendEvents } from './friends'
import { useGamesStore } from './games'
import { usePrefs } from './prefs'
import { useQuality } from './quality'
import { useVoice } from './voice'
import { useFiles, type FileTransfer } from './files'
import { useRoomsStore } from './rooms'
import { toast } from './toasts'
import { translate } from '../i18n'
import { DaemonClient, DaemonError, type ConnectionState } from '../services/daemon-client'
import { announce, isOverlaySurface } from '../services/overlay'
import {
  daemonEndpoint,
  isElevated,
  isTauri,
  startDaemon as startDaemonSidecar,
  stopDaemon as stopDaemonSidecar,
} from '../services/tauri'
import {
  EVENT,
  METHOD,
  type DaemonStatus,
  type DaemonVersion,
  type Capabilities,
  type DiagnoseReport,
  type ChatMessage,
  type NetworkEvent,
  type Presence,
  type PeerEvent,
  type RoomEvent,
  type Settings,
  type StateEvent,
} from '../types/protocol'

export type Page = 'home' | 'friends' | 'rooms' | 'chat' | 'games' | 'settings'

interface DaemonState {
  page: Page
  connection: ConnectionState
  connectionError: string | null
  status: DaemonStatus | null
  version: DaemonVersion | null
  /** User-editable daemon settings, or null until loaded. */
  settings: Settings | null
  /** Whether the shell runs with administrator rights; null until known. */
  elevated: boolean | null
  /** True after the user stopped the daemon on purpose. */
  stoppedByUser: boolean
  /** What the engine can do (Classic LAN driver present, ...). */
  capabilities: Capabilities | null
  refreshCapabilities: () => Promise<void>
  /** Runs the connection check (network.diagnose); takes a few seconds. */
  diagnose: () => Promise<DiagnoseReport>
  busy: boolean
  shutdownNotice: string | null
  lastError: string | null

  setPage: (page: Page) => void
  /** Navigates to the rooms page and loads the current rooms. */
  openRooms: () => void
  connect: () => Promise<void>
  refresh: () => Promise<void>
  shutdown: () => Promise<void>
  disconnect: () => void
  clearError: () => void
  saveSettings: (next: Settings) => Promise<void>
}

let client: DaemonClient | null = null
const unsubscribers: Array<() => void> = []
let retryTimer: ReturnType<typeof setTimeout> | null = null

function resetClient(): void {
  for (const off of unsubscribers.splice(0)) off()
  client?.dispose()
  client = null
}

function clearRetry(): void {
  if (retryTimer !== null) {
    clearTimeout(retryTimer)
    retryTimer = null
  }
}

export const useDaemonStore = create<DaemonState>((set, get) => ({
  page: 'home',
  connection: 'idle',
  connectionError: null,
  status: null,
  version: null,
  settings: null,
  elevated: null,
  stoppedByUser: false,
  capabilities: null,
  busy: false,
  shutdownNotice: null,
  lastError: null,

  setPage: (page) => set({ page }),

  openRooms: () => {
    set({ page: 'rooms' })
    void useRoomsStore.getState().load()
  },

  clearError: () => set({ lastError: null, connectionError: null }),

  disconnect: () => {
    clearRetry()
    resetClient()
    set({ connection: 'idle', status: null, connectionError: null })
  },

  /**
   * Ensures a daemon exists, connects to it and loads the initial state.
   *
   * Only the main window asks the shell to start the daemon (the shell also
   * starts it by itself at launch). The overlay just waits for one: if both
   * windows could start it, a daemon the user stopped on purpose would be
   * brought back by the hidden overlay a second later.
   */
  connect: async () => {
    clearRetry()
    set({ busy: true, lastError: null, connectionError: null, stoppedByUser: false, shutdownNotice: null })

    if (!isTauri()) {
      set({
        busy: false,
        connection: 'error',
        connectionError: 'This build must run inside the LanBaz shell. Use "npm run tauri dev".',
      })
      return
    }

    if (get().elevated === null) {
      void isElevated().then((elevated) => set({ elevated })).catch(() => undefined)
    }

    try {
      if (!isOverlaySurface()) {
        // Not fatal: the user may have started lanbazd by hand.
        await startDaemonSidecar().catch(() => undefined)
      }

      const endpoint = await daemonEndpoint()
      resetClient()

      const next = new DaemonClient({ endpointProvider: daemonEndpoint })
      client = next

      unsubscribers.push(
        next.onState((state, error) => {
          set({ connection: state, connectionError: error ?? null })
          if (state === 'connected') {
            // After every (re)connect the daemon may be a different process
            // with different rooms: reload everything rather than trusting
            // what was on screen before the gap.
            void get().refresh()
            void useRoomsStore
              .getState()
              .load()
              .then(() => {
                for (const id of useRoomsStore.getState().order) void useChatStore.getState().loadHistory(id)
              })
            void loadSettings(set)
            void get().refreshCapabilities()
            void useGamesStore.getState().loadLibrary()
            void useFriendsStore.getState().load()
            void useFiles.getState().load()
            void next
              .request<Presence[]>('game.detect')
              .then((list) => {
                for (const p of list ?? []) useGamesStore.getState().applyPresence(p)
              })
              .catch(() => undefined)
          }
        }),
        next.on<StateEvent>(EVENT.daemonState, (event) => {
          set((prev) => ({
            status: prev.status ? { ...prev.status, state: event.state } : prev.status,
          }))
        }),
        next.on<StateEvent>(EVENT.daemonStopping, (event) => {
          set((prev) => ({
            status: prev.status ? { ...prev.status, state: event.state } : null,
          }))
        }),
        next.on<RoomEvent>(EVENT.roomCreated, (event) => {
          useRoomsStore.getState().applyRoom(event)
        }),
        next.on<RoomEvent>(EVENT.roomClosed, (event) => {
          useRoomsStore.getState().applyRoomClosed(event.room_id, event.reason)
          useChatStore.getState().forgetRoom(event.room_id)
          if (useVoice.getState().roomId === event.room_id) useVoice.getState().leave()
        }),
        next.on<PeerEvent>(EVENT.peerJoined, (event) => {
          const known = useRoomsStore.getState().hasPeer(event)
          useRoomsStore.getState().applyPeer(event)
          // A peer.joined fires before the hello (no name yet) and again with
          // it; announce the named one.
          const name = event.peer?.display_name
          if (!known || (name && !useRoomsStore.getState().announced.has(event.peer?.link_id ?? event.peer_id))) {
            if (name && !event.peer?.is_self) {
              useRoomsStore.getState().announced.add(event.peer?.link_id ?? event.peer_id)
              notifyPeer('overlay.joined', name)
              // Someone arriving mid-match is exactly what the overlay is for.
              if (!isOverlaySurface()) void announce()
            }
          }
        }),
        next.on<PeerEvent>(EVENT.peerState, (event) => {
          useRoomsStore.getState().applyPeer(event)
        }),
        next.on<PeerEvent>(EVENT.peerConnected, (event) => {
          useRoomsStore.getState().applyPeer(event)
        }),
        next.on<PeerEvent>(EVENT.peerStats, (event) => {
          useRoomsStore.getState().applyPeer(event, { quiet: true })
          useQuality.getState().add(event)
        }),
        next.on<FileTransfer>('file.update', (f) => {
          useFiles.getState().apply(f)
        }),
        next.on<{ room_id: string; from?: string; data: never }>('voice.signal', (v) => {
          useVoice.getState().onSignal(v.room_id, v.from ?? '', v.data)
        }),
        next.on<PeerEvent>(EVENT.peerLeft, (event) => {
          useRoomsStore.getState().removePeer(event)
          const name = event.peer?.display_name
          if (name && !event.peer?.is_self) notifyPeer('overlay.left', name)
        }),
        next.on<Presence>(EVENT.peerPresence, (p) => {
          const before = useGamesStore.getState().presence[p.self ? '' : p.peer_id]
          useGamesStore.getState().applyPresence(p)
          const prefs = usePrefs.getState()
          if (!p.self && p.game_name && before?.game_id !== p.game_id && prefs.toastGames) {
            toast({
              tone: 'info',
              title: translate(prefs.language, 'overlay.startedGame', { name: p.name || '?', game: p.game_name }),
              who: p.name || '?',
            })
          }
        }),
        next.on<Presence>('game.detected', (p) => {
          useGamesStore.getState().applyPresence({ ...p, self: true })
        }),
        next.on<ChatMessage>(EVENT.chatMessage, (m) => {
          useChatStore.getState().receive(m)
          const chat = useChatStore.getState()
          if (!m.self && usePrefs.getState().toastChat && !(chat.viewing && !isOverlaySurface())) {
            toast({ tone: 'chat', title: m.name || '?', body: m.text, who: m.name || '?' })
            if (usePrefs.getState().soundOnChat && !isOverlaySurface()) chime()
            if (!isOverlaySurface()) void announce()
          }
        }),
        next.on<NetworkEvent>(EVENT.networkChanged, (event) => {
          useRoomsStore.getState().applyNetwork(event)
        }),
      )
      wireFriendEvents(next)

      await next.connect(endpoint)
      set({ busy: false })
    } catch (err) {
      set({
        busy: false,
        connection: 'error',
        connectionError: describeError(err),
      })
      // Keep trying in the background until a daemon appears, unless the user
      // stopped it on purpose. The shell's supervisor restarts a crashed one;
      // this is what reconnects the UI to it.
      if (!get().stoppedByUser) {
        retryTimer = setTimeout(() => {
          retryTimer = null
          if (!get().stoppedByUser && get().connection !== 'connected') void get().connect()
        }, 3000)
      }
    }
  },

  /** Reloads daemon.status and daemon.version. */
  refresh: async () => {
    if (!client) return
    try {
      const [status, version] = await Promise.all([
        client.request<DaemonStatus>(METHOD.daemonStatus),
        client.request<DaemonVersion>(METHOD.daemonVersion),
      ])
      set({ status, version, lastError: null })
    } catch (err) {
      set({ lastError: describeError(err) })
    }
  },

  /**
   * Stops the daemon on purpose. Goes through the shell, which asks the daemon
   * to shut down gracefully (removing the adapter and firewall rule), kills it
   * if it does not, and tells the supervisor not to bring it back.
   */
  shutdown: async () => {
    clearRetry()
    set({ busy: true, lastError: null, stoppedByUser: true })
    try {
      resetClient()
      await stopDaemonSidecar()
      set({
        busy: false,
        shutdownNotice: 'LanBaz was stopped. Start it again to rejoin your rooms.',
        status: null,
        connection: 'disconnected',
      })
    } catch (err) {
      set({
        busy: false,
        connection: 'disconnected',
        shutdownNotice: 'LanBaz was stopped.',
        lastError: describeError(err),
      })
    }
    useRoomsStore.getState().reset()
    useChatStore.getState().reset()
    useGamesStore.getState().reset()
    useFriendsStore.getState().reset()
  },

  diagnose: async () => {
    if (!client) throw new Error('not connected to LanBaz')
    return client.request<DiagnoseReport>('network.diagnose', undefined, 45_000)
  },

  refreshCapabilities: async () => {
    if (!client) return
    try {
      set({ capabilities: await client.request<Capabilities>('network.capabilities') })
    } catch {
      set({ capabilities: null })
    }
  },

  saveSettings: async (nextSettings) => {
    if (!client) throw new Error('not connected to LanBaz')
    const saved = await client.setSettings(nextSettings)
    set({ settings: saved })
  },
}))

/** Join/leave toast, honouring the notification preference. */
function notifyPeer(key: 'overlay.joined' | 'overlay.left', name: string): void {
  const prefs = usePrefs.getState()
  if (!prefs.toastJoinLeave) return
  toast({ tone: key === 'overlay.joined' ? 'ok' : 'info', title: translate(prefs.language, key, { name }), who: name })
}

async function loadSettings(set: (partial: Partial<DaemonState>) => void): Promise<void> {
  if (!client) return
  try {
    set({ settings: await client.getSettings() })
  } catch {
    // An older daemon without settings support: the page shows defaults.
  }
}

/**
 * The one socket this window shares.
 *
 * Exported for the rooms store, which needs to call methods but must not open a
 * second connection: the daemon counts sessions against its connection limit.
 */
export function daemonClient(): DaemonClient | null {
  return client
}

function describeError(err: unknown): string {
  if (err instanceof DaemonError) return `${err.code}: ${err.message}`
  if (err instanceof Error) return err.message
  return String(err)
}

/** A short two-note chime for new chat messages; no audio files needed. */
function chime(): void {
  try {
    const ctx = new AudioContext()
    const now = ctx.currentTime
    for (const [i, freq] of [880, 1320].entries()) {
      const osc = ctx.createOscillator()
      const gain = ctx.createGain()
      osc.frequency.value = freq
      gain.gain.setValueAtTime(0.0001, now + i * 0.09)
      gain.gain.exponentialRampToValueAtTime(0.08, now + i * 0.09 + 0.02)
      gain.gain.exponentialRampToValueAtTime(0.0001, now + i * 0.09 + 0.18)
      osc.connect(gain).connect(ctx.destination)
      osc.start(now + i * 0.09)
      osc.stop(now + i * 0.09 + 0.2)
    }
    setTimeout(() => void ctx.close(), 600)
  } catch {
    // No audio device: silence is fine.
  }
}
