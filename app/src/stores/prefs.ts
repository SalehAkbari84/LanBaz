/**
 * UI preferences: language, overlay look and behaviour, notifications.
 *
 * These belong to this machine's UI, not to the daemon, so they live in
 * localStorage. Every access is guarded: storage can be unavailable or throw,
 * and the app must render with defaults either way.
 *
 * Both webviews (main and overlay) share the same origin and therefore the same
 * storage; a `storage` event keeps them in step when one changes a setting.
 */

import { create } from 'zustand'

export type Language = 'fa' | 'en'
export type Corner = 'top-right' | 'top-left' | 'bottom-right' | 'bottom-left'

export interface Prefs {
  language: Language
  overlayCorner: Corner
  overlayOpacity: number // 0.5 – 1
  overlayScale: number // 0.8 – 1.3
  toastJoinLeave: boolean
  toastChat: boolean
  toastGames: boolean
  /** Voice: push-to-talk instead of an open microphone. */
  voicePtt: boolean
  /** Windows virtual-key code of the push-to-talk key. */
  voicePttKey: number
  soundOnChat: boolean
  compactPlayers: boolean
}

const KEY = 'lanbaz.prefs.v1'

function defaultLanguage(): Language {
  try {
    const langs = navigator.languages?.length ? navigator.languages : [navigator.language]
    if (langs.some((l) => l?.toLowerCase().startsWith('fa'))) return 'fa'
  } catch {
    // fall through
  }
  return 'en'
}

export const DEFAULT_PREFS: Prefs = {
  language: defaultLanguage(),
  overlayCorner: 'top-right',
  overlayOpacity: 0.92,
  overlayScale: 1,
  toastJoinLeave: true,
  toastChat: true,
  toastGames: true,
  voicePtt: false,
  voicePttKey: 0x05,
  soundOnChat: false,
  compactPlayers: false,
}

function load(): Prefs {
  try {
    const raw = localStorage.getItem(KEY)
    if (!raw) return DEFAULT_PREFS
    return { ...DEFAULT_PREFS, ...(JSON.parse(raw) as Partial<Prefs>) }
  } catch {
    return DEFAULT_PREFS
  }
}

function save(p: Prefs): void {
  try {
    localStorage.setItem(KEY, JSON.stringify(p))
  } catch {
    // Not fatal: the preference simply does not survive a restart.
  }
}

interface PrefsState extends Prefs {
  set: (patch: Partial<Prefs>) => void
  reset: () => void
}

export const usePrefs = create<PrefsState>((set, get) => ({
  ...load(),
  set: (patch) => {
    const next = { ...pick(get()), ...patch }
    save(next)
    set(patch)
  },
  reset: () => {
    save(DEFAULT_PREFS)
    set(DEFAULT_PREFS)
  },
}))

function pick(s: PrefsState): Prefs {
  const { set: _s, reset: _r, ...rest } = s
  return rest
}

// Keep the other window in step.
if (typeof window !== 'undefined') {
  window.addEventListener('storage', (e) => {
    if (e.key === KEY) usePrefs.setState(load())
  })
}
