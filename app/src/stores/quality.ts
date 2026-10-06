import { create } from 'zustand'

import type { PeerEvent } from '../types/protocol'

/** How many recent round trips each player keeps (one per probe, ~1 s). */
const KEEP = 60

interface QualityState {
  /** Recent round trips per room/peer, oldest first. */
  rtt: Record<string, number[]>
  add: (e: PeerEvent) => void
  reset: () => void
}

export const qualityKey = (roomId: string, peerId: string) => `${roomId}/${peerId}`

/** Ping history for the per-player sparkline, fed by peer.stats events. */
export const useQuality = create<QualityState>((set) => ({
  rtt: {},
  add: (e) => {
    const p = e.peer
    if (!p || p.is_self || !p.round_trips || p.rtt_ms <= 0) return
    const key = qualityKey(e.room_id, e.peer_id)
    set((s) => {
      const prev = s.rtt[key] ?? []
      const next = prev.length >= KEEP ? prev.slice(prev.length - KEEP + 1) : prev.slice()
      next.push(p.rtt_ms)
      return { rtt: { ...s.rtt, [key]: next } }
    })
  },
  reset: () => set({ rtt: {} }),
}))
