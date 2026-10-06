import { invoke } from '@tauri-apps/api/core'
import { create } from 'zustand'

import { translate } from '../i18n'
import { daemonClient } from './daemon'
import { usePrefs } from './prefs'
import { toast } from './toasts'

export interface FileTransfer {
  id: string
  room_id: string
  peer_id: string
  peer_name?: string
  name: string
  size: number
  done: number
  rate?: number
  direction: 'in' | 'out'
  state: 'offered' | 'incoming' | 'transferring' | 'done' | 'declined' | 'canceled' | 'expired' | 'failed'
  error?: string
  path?: string
}

interface FilesState {
  transfers: Record<string, FileTransfer>
  load: () => Promise<void>
  apply: (t: FileTransfer) => void
  offer: (roomId: string, peerId: string, path: string) => Promise<void>
  respond: (id: string, accept: boolean) => Promise<void>
  cancel: (id: string) => Promise<void>
  reveal: (path: string) => Promise<void>
  dismiss: (id: string) => void
}

const tr = (k: Parameters<typeof translate>[1], vars?: Record<string, string | number>) => translate(usePrefs.getState().language, k, vars)

function fail(e: unknown): void {
  toast({ tone: 'error', title: e instanceof Error ? e.message : String(e) }, 8000)
}

/** Files sent to and received from players (file.* methods, file.update events). */
export const useFiles = create<FilesState>((set, get) => ({
  transfers: {},
  load: async () => {
    try {
      const list = (await daemonClient()?.request<FileTransfer[]>('file.list')) ?? []
      set({ transfers: Object.fromEntries(list.map((t) => [t.id, t])) })
    } catch {
      // An older daemon without file transfer.
    }
  },
  apply: (t) => {
    const before = get().transfers[t.id]
    set((s) => ({ transfers: { ...s.transfers, [t.id]: t } }))
    if (before?.state === t.state) return
    if (t.direction === 'in' && t.state === 'incoming') toast({ tone: 'info', title: tr('files.incoming', { name: t.peer_name ?? '?' }), body: t.name, who: t.peer_name }, 12000)
    if (t.state === 'done') toast({ tone: 'ok', title: t.direction === 'in' ? tr('files.received') : tr('files.sent'), body: t.name })
    if (t.state === 'failed') toast({ tone: 'error', title: tr('files.failed'), body: t.error ?? t.name }, 8000)
    if (t.direction === 'out' && t.state === 'declined') toast({ tone: 'warn', title: tr('files.declined', { name: t.peer_name ?? '?' }), body: t.name })
  },
  offer: async (roomId, peerId, path) => {
    try {
      const t = await daemonClient()?.request<FileTransfer>('file.offer', { room_id: roomId, peer_id: peerId, path })
      if (t) get().apply(t)
    } catch (e) {
      fail(e)
    }
  },
  respond: async (id, accept) => {
    try {
      await daemonClient()?.request('file.respond', { id, accept })
    } catch (e) {
      fail(e)
    }
  },
  cancel: async (id) => {
    try {
      await daemonClient()?.request('file.cancel', { id })
    } catch (e) {
      fail(e)
    }
  },
  reveal: async (path) => {
    try {
      await invoke('reveal_received', { path })
    } catch (e) {
      fail(e)
    }
  },
  dismiss: (id) =>
    set((s) => {
      const transfers = { ...s.transfers }
      delete transfers[id]
      return { transfers }
    }),
}))
