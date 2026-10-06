import { listen } from '@tauri-apps/api/event'
import { motion } from 'framer-motion'
import { Activity, ChevronDown, DoorOpen, Gauge, Layers, Plus, Power, ShieldAlert, Users, Wifi } from 'lucide-react'
import { useEffect, useMemo, useState, type ReactNode } from 'react'

import { Avatar, Banner, Card, CopyButton, EmptyState, PageHeader, PingBadge, SectionTitle, StateDot } from '../components/ui'
import { useNumber, useT } from '../i18n'
import { isTauri } from '../services/tauri'
import { useDaemonStore } from '../stores/daemon'
import { useGamesStore } from '../stores/games'
import { useRoomsStore } from '../stores/rooms'
import { isUsable, peerKey, type PeerSummary, type RoomSummary } from '../types/protocol'

export function HomePage() {
  const t = useT()
  const num = useNumber()
  const connection = useDaemonStore((s) => s.connection)
  const connectionError = useDaemonStore((s) => s.connectionError)
  const elevated = useDaemonStore((s) => s.elevated)
  const shutdownNotice = useDaemonStore((s) => s.shutdownNotice)
  const connect = useDaemonStore((s) => s.connect)
  const busy = useDaemonStore((s) => s.busy)
  const setPage = useDaemonStore((s) => s.setPage)
  const openRooms = useDaemonStore((s) => s.openRooms)

  const rooms = useRoomsStore((s) => s.rooms)
  const order = useRoomsStore((s) => s.order)
  const networks = useRoomsStore((s) => s.networks)

  const list = useMemo(() => order.map((id) => rooms[id]).filter((r): r is RoomSummary => Boolean(r)), [order, rooms])
  const players = useMemo(
    () => list.flatMap((room) => (room.peers ?? []).filter((p) => !p.is_self).map((p) => ({ room, peer: p }))),
    [list],
  )
  const online = players.filter(({ peer }) => isUsable(peer.state))
  const pings = online.filter(({ peer }) => peer.round_trips > 0).map(({ peer }) => peer.rtt_ms)
  const best = pings.length ? Math.min(...pings) : 0
  const primary = list[0]
  const primaryNet = primary ? networks[primary.room_id] : null
  const connected = connection === 'connected'

  return (
    <>
      <PageHeader title={t('home.title')} subtitle={t('home.subtitle')} />

      <SupervisionNotice />
      {elevated === false ? (
        <Banner tone="error">
          <span className="inline-flex items-start gap-2">
            <ShieldAlert size={16} className="mt-0.5 shrink-0" />
            {t('home.notAdmin')}
          </span>
        </Banner>
      ) : null}
      {shutdownNotice ? <Banner tone="warn">{t('home.stopped')}</Banner> : null}

      {!connected ? (
        <Card>
          <EmptyState
            icon={<Power size={26} />}
            title={connection === 'connecting' || connection === 'reconnecting' ? t('conn.waiting') : t('conn.offlineHint')}
            hint={connectionError ?? undefined}
          >
            <button type="button" className="btn btn-primary" onClick={() => void connect()} disabled={busy}>
              <Power size={16} /> {t('conn.start')}
            </button>
          </EmptyState>
        </Card>
      ) : list.length === 0 ? (
        <Card>
          <EmptyState icon={<Layers size={26} />} title={t('home.notInRoom')} hint={t('home.notInRoomHint')}>
            <button type="button" className="btn btn-primary" onClick={openRooms}>
              <Plus size={16} /> {t('home.hostRoom')}
            </button>
            <button type="button" className="btn" onClick={openRooms}>
              <DoorOpen size={16} /> {t('home.joinRoom')}
            </button>
          </EmptyState>
        </Card>
      ) : (
        <>
          <div className="grid grid-cols-1 gap-4 md:grid-cols-4">
            <motion.div layout className="glass relative col-span-1 overflow-hidden p-5 md:col-span-2">
              <div className="absolute -end-10 -top-10 h-40 w-40 rounded-full bg-accent/20 blur-3xl" />
              <div className="relative flex items-center justify-between gap-3">
                <div className="min-w-0">
                  <div className="label">{t('home.myAddress')}</div>
                  <div className="ltr mt-1 font-mono text-2xl font-semibold text-white">{primary?.local_address ?? '—'}</div>
                  <div className="mt-2 flex items-center gap-2 text-xs">
                    <span className={`h-2 w-2 rounded-full ${primaryNet?.state === 'ready' ? 'bg-status-running' : 'animate-pulse-dot bg-status-stopping'}`} />
                    <span className="text-slate-300">{primaryNet?.state === 'ready' ? t('home.lanReady') : t('home.lanNotReady')}</span>
                    <span className="text-slate-600">·</span>
                    <span className="truncate text-slate-400">{primary?.name}</span>
                  </div>
                </div>
                {primary?.local_address ? <CopyButton value={primary.local_address} className="btn" /> : null}
              </div>
              {primaryNet?.note ? <p className="relative mt-3 text-xs text-amber-200/90">{primaryNet.note}</p> : null}
            </motion.div>
            <Stat icon={<Users size={18} />} label={t('home.playersOnline')} value={num(online.length)} />
            <Stat icon={<Gauge size={18} />} label={t('home.bestPing')} value={best > 0 ? `${num(Math.round(best))} ms` : '—'} />
          </div>

          <Card>
            <SectionTitle icon={<Activity size={16} />} extra={<span className="chip">{t('home.rooms')}: {num(list.length)}</span>}>
              {t('home.players')}
            </SectionTitle>
            {players.length === 0 ? (
              <p className="py-6 text-center text-sm text-slate-500">{t('home.noPlayers')}</p>
            ) : (
              <ul className="flex flex-col divide-y divide-white/[0.05]">
                {players.map(({ room, peer }) => (
                  <PlayerLine key={`${room.room_id}-${peerKey(peer)}`} peer={peer} roomName={list.length > 1 ? room.name : undefined} />
                ))}
              </ul>
            )}
            <div className="mt-4 flex gap-2">
              <button type="button" className="btn btn-sm" onClick={openRooms}>
                <Layers size={14} /> {t('nav.rooms')}
              </button>
              <button type="button" className="btn btn-sm" onClick={() => setPage('chat')}>
                {t('nav.chat')}
              </button>
            </div>
          </Card>
        </>
      )}

      <EngineDetails />
    </>
  )
}

function Stat({ icon, label, value }: { icon: ReactNode; label: string; value: string }) {
  return (
    <div className="glass flex flex-col justify-between p-5">
      <div className="flex items-center gap-2 text-slate-400">
        <span className="text-accent-2">{icon}</span>
        <span className="label">{label}</span>
      </div>
      <div className="ltr mt-3 text-3xl font-semibold tabular-nums text-white">{value}</div>
    </div>
  )
}

function PlayerLine({ peer, roomName }: { peer: PeerSummary; roomName?: string }) {
  const t = useT()
  const presence = useGamesStore((s) => s.presence[peer.peer_id])
  const name = peer.display_name || t('rooms.connecting')
  return (
    <li className="flex items-center gap-3 py-3">
      <Avatar name={peer.display_name || peer.peer_id || '?'} />
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="truncate text-sm font-medium text-slate-100">{name}</span>
          {peer.is_host ? <span className="chip px-2 py-0 text-[10px]">{t('common.host')}</span> : null}
          {roomName ? <span className="truncate text-xs text-slate-500">· {roomName}</span> : null}
        </div>
        <div className="mt-0.5 flex items-center gap-3">
          <StateDot state={peer.state} />
          {presence?.game_name ? (
            <span className="truncate text-xs text-accent-2">
              {presence.game_name}
              {presence.hosting ? ` · ${t('games.hosting')}` : ''}
            </span>
          ) : null}
        </div>
      </div>
      {peer.virtual_address ? (
        <span className="ltr hidden items-center gap-1 font-mono text-xs text-slate-400 sm:inline-flex">
          {peer.virtual_address}
          <CopyButton value={peer.virtual_address} iconOnly className="rounded-md p-1 text-slate-500 hover:bg-white/10 hover:text-slate-200" />
        </span>
      ) : null}
      <span className="w-20 text-xs text-slate-500">{t(`link.${peer.link_kind}` as never)}</span>
      <PingBadge ms={peer.rtt_ms} measured={peer.round_trips > 0 && isUsable(peer.state)} />
    </li>
  )
}

/** Engine status, version and the stop button, collapsed by default. */
function EngineDetails() {
  const t = useT()
  const status = useDaemonStore((s) => s.status)
  const connection = useDaemonStore((s) => s.connection)
  const shutdown = useDaemonStore((s) => s.shutdown)
  const busy = useDaemonStore((s) => s.busy)
  const [open, setOpen] = useState(false)
  if (connection !== 'connected' || !status) return null
  return (
    <Card className="p-0">
      <button type="button" className="flex w-full items-center justify-between px-5 py-4 text-sm text-slate-300" onClick={() => setOpen((v) => !v)}>
        <span className="flex items-center gap-2">
          <Wifi size={16} className="text-accent" /> {t('home.engineDetails')}
        </span>
        <ChevronDown size={16} className={`transition-transform ${open ? 'rotate-180' : ''}`} />
      </button>
      {open ? (
        <div className="grid grid-cols-1 gap-x-8 gap-y-3 border-t border-white/[0.06] px-5 py-4 text-sm sm:grid-cols-2">
          <Kv k={t('home.version')} v={status.version} />
          <Kv k={t('home.uptime')} v={formatUptime(status.uptime_seconds)} />
          <Kv k={t('home.api')} v={status.api_listen} />
          <Kv k={t('home.stateDir')} v={status.state_dir} />
          <div className="sm:col-span-2">
            <button
              type="button"
              className="btn btn-danger btn-sm"
              disabled={busy}
              onClick={() => {
                if (window.confirm(t('home.stopConfirm'))) void shutdown()
              }}
            >
              <Power size={14} /> {t('home.stopEngine')}
            </button>
          </div>
        </div>
      ) : null}
    </Card>
  )
}

function Kv({ k, v }: { k: string; v: string }) {
  return (
    <div className="min-w-0">
      <div className="label">{k}</div>
      <div className="ltr mt-0.5 break-all font-mono text-xs text-slate-300">{v}</div>
    </div>
  )
}

function formatUptime(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return '—'
  const total = Math.floor(seconds)
  const h = Math.floor(total / 3600)
  const m = Math.floor((total % 3600) / 60)
  const s = total % 60
  return h > 0 ? `${h}h ${m}m` : m > 0 ? `${m}m ${s}s` : `${s}s`
}

interface SupervisionEvent {
  state: 'restarting' | 'started' | 'given_up'
  attempt: number
}

/** "The engine crashed and is coming back" — shown only while it matters. */
function SupervisionNotice() {
  const t = useT()
  const [event, setEvent] = useState<SupervisionEvent | null>(null)
  useEffect(() => {
    if (!isTauri()) return
    let off: (() => void) | undefined
    let disposed = false
    void listen<SupervisionEvent>('lanbaz://daemon-supervision', (e) => {
      if (disposed) return
      setEvent(e.payload)
      if (e.payload.state === 'started') setTimeout(() => setEvent(null), 4000)
    }).then((u) => {
      if (disposed) u()
      else off = u
    })
    return () => {
      disposed = true
      off?.()
    }
  }, [])
  if (!event) return null
  return event.state === 'started' ? (
    <Banner tone="ok">{t('home.restarted')}</Banner>
  ) : (
    <Banner tone="warn">{t('home.restarting', { n: event.attempt })}</Banner>
  )
}
