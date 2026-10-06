/**
 * Chat store: messages per room, the room being viewed, and unread counts.
 * Fed by `chat.message` events; history is loaded once per room on connect.
 */

import { create } from 'zustand'

import { daemonClient } from './daemon'
import type { ChatMessage } from '../types/protocol'

interface ChatState {
  messages: Record<string, ChatMessage[]>
  /** Room whose chat is on screen, or null. */
  activeRoom: string | null
  /** Whether the chat page/panel is visible (for unread counting). */
  viewing: boolean
  unread: number
  sending: boolean
  error: string | null

  setActiveRoom: (roomId: string | null) => void
  setViewing: (v: boolean) => void
  receive: (m: ChatMessage) => void
  loadHistory: (roomId: string) => Promise<void>
  send: (roomId: string, text: string) => Promise<void>
  forgetRoom: (roomId: string) => void
  reset: () => void
}

const MAX = 300

export const useChatStore = create<ChatState>((set, get) => ({
  messages: {},
  activeRoom: null,
  viewing: false,
  unread: 0,
  sending: false,
  error: null,

  setActiveRoom: (roomId) => set({ activeRoom: roomId }),
  setViewing: (v) => set(v ? { viewing: true, unread: 0 } : { viewing: false }),

  receive: (m) => {
    const list = get().messages[m.room_id] ?? []
    if (list.some((x) => x.id === m.id)) return
    const next = [...list, m].slice(-MAX)
    set((s) => ({
      messages: { ...s.messages, [m.room_id]: next },
      unread: s.viewing || m.self ? s.unread : s.unread + 1,
      activeRoom: s.activeRoom ?? m.room_id,
    }))
  },

  loadHistory: async (roomId) => {
    const client = daemonClient()
    if (!client) return
    try {
      const history = await client.chatHistory(roomId)
      set((s) => ({ messages: { ...s.messages, [roomId]: (history ?? []).slice(-MAX) } }))
    } catch {
      // An older engine without chat: nothing to show.
    }
  },

  send: async (roomId, text) => {
    const client = daemonClient()
    const trimmed = text.trim()
    if (!client || !trimmed) return
    set({ sending: true, error: null })
    try {
      const m = await client.sendChat(roomId, trimmed)
      get().receive(m)
    } catch (err) {
      set({ error: err instanceof Error ? err.message : String(err) })
      throw err
    } finally {
      set({ sending: false })
    }
  },

  forgetRoom: (roomId) =>
    set((s) => {
      const messages = { ...s.messages }
      delete messages[roomId]
      return { messages, activeRoom: s.activeRoom === roomId ? null : s.activeRoom }
    }),

  reset: () => set({ messages: {}, activeRoom: null, unread: 0 }),
}))
