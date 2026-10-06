/**
 * Games store: what each player is running (presence) and the supported game
 * library. Presence comes from `peer.presence` events, which the engine emits
 * for this machine's own detected game too.
 */

import { create } from 'zustand'

import { daemonClient } from './daemon'
import type { GameProfile, Presence } from '../types/protocol'

interface GamesState {
  /** Latest presence per announced peer id ('' key = this machine). */
  presence: Record<string, Presence>
  library: GameProfile[]
  applyPresence: (p: Presence) => void
  loadLibrary: () => Promise<void>
  reset: () => void
}

export const useGamesStore = create<GamesState>((set) => ({
  presence: {},
  library: [],
  applyPresence: (p) =>
    set((s) => {
      const key = p.self ? '' : p.peer_id
      const presence = { ...s.presence }
      if (!p.game_id && !p.game_name) delete presence[key]
      else presence[key] = p
      return { presence }
    }),
  loadLibrary: async () => {
    const client = daemonClient()
    if (!client) return
    try {
      const library = await client.request<GameProfile[]>('game.list')
      set({ library: library ?? [] })
    } catch {
      // Older engine: no library.
    }
  },
  reset: () => set({ presence: {} }),
}))
