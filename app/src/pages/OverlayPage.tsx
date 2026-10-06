/**
 * The in-game overlay: a compact HUD over the game, never injected into it.
 *
 *   * `panel` (Ctrl+Alt+L): players with ping, who plays what, your address,
 *     and the room chat. Ctrl+Alt+C opens it with the chat box focused.
 *   * `toast`: click-through notifications (join/leave/chat) that never take
 *     focus from the game and hide themselves.
 *
 * The window is exactly the size of the panel (see set_overlay_layout), so no
 * transparent area around it can swallow a click meant for the game.
 */

import { AnimatePresence, motion } from 'framer-motion'
import { Crown, MessageSquare, Mic, Network, Users, X } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'

import { Toaster } from '../components/Toaster'
import { Avatar, CopyButton, PingBadge } from '../components/ui'
import { useOverlayVoice } from '../services/bridges'
import { useT } from '../i18n'
import { hideOverlay, onOverlayChat, onOverlayMode, overlayStatus, setOverlayLayout, type OverlayMode } from '../services/overlay'
import { useDaemonStore } from '../stores/daemon'
import { useGamesStore } from '../stores/games'
import { usePrefs } from '../stores/prefs'
import { sortPeers, useRoomsStore } from '../stores/rooms'
import { useToasts } from '../stores/toasts'
import { isUsable, peerKey, type PeerSummary, type RoomSummary } from '../types/protocol'
import { ChatPanel } from './ChatPage'

export function OverlayPage() {
  const t = useT()
  const connection = useDaemonStore((s) => s.connection)
  const rooms = useRoomsStore((s) => s.rooms)
  const order = useRoomsStore((s) => s.order)
  const corner = usePrefs((s) => s.overlayCorner)
  const scale = usePrefs((s) => s.overlayScale)
  const opacity = usePrefs((s) => s.overlayOpacity)
  const toasts = useToasts((s) => s.toasts)
  const list = useMemo(() => order.map((id) => rooms[id]).filter((r): r is RoomSummary => Boolean(r)), [order, rooms])

  const [mode, setMode] = useState<OverlayMode>('panel')
  const [tab, setTab] = useState<'players' | 'chat'>('players')
  const [chatFocus, setChatFocus] = useState(0)
  const [keys, setKeys] = useState('Ctrl+Alt+L')

  useEffect(() => {
    let a: (() => void) | undefined
    let b: (() => void) | undefined
    void onOverlayMode(setMode).then((u) => (a = u))
    void onOverlayChat(() => {
      setTab('chat')
      setChatFocus((n) => n + 1)
    }).then((u) => (b = u))
    void overlayStatus()
      .then((s) => setKeys(s.keys))
      .catch(() => undefined)
    return () => {
      a?.()
      b?.()
    }
  }, [])

  // Keep the window where the user wants it.
  useEffect(() => {
    void setOverlayLayout(corner, scale).catch(() => undefined)
  }, [corner, scale])

  // A toast leaves once there is nothing left to say.
  useEffect(() => {
    if (mode !== 'toast') return
    const timer = setTimeout(() => {
      if (useToasts.getState().toasts.length === 0) void hideOverlay()
    }, 5500)
    return () => clearTimeout(timer)
  }, [mode, toasts.length])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault()
        void hideOverlay()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  if (mode === 'toast') {
    return (
      <div className="h-full" style={{ opacity }}>
        <Toaster compact />
      </div>
    )
  }

  const room = list[0] ?? null

  return (
    <div className="h-full p-1.5" style={{ opacity }}>
      <motion.div
        initial={{ opacity: 0, scale: 0.97, y: -6 }}
        animate={{ opacity: 1, scale: 1, y: 0 }}
        transition={{ type: 'spring', stiffness: 420, damping: 32 }}
        className="flex h-full flex-col overflow-hidden rounded-2xl border border-white/10 bg-[#0b0e17]/90 shadow-2xl backdrop-blur-2xl"
      >
        <header className="flex items-center gap-2 border-b border-white/[0.06] bg-accent-gradient/10 px-3 py-2.5">
          <div className="grid h-7 w-7 place-items-center rounded-lg bg-accent-gradient">
            <Network size={14} className="text-white" />
          </div>
          <div className="min-w-0 flex-1">
            <div className="truncate text-sm font-semibold text-white">{room?.name ?? t('overlay.title')}</div>
            <div className="text-[10px] text-slate-500">{t('overlay.hotkeyHint', { keys })}</div>
          </div>
          <span className={`h-2 w-2 rounded-full ${connection === 'connected' ? 'bg-status-running' : 'animate-pulse-dot bg-status-stopping'}`} />
          <button type="button" className="rounded-md p-1 text-slate-400 hover:bg-white/10 hover:text-white" onClick={() => void hideOverlay()} aria-label="close">
            <X size={15} />
          </button>
        </header>

        {!room ? (
          <div className="grid flex-1 place-items-center px-6 text-center text-sm text-slate-400">
            {connection === 'connected' ? t('overlay.noRoom') : t('conn.waiting')}
          </div>
        ) : (
          <>
            <div className="flex items-center gap-2 px-3 pt-3">
              <div className="flex-1">
                <div className="text-[10px] text-slate-500">{t('home.myAddress')}</div>
                <div className="ltr font-mono text-sm text-white">{room.local_address}</div>
              </div>
              {room.local_address ? <CopyButton value={room.local_address} className="btn btn-sm" /> : null}
            </div>

            <div className="mx-3 mt-3 flex rounded-xl border border-white/10 bg-white/[0.03] p-1">
              {(['players', 'chat'] as const).map((k) => (
                <button
                  key={k}
                  type="button"
                  className={`relative flex flex-1 items-center justify-center gap-1.5 rounded-lg py-1.5 text-xs ${tab === k ? 'text-white' : 'text-slate-400'}`}
                  onClick={() => setTab(k)}
                >
                  {tab === k ? <motion.span layoutId="ov-tab" className="absolute inset-0 rounded-lg bg-accent/25" /> : null}
                  <span className="relative flex items-center gap-1.5">
                    {k === 'players' ? <Users size={13} /> : <MessageSquare size={13} />}
                    {k === 'players' ? t('rooms.players') : t('chat.title')}
                  </span>
                </button>
              ))}
            </div>

            <div className="min-h-0 flex-1 px-3 py-3">
              <AnimatePresence mode="wait">
                {tab === 'players' ? (
                  <motion.ul key="p" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} className="flex h-full flex-col gap-1 overflow-y-auto">
                    {sortPeers(room.peers ?? []).map((p) => (
                      <PlayerLine key={peerKey(p)} peer={p} />
                    ))}
                  </motion.ul>
                ) : (
                  <motion.div key="c" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} className="h-full">
                    <ChatPanel roomId={room.room_id} compact autoFocus={chatFocus > 0} key={chatFocus} />
                  </motion.div>
                )}
              </AnimatePresence>
            </div>
          </>
        )}
      </motion.div>
    </div>
  )
}

function PlayerLine({ peer }: { peer: PeerSummary }) {
  const talking = useOverlayVoice((v) => (peer.is_self ? v.self : v.speaking.includes(peer.peer_id)))
  const t = useT()
  const presence = useGamesStore((s) => (peer.is_self ? s.presence[''] : s.presence[peer.peer_id]))
  const name = peer.display_name || (peer.peer_id ? peer.peer_id.slice(0, 8) : '…')
  return (
    <li className="flex items-center gap-2.5 rounded-lg px-2 py-1.5 hover:bg-white/[0.04]">
      <div className="relative">
        <Avatar name={peer.display_name || peer.peer_id || '?'} size={28} />
        <span
          className={`absolute -bottom-0.5 -end-0.5 h-2.5 w-2.5 rounded-full border-2 border-[#0b0e17] ${
            peer.is_self || isUsable(peer.state) ? 'bg-status-running' : 'bg-status-stopping'
          }`}
        />
      </div>
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-1 truncate text-xs font-medium text-slate-100">
          {name}
          {peer.is_self ? <span className="text-slate-500">({t('common.you')})</span> : null}
          {peer.is_host ? <Crown size={11} className="text-amber-300" /> : null}
        </div>
        <div className="truncate text-[10px] text-slate-500">
          {presence?.game_name ? <span className="text-accent-2">{presence.game_name}</span> : <span className="ltr">{peer.virtual_address ?? ''}</span>}
        </div>
      </div>
      {talking ? <Mic size={13} className="shrink-0 animate-pulse text-emerald-400" aria-label={t('voice.talking')} /> : null}
      {peer.is_self ? null : <PingBadge ms={peer.rtt_ms} measured={peer.round_trips > 0 && isUsable(peer.state)} />}
    </li>
  )
}
