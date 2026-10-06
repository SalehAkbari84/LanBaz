import { invoke } from '@tauri-apps/api/core'
import { listen } from '@tauri-apps/api/event'
import { create } from 'zustand'

import { translate } from '../i18n'
import { isTauri } from '../services/tauri'
import { usePrefs } from './prefs'
import { toast } from './toasts'

const failed = (e: unknown) =>
  toast({ tone: 'error', title: translate(usePrefs.getState().language, 'update.failed'), body: String(e) }, 10000)

export interface UpdateStatus {
  configured: boolean
  repo: string
  current: string
}

export interface UpdateInfo {
  version: string
  notes: string
  date: string
}

/** idle → (asked) → downloading → applying (LanBaz closes and the installer runs). */
export type UpdatePhase = 'idle' | 'downloading' | 'applying'

interface UpdateState {
  status: UpdateStatus | null
  available: UpdateInfo | null
  /** The version the main screen is asking about, or null. */
  prompt: UpdateInfo | null
  phase: UpdatePhase
  checking: boolean
  /** Bytes downloaded and total, while downloading. */
  progress: { done: number; total: number | null } | null
  error: string | null
  refresh: () => Promise<void>
  check: () => Promise<UpdateInfo | null>
  /** A periodic check: asks on the main screen unless that version was snoozed. */
  checkAndAsk: () => Promise<void>
  /** "Later": do not ask about this version again for a while. */
  snooze: () => void
  /** Download, then close the rooms and install. */
  install: () => Promise<void>
  setRepo: (repo: string) => Promise<void>
}

const SNOOZE_MS = 2 * 3600 * 1000
let snoozed: { version: string; until: number } | null = null

/** Automatic updates through the shell (signed installers on GitHub Releases). */
export const useUpdates = create<UpdateState>((set, get) => ({
  status: null,
  available: null,
  prompt: null,
  phase: 'idle',
  checking: false,
  progress: null,
  error: null,

  refresh: async () => {
    if (!isTauri()) return
    set({ status: await invoke<UpdateStatus>('update_status') })
  },

  check: async () => {
    if (!isTauri() || get().checking || get().phase !== 'idle') return null
    set({ checking: true, error: null })
    try {
      const available = await invoke<UpdateInfo | null>('update_check')
      set({ available })
      return available
    } catch (e) {
      set({ error: String(e) })
      return null
    } finally {
      set({ checking: false })
    }
  },

  checkAndAsk: async () => {
    await get().refresh()
    const found = await get().check()
    if (!found || get().phase !== 'idle') return
    if (snoozed && snoozed.version === found.version && Date.now() < snoozed.until) return
    set({ prompt: found })
  },

  snooze: () => {
    const v = get().prompt?.version
    if (v) snoozed = { version: v, until: Date.now() + SNOOZE_MS }
    set({ prompt: null })
  },

  install: async () => {
    if (get().phase !== 'idle') return
    set({ prompt: null, phase: 'downloading', error: null, progress: { done: 0, total: null } })
    const stop = await listen<{ downloaded: number; total: number | null }>('update-progress', (e) => {
      const p = get().progress ?? { done: 0, total: null }
      set({ progress: { done: p.done + e.payload.downloaded, total: e.payload.total ?? p.total } })
    })
    try {
      await invoke('update_download')
    } catch (e) {
      set({ error: String(e), phase: 'idle', progress: null })
      failed(e)
      return
    } finally {
      stop()
    }
    // Downloaded and verified. The shell stops the daemon, which says goodbye
    // to every player and closes the rooms, then the installer replaces LanBaz
    // and starts the new version; kept networks come back by themselves.
    set({ phase: 'applying' })
    await new Promise((r) => setTimeout(r, 1200)) // let "closing rooms" be seen
    try {
      await invoke('update_apply')
    } catch (e) {
      set({ error: String(e), phase: 'idle', progress: null })
      failed(e)
    }
  },

  setRepo: async (repo) => {
    await invoke('update_set_repo', { repo })
    set({ available: null })
    await get().refresh()
  },
}))
