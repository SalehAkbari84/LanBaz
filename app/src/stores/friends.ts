import { create } from 'zustand'

import { translate } from '../i18n'
import type { DaemonClient } from '../services/daemon-client'
import { FRIEND_EVENT, type Friend, type FriendEvent, type FriendsList, type JoinPrompt, type JoinStatus } from '../types/friends'
import { daemonClient } from './daemon'
import { usePrefs } from './prefs'
import { toast } from './toasts'

interface FriendsState {
  code: string
  enabled: boolean
  friends: Friend[]
  prompts: JoinPrompt[]
  busy: boolean
  load: () => Promise<void>
  add: (code: string) => Promise<void>
  respond: (pub: string, accept: boolean) => Promise<void>
  remove: (pub: string) => Promise<void>
  trust: (pub: string, trusted: boolean) => Promise<void>
  join: (pub: string, roomId?: string) => Promise<void>
  invite: (pub: string, roomId?: string) => Promise<void>
  answer: (id: string, accept: boolean, always?: boolean) => Promise<void>
  reset: () => void
}

function client(): DaemonClient {
  const c = daemonClient()
  if (!c) throw new Error(translate(usePrefs.getState().language, 'friends.disabled'))
  return c
}

const t = (key: Parameters<typeof translate>[1], vars?: Record<string, string | number>) =>
  translate(usePrefs.getState().language, key, vars)

function upsert(list: Friend[], f: Friend, removed: boolean): Friend[] {
  const rest = list.filter((x) => x.pub !== f.pub)
  return removed ? rest : [...rest, f].sort((a, b) => Number(b.state === 'friend') - Number(a.state === 'friend') || a.name.localeCompare(b.name))
}

export const useFriendsStore = create<FriendsState>((set, get) => ({
  code: '',
  enabled: false,
  friends: [],
  prompts: [],
  busy: false,

  load: async () => {
    try {
      const l = await client().request<FriendsList>('friends.list')
      set({ code: l.code, enabled: l.enabled, friends: l.friends })
    } catch {
      set({ enabled: false })
    }
  },
  add: async (code) => {
    set({ busy: true })
    try {
      const f = await client().request<Friend>('friends.add', { code }, 30_000)
      set({ friends: upsert(get().friends, f, false) })
      toast({ tone: 'ok', title: t('friends.requestSent'), who: f.name || f.code })
    } finally {
      set({ busy: false })
    }
  },
  respond: async (pub, accept) => {
    const f = await client().request<Friend>('friends.respond', { pub, accept }, 30_000)
    set({ friends: upsert(get().friends, f, !accept) })
  },
  remove: async (pub) => {
    await client().request('friends.remove', { pub }, 30_000)
    set({ friends: get().friends.filter((x) => x.pub !== pub) })
  },
  trust: async (pub, trusted) => {
    const f = await client().request<Friend>('friends.trust', { pub, trusted })
    set({ friends: upsert(get().friends, f, false) })
  },
  join: async (pub, roomId) => {
    await client().request('join.request', { pub, room_id: roomId }, 30_000)
  },
  invite: async (pub, roomId) => {
    await client().request('join.invite', { pub, room_id: roomId }, 60_000)
  },
  answer: async (id, accept, always = false) => {
    set({ prompts: get().prompts.filter((p) => p.id !== id) })
    await client().request('join.respond', { id, accept, always }, 60_000)
  },
  reset: () => set({ friends: [], prompts: [], code: '', enabled: false }),
}))

/** Subscribes the friends store to the daemon's events. */
export function wireFriendEvents(c: DaemonClient): void {
  c.on<FriendEvent>(FRIEND_EVENT.update, (e) => {
    const removed = e.why === 'removed' || e.why === 'declined'
    useFriendsStore.setState((s) => ({ friends: upsert(s.friends, e.friend, removed) }))
    if (e.why === 'request') toast({ tone: 'info', title: t('friends.incoming'), body: e.friend.name, who: e.friend.name }, 8000)
    if (e.why === 'accepted') toast({ tone: 'ok', title: t('friends.nowFriends', { name: e.friend.name }), who: e.friend.name })
  })
  c.on<JoinPrompt>(FRIEND_EVENT.prompt, (p) => {
    useFriendsStore.setState((s) => ({ prompts: [...s.prompts.filter((x) => x.id !== p.id), p] }))
  })
  c.on<JoinStatus>(FRIEND_EVENT.status, (s) => {
    const name = s.friend.name
    const key = `join:${s.friend.pub}`
    switch (s.status) {
      case 'sent':
        toast({ tone: 'info', title: t('join.sent', { name }), who: name, key })
        break
      case 'connected':
        toast({ tone: 'ok', title: t('join.connected', { name }), who: name, key })
        break
      case 'joining':
        toast({ tone: 'info', title: t('join.joining', { name }), who: name, key })
        break
      case 'reconnecting':
        toast({ tone: 'warn', title: t('join.reconnecting', { name }), who: name, key })
        break
      case 'rejected':
        toast({ tone: 'warn', title: t('join.rejected', { name }), body: s.message, who: name, key }, 7000)
        break
      case 'failed':
        toast({ tone: 'error', title: t('join.failed', { name }), body: s.message, who: name, key }, 9000)
        break
    }
  })
}
