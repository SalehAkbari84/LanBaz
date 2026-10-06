import { create } from 'zustand'

export type ToastTone = 'info' | 'ok' | 'warn' | 'error' | 'chat'

export interface Toast {
  id: number
  tone: ToastTone
  title: string
  body?: string
  /** Avatar name for chat/join toasts. */
  who?: string
}

interface ToastState {
  toasts: Toast[]
  push: (t: Omit<Toast, 'id'>, ttlMs?: number) => void
  dismiss: (id: number) => void
}

let seq = 0

export const useToasts = create<ToastState>((set, get) => ({
  toasts: [],
  push: (t, ttlMs = 4500) => {
    const id = ++seq
    set({ toasts: [...get().toasts.slice(-4), { ...t, id }] })
    setTimeout(() => get().dismiss(id), ttlMs)
  },
  dismiss: (id) => set({ toasts: get().toasts.filter((t) => t.id !== id) }),
}))

export function toast(t: Omit<Toast, 'id'>, ttlMs?: number): void {
  useToasts.getState().push(t, ttlMs)
}
