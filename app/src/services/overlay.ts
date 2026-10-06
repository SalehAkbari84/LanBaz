/**
 * Everything the webview needs to drive the in-game overlay window.
 *
 * The overlay is a second webview running this same app, so it shares the
 * stores, the daemon connection and the event stream with the main window. What
 * lives here is only the window plumbing: which surface am I, show or hide the
 * other one, and listen for the mode the shell decided on.
 */

import { invoke } from '@tauri-apps/api/core'
import { listen, type UnlistenFn } from '@tauri-apps/api/event'
import { getCurrentWindow } from '@tauri-apps/api/window'

/** Mirrors OverlayMode in src-tauri/src/overlay.rs. */
export type OverlayMode = 'hidden' | 'toast' | 'panel'

/** Mirrors OverlayStatus in src-tauri/src/overlay.rs. */
export interface OverlayStatus {
  registered: boolean
  keys: string
  chat_keys?: string
  error: string | null
}

/** Label of the floating window, matching tauri.conf.json. */
export const OVERLAY_LABEL = 'overlay'

/**
 * Which window this webview is.
 *
 * Read once at module load from Tauri's own metadata: the answer cannot change
 * for the lifetime of a webview, and asking the shell for it would mean an await
 * before the first render of every page.
 *
 * Returns null outside Tauri (a browser dev build), which is the same "not in
 * the shell" case the daemon store already handles.
 */
export function currentLabel(): string | null {
  if (typeof window === 'undefined' || !('__TAURI_INTERNALS__' in window)) {
    return null
  }
  try {
    return getCurrentWindow().label
  } catch {
    return null
  }
}

/** True when this webview is the floating overlay rather than the main window. */
export function isOverlaySurface(): boolean {
  return currentLabel() === OVERLAY_LABEL
}

/**
 * Shows the overlay.
 *
 * `panel` is the interactive one the hotkey produces. `toast` stays click-through
 * and never takes focus, so it can announce a peer joining without pulling the
 * user out of the game.
 */
export async function showOverlay(mode: 'panel' | 'toast' = 'panel'): Promise<void> {
  await invoke('set_overlay_visible', { visible: true, mode })
}

/** Hides the overlay. */
export async function hideOverlay(): Promise<void> {
  await invoke('set_overlay_visible', { visible: false, mode: null })
}

/** Whether the overlay is currently on screen. */
export async function overlayVisible(): Promise<boolean> {
  return invoke<boolean>('overlay_is_visible')
}

/** The hotkey binding, and whether it could actually be claimed. */
export async function overlayStatus(): Promise<OverlayStatus> {
  return invoke<OverlayStatus>('overlay_status')
}

/**
 * Subscribes to visibility changes of this overlay window.
 *
 * The shell emits this instead of the webview polling `isVisible` on a timer:
 * a poll loop would keep a second event loop alive for a value that changes a
 * handful of times per session.
 */
export function onOverlayMode(cb: (mode: OverlayMode) => void): Promise<UnlistenFn> {
  return listen<OverlayMode>('lanbaz://overlay-mode', (event) => cb(event.payload))
}

/** Subscribes to the hotkey failing to register, which is reported once. */
export function onHotkeyError(cb: (message: string) => void): Promise<UnlistenFn> {
  return listen<OverlayStatus>('lanbaz://hotkey', (event) => {
    if (event.payload?.error) cb(event.payload.error)
  })
}

/**
 * Announces something over the game, but only if the overlay is not already up.
 *
 * The guard matters: if the user has the panel open and is looking at it, a peer
 * joining is already visible, and switching the window to click-through toast
 * mode under their cursor would make the panel stop responding mid-click. A
 * notification that changes what the user is interacting with is worse than no
 * notification.
 *
 * Every failure here is swallowed. The overlay is a convenience on top of a
 * working VPN; a peer joining must not be blocked, or reported as an error,
 * because a floating window could not be shown.
 */
export async function announce(): Promise<void> {
  if (currentLabel() === null) return
  try {
    if (await overlayVisible()) return
    await showOverlay('toast')
  } catch {
    // Nothing to do: the user will see the peer in the tray app.
  }
}
/** Positions and sizes the overlay window (corner of the primary monitor). */
export async function setOverlayLayout(corner: string, scale: number): Promise<void> {
  await invoke('set_overlay_layout', { corner, scale })
}

/** Fires when the chat hotkey asks the overlay to focus its chat box. */
export function onOverlayChat(cb: () => void): Promise<UnlistenFn> {
  return listen('lanbaz://overlay-chat', () => cb())
}
