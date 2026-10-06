import { create } from 'zustand'

export type ToastTone = 'info' | 'ok' | 'warn' | 'error' | 'chat'

export interface Toast {
  id: number
  tone: ToastTone
  title: string
  body?: string
  /** Avatar name for chat/join toasts. */
  who?: string
  /**
   * What the toast is about ("join:<friend>", "peer:<name>", ...). A newer
   * toast with the same key replaces the one on screen instead of stacking,
   * so "sending → joining → connected" is one toast that changes.
   */
  key?: string
}

interface ToastState {
  toasts: Toast[]
  push: (t: Omit<Toast, 'id'>, ttlMs?: number) => void
  dismiss: (id: number) => void
}

/** An identical toast (same tone, title and body) is not shown again within this. */
const REPEAT_MS = 15_000

let seq = 0
const timers = new Map<number, ReturnType<typeof setTimeout>>()
const recent = new Map<string, number>() // fingerprint -> last shown

export const useToasts = create<ToastState>((set, get) => ({
  toasts: [],
  push: (t, ttlMs = 4500) => {
    const now = Date.now()
    const print = `${t.tone}|${t.title}|${t.body ?? ''}`
    const same = get().toasts.find((x) => (t.key && x.key === t.key) || (t.tone !== 'chat' && `${x.tone}|${x.title}|${x.body ?? ''}` === print))
    // Chat is exempt: somebody may really say "gg" twice.
    if (!same && t.tone !== 'chat' && (recent.get(print) ?? 0) > now - REPEAT_MS) return
    for (const [k, at] of recent) if (at < now - REPEAT_MS) recent.delete(k)
    recent.set(print, now)

    // Replace in place: same id, so it stays put instead of animating in again.
    const id = same?.id ?? ++seq
    clearTimeout(timers.get(id))
    timers.set(
      id,
      setTimeout(() => get().dismiss(id), ttlMs),
    )
    set({
      toasts: same ? get().toasts.map((x) => (x.id === id ? { ...t, id } : x)) : [...get().toasts.slice(-4), { ...t, id }],
    })
  },
  dismiss: (id) => {
    clearTimeout(timers.get(id))
    timers.delete(id)
    set({ toasts: get().toasts.filter((t) => t.id !== id) })
  },
}))

export function toast(t: Omit<Toast, 'id'>, ttlMs?: number): void {
  useToasts.getState().push(t, ttlMs)
}
