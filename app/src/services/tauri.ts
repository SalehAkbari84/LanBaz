/**
 * Typed wrappers around the Tauri commands exposed by the Rust shell.
 *
 * The webview never guesses the daemon address: it asks the shell, which reads
 * <state-dir>/daemon.json. The token therefore lives in Rust, is handed over
 * once at connect time, and is never persisted in the UI.
 */

import { invoke } from '@tauri-apps/api/core'

export interface DaemonEndpoint {
  url: string
  token: string
  pid: number
  version: string
  stateDir: string
}

export interface DaemonStateDir {
  path: string
}

/** True when running inside the Tauri shell rather than a plain browser. */
export function isTauri(): boolean {
  return typeof window !== 'undefined' && '__TAURI_INTERNALS__' in window
}

/**
 * Returns the daemon control API endpoint. Throws when no daemon is running, so
 * the UI can start one through startDaemon first.
 */
export async function daemonEndpoint(): Promise<DaemonEndpoint> {
  return invoke<DaemonEndpoint>('daemon_endpoint')
}

/** Starts the daemon sidecar and waits until it is ready. */
export async function startDaemon(): Promise<void> {
  await invoke<void>('start_daemon')
}

/** Asks the daemon to stop gracefully. */
export async function stopDaemon(): Promise<void> {
  await invoke<void>('stop_daemon')
}

/** Returns the daemon state directory path. */
export async function daemonStateDir(): Promise<string> {
  const result = await invoke<DaemonStateDir>('daemon_state_dir')
  return result.path
}

/** Whether the shell runs with administrator rights. */
export async function isElevated(): Promise<boolean> {
  const result = await invoke<{ elevated: boolean }>('is_elevated')
  return result.elevated
}

/** Whether LanBaz starts at Windows logon (a scheduled task). */
export async function autostartIsEnabled(): Promise<boolean> {
  return invoke<boolean>('autostart_is_enabled')
}

/** Turns start-at-logon on or off; resolves with the resulting state. */
export async function setAutostart(enabled: boolean): Promise<boolean> {
  return invoke<boolean>('autostart_set', { enabled })
}

/** A code that arrived from a lanbaz:// link or .lanbaz file before we listened. */
export async function takePendingInvite(): Promise<string | null> {
  if (!isTauri()) return null
  return (await invoke<string | null>('take_pending_invite')) ?? null
}

/** Plain text currently on the Windows clipboard, or null. */
export async function readClipboardText(): Promise<string | null> {
  if (!isTauri()) return null
  try {
    return (await invoke<string | null>('read_clipboard_text')) ?? null
  } catch {
    return null
  }
}

/** Saves a code as a .lanbaz file in Downloads and reveals it; returns the path. */
export async function saveInviteFile(code: string, label: string): Promise<string> {
  return invoke<string>('save_invite_file', { code, label })
}

/** Codes opened from a link or file while LanBaz is running. */
export async function onInvite(cb: (code: string) => void): Promise<() => void> {
  if (!isTauri()) return () => undefined
  const { listen } = await import('@tauri-apps/api/event')
  return listen<{ code: string }>('lanbaz://invite', (e) => cb(e.payload.code))
}

/** Opens a game's join link (e.g. steam://connect/ip:port). Only game schemes are allowed. */
export async function openGameUri(uri: string): Promise<void> {
  await invoke('open_game_uri', { uri })
}

/** Installs the bundled TAP-Windows driver for Classic LAN rooms. */
export async function installTapDriver(): Promise<string> {
  return invoke<string>('install_tap_driver')
}

/** True when the code runs outside Tauri (unit tests, vite dev in a browser). */
export function isMockMode(): boolean {
  return !isTauri()
}