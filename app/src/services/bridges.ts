/**
 * Things the main window tells the rest of the desktop: the tray's network
 * list, and who is talking for the in-game overlay (a separate webview with
 * its own stores, so it learns this through a Tauri event).
 */

import { invoke } from '@tauri-apps/api/core'
import { emit, listen } from '@tauri-apps/api/event'
import { useEffect } from 'react'
import { create } from 'zustand'

import { translate } from '../i18n'
import { useDaemonStore } from '../stores/daemon'
import { useKept } from '../stores/kept'
import { usePrefs } from '../stores/prefs'
import { useRoomsStore } from '../stores/rooms'
import { useVoice } from '../stores/voice'
import { isUsable } from '../types/protocol'
import { isTauri } from './tauri'

/** Keeps the tray menu's "Networks" list in step with rooms and kept networks. */
export function useTraySync(): void {
  const rooms = useRoomsStore((s) => s.rooms)
  const kept = useKept((s) => s.kept)
  const lang = usePrefs((s) => s.language)

  useEffect(() => {
    if (!isTauri()) return
    void useKept.getState().load()
    const every = setInterval(() => void useKept.getState().load(), 30_000)
    let off: (() => void) | undefined
    void listen('tray-open-network', () => useDaemonStore.getState().setPage('rooms')).then((u) => (off = u))
    return () => {
      clearInterval(every)
      off?.()
    }
  }, [])

  useEffect(() => {
    if (!isTauri()) return
    const t = (k: Parameters<typeof translate>[1], v?: Record<string, string | number>) => translate(lang, k, v)
    const keptRooms = new Set(kept?.rooms ?? [])
    const nets: { room?: string; label: string }[] = []
    for (const r of Object.values(rooms)) {
      const others = (r.peers ?? []).filter((p) => !p.is_self)
      const online = others.filter((p) => isUsable(p.state)).length
      nets.push({
        room: r.room_id,
        label: `${keptRooms.has(r.room_id) ? '📌 ' : '● '}${r.name} — ${t('tray.online', { n: online, total: others.length })}`,
      })
    }
    if (kept?.hosting && !kept.rooms.length) nets.push({ label: `○ ${kept.hosting.name} — ${t('tray.reopening')}` })
    for (const h of kept?.hosts ?? []) {
      if (h.room_id && rooms[h.room_id]) continue
      nets.push({ label: `○ ${t('tray.friendNet', { name: h.name || '?' })} — ${h.online ? t('tray.rejoining') : t('tray.offline')}` })
    }
    const timer = setTimeout(() => void invoke('tray_set_networks', { nets }).catch(() => {}), 400)
    return () => clearTimeout(timer)
  }, [rooms, kept, lang])
}

/** What the overlay needs to show who is talking. */
export interface VoiceBroadcast {
  room_id: string | null
  speaking: string[]
  self: boolean
}

/** Main window: publish voice activity for the overlay. */
export function useVoiceBroadcast(): void {
  useEffect(() => {
    if (!isTauri()) return
    let last = ''
    return useVoice.subscribe((v) => {
      const ptt = usePrefs.getState().voicePtt
      const live = ptt ? v.pttHeld : !v.muted
      const msg: VoiceBroadcast = {
        room_id: v.roomId,
        speaking: Object.entries(v.peers)
          .filter(([, p]) => p.speaking && !p.muted)
          .map(([id]) => id),
        self: live && v.speaking,
      }
      const key = JSON.stringify(msg)
      if (key === last) return
      last = key
      void emit('voice-state', msg)
    })
  }, [])
}

/** Overlay: who is talking, as broadcast by the main window. */
export const useOverlayVoice = create<VoiceBroadcast>(() => ({ room_id: null, speaking: [], self: false }))

export function useOverlayVoiceListener(): void {
  useEffect(() => {
    if (!isTauri()) return
    let off: (() => void) | undefined
    void listen<VoiceBroadcast>('voice-state', (e) => useOverlayVoice.setState(e.payload)).then((u) => (off = u))
    return () => off?.()
  }, [])
}
