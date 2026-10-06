import { invoke } from '@tauri-apps/api/core'
import { listen } from '@tauri-apps/api/event'
import { create } from 'zustand'

import { isTauri } from '../services/tauri'

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

interface UpdateState {
  status: UpdateStatus | null
  available: UpdateInfo | null
  checking: boolean
  installing: boolean
  /** Bytes downloaded and total, while installing. */
  progress: { done: number; total: number | null } | null
  error: string | null
  refresh: () => Promise<void>
  check: () => Promise<UpdateInfo | null>
  install: () => Promise<void>
  setRepo: (repo: string) => Promise<void>
}

/** Automatic updates through the shell (signed installers on GitHub Releases). */
export const useUpdates = create<UpdateState>((set, get) => ({
  status: null,
  available: null,
  checking: false,
  installing: false,
  progress: null,
  error: null,
  refresh: async () => {
    if (!isTauri()) return
    set({ status: await invoke<UpdateStatus>('update_status') })
  },
  check: async () => {
    if (!isTauri() || get().checking) return null
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
  install: async () => {
    set({ installing: true, error: null, progress: { done: 0, total: null } })
    const stop = await listen<{ downloaded: number; total: number | null }>('update-progress', (e) => {
      const p = get().progress ?? { done: 0, total: null }
      set({
        progress: {
          done: p.done + e.payload.downloaded,
          total: e.payload.total ?? p.total,
        },
      })
    })
    try {
      // On success the installer starts and LanBaz exits; nothing after this runs.
      await invoke('update_install')
    } catch (e) {
      set({ error: String(e), installing: false, progress: null })
    } finally {
      stop()
    }
  },
  setRepo: async (repo) => {
    await invoke('update_set_repo', { repo })
    set({ available: null })
    await get().refresh()
  },
}))
