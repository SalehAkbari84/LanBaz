import { create } from 'zustand'

import { translate } from '../i18n'
import type { KeptNetworks } from '../types/friends'
import { daemonClient } from './daemon'
import { usePrefs } from './prefs'
import { toast } from './toasts'

interface KeptState {
  kept: KeptNetworks | null
  load: () => Promise<void>
  set: (roomId: string, keep: boolean) => Promise<void>
}

/** Kept networks: a room that reopens (host) or is rejoined (guest) by itself. */
export const useKept = create<KeptState>((set) => ({
  kept: null,
  load: async () => {
    const c = daemonClient()
    if (!c) return
    try {
      set({ kept: await c.request<KeptNetworks>('network.kept') })
    } catch {
      // An older daemon without the method: the toggle simply stays hidden.
    }
  },
  set: async (roomId, keep) => {
    const c = daemonClient()
    if (!c) return
    try {
      set({ kept: await c.request<KeptNetworks>('network.keep', { room_id: roomId, keep }) })
      const t = (k: 'rooms.keptOn' | 'rooms.keptOff') => translate(usePrefs.getState().language, k)
      toast({ tone: 'ok', title: keep ? t('rooms.keptOn') : t('rooms.keptOff') })
    } catch (e) {
      toast({ tone: 'error', title: e instanceof Error ? e.message : String(e) }, 8000)
    }
  },
}))
