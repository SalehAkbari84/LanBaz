/**
 * Rooms: host or join, the invite → reply flow as two numbered steps, and the
 * live player list. Every value comes from the rooms store, which is fed by
 * daemon events.
 */

import { AnimatePresence, motion } from 'framer-motion'
import {
  ClipboardPaste,
  Crown,
  DoorOpen,
  FileDown,
  Link2,
  LogOut,
  Pin,
  Plus,
  RefreshCw,
  Send,
  Sparkles,
  Trash2,
  UserPlus,
  Users,
  X,
  Zap,
} from 'lucide-react'
import { useEffect, useMemo, useState, type ReactNode } from 'react'

import { Avatar, Banner, Card, CopyButton, EmptyState, PageHeader, PingBadge, SectionTitle, Segmented, Sparkline, StateDot, useNow, writeClipboard } from '../components/ui'
import { TapDriverNotice } from '../components/TapDriverNotice'
import { VoicePanel } from '../components/VoicePanel'
import { FilesPanel, useFileDrop } from '../components/FilesPanel'
import { useNumber, useT } from '../i18n'
import { readClipboardText, saveInviteFile } from '../services/tauri'
import { useDaemonStore } from '../stores/daemon'
import { useFriendsStore } from '../stores/friends'
import { useGamesStore } from '../stores/games'
import { useKept } from '../stores/kept'
import { qualityKey, useQuality } from '../stores/quality'
import { sortPeers, useRoomsStore, type RoomAction } from '../stores/rooms'
import { toast } from '../stores/toasts'
import { isUsable, peerKey, validTime, type NetworkStatus, type PeerSummary, type RoomSummary } from '../types/protocol'

const DEFAULT_MAX_PEERS = 8
const TTL_CHOICES = [10, 30, 60]
const EMPTY: number[] = []

/** The LAN ping limit of the game running on this PC, if it has one. */
function useGameLimit(): number | undefined {
  const gameId = useGamesStore((s) => s.presence['']?.game_id)
  const library = useGamesStore((s) => s.library)
  return gameId ? library.find((g) => g.id === gameId)?.max_lan_rtt_ms : undefined
}

/** Warns when a player is above the running game's LAN ping limit. */
function GameLimitBanner({ room }: { room: RoomSummary }) {
  const t = useT()
  const limit = useGameLimit()
  const game = useGamesStore((s) => s.presence['']?.game_name)
  if (!limit) return null
  const slow = (room.peers ?? []).filter((p) => !p.is_self && p.round_trips > 0 && isUsable(p.state) && p.rtt_ms > limit)
  if (!slow.length) return null
  return (
    <Banner tone="warn">
      {t('rooms.gameLimit', { game: game ?? '', limit, names: slow.map((p) => `${p.display_name || p.peer_id.slice(0, 8)} (${Math.round(p.rtt_ms)} ms)`).join('، ') })}
    </Banner>
  )
}

export function RoomsPage() {
  const t = useT()
  useFileDrop(t('files.dropWhere'))
  const rooms = useRoomsStore((s) => s.rooms)
  const order = useRoomsStore((s) => s.order)
  const networks = useRoomsStore((s) => s.networks)
  const busy = useRoomsStore((s) => s.busy)
  const lastError = useRoomsStore((s) => s.lastError)
  const clearError = useRoomsStore((s) => s.clearError)
  const notice = useRoomsStore((s) => s.notice)
  const clearNotice = useRoomsStore((s) => s.clearNotice)
  const connected = useDaemonStore((s) => s.connection === 'connected')

  const list = useMemo(() => order.map((id) => rooms[id]).filter((r): r is RoomSummary => Boolean(r)), [order, rooms])

  return (
    <>
      <PageHeader title={t('rooms.title')} subtitle={t('rooms.subtitle')} />

      {lastError ? (
        <Banner tone="error" onClose={clearError}>
          {lastError}
        </Banner>
      ) : null}
      {notice ? (
        <Banner tone="warn" onClose={clearNotice}>
          {notice}
        </Banner>
      ) : null}
      {!connected ? <Banner tone="warn">{t('conn.offlineHint')}</Banner> : null}

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <HostCard disabled={!connected} busy={busy} />
        <JoinCard disabled={!connected} busy={busy} />
      </div>

      {list.length === 0 ? (
        <Card>
          <EmptyState icon={<Users size={26} />} title={t('rooms.empty')} />
        </Card>
      ) : (
        <AnimatePresence initial={false}>
          {list.map((room) => (
            <motion.div key={room.room_id} layout initial={{ opacity: 0, y: 10 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0, scale: 0.98 }}>
              <RoomCard room={room} network={networks[room.room_id] ?? null} busy={busy} />
            </motion.div>
          ))}
        </AnimatePresence>
      )}
    </>
  )
}

// -------------------------------------------------------------------- host --

function HostCard({ disabled, busy }: { disabled: boolean; busy: RoomAction | null }) {
  const t = useT()
  const num = useNumber()
  const createRoom = useRoomsStore((s) => s.createRoom)
  const [name, setName] = useState('')
  const [seats, setSeats] = useState(DEFAULT_MAX_PEERS)
  const [ttl, setTtl] = useState(30)
  const defaultMode = useDaemonStore((s) => s.settings?.network?.room_mode ?? 'l3')
  const [mode, setMode] = useState<'l3' | 'l2' | null>(null)
  const effectiveMode = mode ?? defaultMode
  const working = busy?.kind === 'create'

  const submit = async () => {
    try {
      await createRoom({ name: name.trim() || undefined, max_peers: seats, pairing_ttl_seconds: ttl * 60, mode: effectiveMode })
      setName('')
    } catch {
      // Shown in the banner.
    }
  }

  return (
    <Card>
      <SectionTitle icon={<Crown size={16} />}>{t('rooms.host.title')}</SectionTitle>
      <p className="mb-4 text-sm text-slate-400">{t('rooms.host.hint')}</p>
      <div className="flex flex-col gap-3">
        <label className="flex flex-col gap-1.5">
          <span className="label">{t('rooms.host.name')}</span>
          <input
            className="input"
            value={name}
            maxLength={48}
            placeholder={t('rooms.host.namePlaceholder')}
            onChange={(e) => setName(e.target.value)}
            onKeyDown={(e) => e.key === 'Enter' && void submit()}
            disabled={working}
          />
        </label>
        <div className="grid grid-cols-2 gap-3">
          <label className="flex flex-col gap-1.5">
            <span className="label">{t('rooms.host.seats')}</span>
            <select className="input" value={seats} onChange={(e) => setSeats(Number(e.target.value))} disabled={working}>
              {[2, 4, 6, 8, 12, 16, 24, 32].map((n) => (
                <option key={n} value={n}>
                  {num(n)}
                </option>
              ))}
            </select>
          </label>
          <label className="flex flex-col gap-1.5">
            <span className="label">{t('rooms.host.validFor')}</span>
            <select className="input" value={ttl} onChange={(e) => setTtl(Number(e.target.value))} disabled={working}>
              {TTL_CHOICES.map((m) => (
                <option key={m} value={m}>
                  {t('rooms.host.minutes', { n: num(m) })}
                </option>
              ))}
            </select>
          </label>
        </div>
        <label className="flex flex-col gap-1.5">
          <span className="label">{t('rooms.host.mode')}</span>
          <Segmented<'l3' | 'l2'>
            value={effectiveMode}
            onChange={setMode}
            options={[
              { value: 'l3', label: t('adv.modeL3') },
              { value: 'l2', label: t('adv.modeL2') },
            ]}
          />
          {effectiveMode === 'l2' ? <span className="text-xs text-slate-500">{t('adv.modeHint')}</span> : null}
          {effectiveMode === 'l2' ? <TapDriverNotice /> : null}
        </label>
        <button type="button" className="btn btn-primary mt-1 self-start" onClick={() => void submit()} disabled={disabled || working}>
          {working ? <RefreshCw size={16} className="animate-spin" /> : <Plus size={16} />}
          {working ? t('rooms.host.creating') : t('rooms.host.create')}
        </button>
      </div>
    </Card>
  )
}

// -------------------------------------------------------------------- join --

function JoinCard({ disabled, busy }: { disabled: boolean; busy: RoomAction | null }) {
  const t = useT()
  const joinRoom = useRoomsStore((s) => s.joinRoom)
  const [code, setCode] = useState('')
  const working = busy?.kind === 'join'
  const trimmed = code.trim()

  const submit = async () => {
    if (!trimmed) return
    try {
      const room = await joinRoom(trimmed)
      setCode('')
      const reply = useRoomsStore.getState().answerCodes[room.room_id]
      if (reply) await writeClipboard(reply)
    } catch {
      // Banner.
    }
  }

  const paste = async () => {
    const text = await readClipboardText()
    if (text) setCode(text.trim())
  }

  return (
    <Card>
      <SectionTitle icon={<DoorOpen size={16} />}>{t('rooms.join.title')}</SectionTitle>
      <p className="mb-4 text-sm text-slate-400">{t('rooms.join.hint')}</p>
      <div className="flex flex-col gap-3">
        <textarea
          className="input ltr h-[88px] resize-none font-mono text-xs"
          value={code}
          placeholder={t('rooms.join.placeholder')}
          onChange={(e) => setCode(e.target.value)}
          disabled={working}
        />
        <div className="flex gap-2">
          <button type="button" className="btn btn-primary" onClick={() => void submit()} disabled={disabled || working || !trimmed}>
            {working ? <RefreshCw size={16} className="animate-spin" /> : <DoorOpen size={16} />}
            {working ? t('rooms.join.joining') : t('rooms.join.button')}
          </button>
          <button type="button" className="btn" onClick={() => void paste()} disabled={working}>
            <ClipboardPaste size={16} /> {t('common.paste')}
          </button>
        </div>
      </div>
    </Card>
  )
}

// -------------------------------------------------------------------- room --

/** Keep this network: reopen it (host) or rejoin it (guest) automatically. */
function KeepToggle({ room }: { room: RoomSummary }) {
  const t = useT()
  const kept = useKept((s) => s.kept)
  const load = useKept((s) => s.load)
  const setKeep = useKept((s) => s.set)
  useEffect(() => {
    void load()
  }, [load, room.room_id, room.peer_count])
  if (!kept) return null
  const on = kept.rooms.includes(room.room_id)
  return (
    <button
      type="button"
      className={`btn btn-sm ${on ? 'border-emerald-400/40 text-emerald-300' : ''}`}
      title={room.is_host ? t('rooms.keepHintHost') : t('rooms.keepHintGuest')}
      aria-pressed={on}
      onClick={() => void setKeep(room.room_id, !on)}
    >
      <Pin size={14} className={on ? 'fill-current' : ''} />
      {on ? t('rooms.kept') : t('rooms.keep')}
    </button>
  )
}

function RoomCard({ room, network, busy }: { room: RoomSummary; network: NetworkStatus | null; busy: RoomAction | null }) {
  const t = useT()
  const num = useNumber()
  const regenerate = useRoomsStore((s) => s.regenerate)
  const leaveRoom = useRoomsStore((s) => s.leaveRoom)
  const closeRoom = useRoomsStore((s) => s.closeRoom)
  const pairingCode = useRoomsStore((s) => s.pairingCodes[room.room_id])
  const codeExpiry = useRoomsStore((s) => s.pairingExpiry[room.room_id])
  const answerCode = useRoomsStore((s) => s.answerCodes[room.room_id])

  const peers = useMemo(() => sortPeers(room.peers ?? []), [room.peers])
  const others = peers.filter((p) => !p.is_self)
  const online = others.filter((p) => isUsable(p.state)).length
  const hostAddress = room.is_host ? room.local_address : others.find((p) => p.is_host)?.virtual_address
  const busyHere = (kind: RoomAction['kind']) => busy?.roomId === room.room_id && busy.kind === kind
  const ready = network?.state === 'ready'

  return (
    <Card className="flex flex-col gap-5" dropRoom={room.room_id}>
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div className="flex min-w-0 items-center gap-3">
          <div className="grid h-11 w-11 shrink-0 place-items-center rounded-xl bg-accent/15 text-accent">
            {room.is_host ? <Crown size={20} /> : <Users size={20} />}
          </div>
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <h2 className="truncate text-lg font-semibold text-white">{room.name}</h2>
              <span className="chip">{room.is_host ? t('common.host') : t('common.guest')}</span>
              {room.mode === 'l2' ? <span className="chip border-accent-2/40 text-accent-2">{t('rooms.modeChip')}</span> : null}
            </div>
            <div className="mt-0.5 text-xs text-slate-500">
              {t('rooms.seats', { online: num(online), used: num(others.length), max: num(room.max_peers) })}
            </div>
          </div>
        </div>
        <div className="flex items-center gap-2">
          <KeepToggle room={room} />
          {room.is_host ? (
            <>
              <button type="button" className="btn btn-sm" onClick={() => void regenerate(room.room_id)} disabled={busyHere('regenerate')}>
                {busyHere('regenerate') ? <RefreshCw size={14} className="animate-spin" /> : <Sparkles size={14} />}
                {busyHere('regenerate') ? t('rooms.issuing') : t('rooms.newInvite')}
              </button>
              <button
                type="button"
                className="btn btn-danger btn-sm"
                disabled={busyHere('close')}
                onClick={() => {
                  if (window.confirm(t('rooms.closeConfirm', { name: room.name }))) void closeRoom(room.room_id)
                }}
              >
                <Trash2 size={14} /> {t('rooms.close')}
              </button>
            </>
          ) : (
            <button type="button" className="btn btn-danger btn-sm" onClick={() => void leaveRoom(room.room_id)} disabled={busyHere('leave')}>
              <LogOut size={14} /> {busyHere('leave') ? t('rooms.leaving') : t('rooms.leave')}
            </button>
          )}
        </div>
      </header>

      <GameLimitBanner room={room} />
      <VoicePanel room={room} />
      <FilesPanel room={room} />
      {room.is_host ? <FriendInvites room={room} /> : null}
      {room.is_host && pairingCode ? (
        <details className="rounded-2xl border border-white/[0.06] bg-white/[0.02] p-3">
          <summary className="cursor-pointer text-xs text-slate-400">{t('rooms.manualInvite')}</summary>
          <div className="mt-3">
            <InviteFlow room={room} code={pairingCode} expiresAt={codeExpiry ?? room.pairing_expires_at} busy={busy} />
          </div>
        </details>
      ) : null}
      {!room.is_host && answerCode ? (
        <div className="rounded-2xl border border-accent/30 bg-accent/[0.07] p-4">
          <div className="mb-1 flex items-center gap-2 text-sm font-medium text-white">
            <Send size={15} className="text-accent" /> {t('rooms.yourReply')}
          </div>
          <p className="mb-3 text-xs text-slate-400">{t('rooms.yourReplyHint')}</p>
          <CodeBox code={answerCode} label={`reply-${room.name}`} />
          <div className="mt-3 flex items-center gap-2 text-xs text-slate-500">
            <RefreshCw size={12} className="animate-spin" /> {t('rooms.waitingHost')}
          </div>
        </div>
      ) : null}

      <LanStrip room={room} network={network} />
      {ready && hostAddress ? (
        <div className="flex items-start gap-3 rounded-xl border border-white/[0.06] bg-white/[0.02] px-4 py-3 text-sm text-slate-300">
          <Zap size={16} className="mt-0.5 shrink-0 text-accent-2" />
          <div>
            <span className="font-medium text-white">{t('rooms.howToPlay')}: </span>
            {room.is_host ? t('rooms.howHost', { addr: hostAddress }) : t('rooms.howGuest', { addr: hostAddress })}
            <CopyButton value={hostAddress} iconOnly className="ms-1 inline-flex rounded-md p-1 align-middle text-slate-400 hover:bg-white/10" />
          </div>
        </div>
      ) : null}

      <div>
        <SectionTitle icon={<Users size={16} />}>{t('rooms.players')}</SectionTitle>
        <ul className="flex flex-col gap-1.5">
          {peers.map((peer) => (
            <PlayerRow key={peerKey(peer)} room={room} peer={peer} busy={busy} />
          ))}
        </ul>
        {others.length === 0 ? <p className="mt-2 text-sm text-slate-500">{t('rooms.noPlayers')}</p> : null}
      </div>
    </Card>
  )
}

/** The two host steps: send the invite, paste the reply. */
function InviteFlow({ room, code, expiresAt, busy }: { room: RoomSummary; code: string; expiresAt?: string; busy: RoomAction | null }) {
  const t = useT()
  const acceptAnswer = useRoomsStore((s) => s.acceptAnswer)
  const [reply, setReply] = useState('')
  const working = busy?.kind === 'accept'
  const now = useNow()
  const expiry = validTime(expiresAt)
  const left = expiry ? Math.max(0, Math.round((expiry.getTime() - now) / 1000)) : null

  const accept = async () => {
    const r = reply.trim()
    if (!r) return
    try {
      await acceptAnswer(r)
      setReply('')
    } catch {
      // Banner.
    }
  }

  const paste = async () => {
    const text = await readClipboardText()
    if (text) setReply(text.trim())
  }

  return (
    <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
      <Step n={1} title={t('rooms.step1')} hint={t('rooms.step1Hint')}>
        {left === 0 ? <Banner tone="warn">{t('rooms.expired')}</Banner> : <CodeBox code={code} label={room.name} />}
        {left !== null && left > 0 ? <div className="mt-2 text-xs text-slate-500">{t('rooms.expires', { t: formatLeft(left) })}</div> : null}
      </Step>
      <Step n={2} title={t('rooms.step2')} hint={t('rooms.step2Hint')}>
        <textarea
          className="input ltr h-[76px] resize-none font-mono text-xs"
          placeholder={t('rooms.replyPlaceholder')}
          value={reply}
          onChange={(e) => setReply(e.target.value)}
          disabled={working}
        />
        <div className="mt-2 flex gap-2">
          <button type="button" className="btn btn-primary btn-sm" onClick={() => void accept()} disabled={working || !reply.trim()}>
            {working ? <RefreshCw size={14} className="animate-spin" /> : <UserPlus size={14} />}
            {working ? t('rooms.accepting') : t('rooms.accept')}
          </button>
          <button type="button" className="btn btn-sm" onClick={() => void paste()} disabled={working}>
            <ClipboardPaste size={14} /> {t('common.paste')}
          </button>
        </div>
      </Step>
    </div>
  )
}

/** One-click invites for friends who are online and not yet in the room. */
function FriendInvites({ room }: { room: RoomSummary }) {
  const t = useT()
  const friends = useFriendsStore((s) => s.friends)
  const invite = useFriendsStore((s) => s.invite)
  const setPage = useDaemonStore((s) => s.setPage)
  const [sent, setSent] = useState<Record<string, boolean>>({})
  const inRoom = new Set(room.peers.map((p) => p.peer_id))
  const candidates = friends.filter((f) => f.state === 'friend' && f.online && !(f.peer_id && inRoom.has(f.peer_id)))
  return (
    <div className="rounded-2xl border border-accent/30 bg-accent/[0.07] p-4">
      <div className="mb-3 flex items-center gap-2 text-sm font-medium text-white">
        <Send size={15} className="text-accent" /> {t('rooms.inviteFriends')}
      </div>
      {candidates.length === 0 ? (
        <p className="text-xs text-slate-400">
          {t('rooms.noFriendsOnline')}{' '}
          <button type="button" className="text-accent underline" onClick={() => setPage('friends')}>
            {t('nav.friends')}
          </button>
        </p>
      ) : (
        <div className="flex flex-wrap gap-2">
          {candidates.map((f) => (
            <button
              key={f.pub}
              type="button"
              className="btn btn-sm"
              disabled={sent[f.pub]}
              onClick={() => {
                setSent((s) => ({ ...s, [f.pub]: true }))
                void invite(f.pub, room.room_id).catch(() => setSent((s) => ({ ...s, [f.pub]: false })))
              }}
            >
              <Avatar name={f.name} size={20} /> {f.name} {sent[f.pub] ? '✓' : ''}
            </button>
          ))}
        </div>
      )}
    </div>
  )
}

function Step({ n, title, hint, children }: { n: number; title: string; hint: string; children: ReactNode }) {
  return (
    <div className="rounded-2xl border border-white/[0.07] bg-white/[0.02] p-4">
      <div className="mb-1 flex items-center gap-2">
        <span className="grid h-6 w-6 place-items-center rounded-full bg-accent-gradient text-xs font-bold text-white">{n}</span>
        <span className="text-sm font-medium text-white">{title}</span>
      </div>
      <p className="mb-3 text-xs text-slate-500">{hint}</p>
      {children}
    </div>
  )
}

/** A code with copy / copy-as-link / save-as-file. */
function CodeBox({ code, label }: { code: string; label: string }) {
  const t = useT()
  const save = async () => {
    try {
      const path = await saveInviteFile(code, label)
      toast({ tone: 'ok', title: t('common.saved'), body: t('rooms.savedTo', { path }) })
    } catch (err) {
      toast({ tone: 'error', title: String(err) })
    }
  }
  return (
    <div>
      <div className="ltr max-h-20 overflow-hidden break-all rounded-xl border border-white/[0.06] bg-surface-sunken/80 px-3 py-2 font-mono text-[11px] leading-relaxed text-slate-400 [mask-image:linear-gradient(to_bottom,black_60%,transparent)]">
        {code}
      </div>
      <div className="mt-2 flex flex-wrap gap-2">
        <CopyButton value={code} label={t('rooms.copyInvite')} className="btn btn-primary btn-sm" />
        <CopyButton value={`lanbaz://join/${code}`} label={t('rooms.copyLink')} className="btn btn-sm" />
        <button type="button" className="btn btn-sm" onClick={() => void save()}>
          <FileDown size={14} /> {t('rooms.saveFile')}
        </button>
      </div>
    </div>
  )
}

function LanStrip({ room, network }: { room: RoomSummary; network: NetworkStatus | null }) {
  const t = useT()
  const ready = network?.state === 'ready'
  return (
    <div className={`rounded-xl border px-4 py-3 ${ready ? 'border-emerald-400/20 bg-emerald-400/[0.05]' : 'border-amber-400/20 bg-amber-400/[0.05]'}`}>
      <div className="flex flex-wrap items-center gap-x-5 gap-y-1 text-xs">
        <span className="flex items-center gap-2 text-slate-200">
          <span className={`h-2 w-2 rounded-full ${ready ? 'bg-status-running' : 'animate-pulse-dot bg-status-stopping'}`} />
          {t('rooms.lanState', { state: network?.state ?? '…' })}
        </span>
        <span className="ltr font-mono text-slate-400">{network?.subnet ?? room.subnet}</span>
        <span className="ltr inline-flex items-center gap-1 font-mono text-slate-100">
          {network?.local_address ?? room.local_address}
          {room.local_address ? <CopyButton value={room.local_address} iconOnly className="rounded p-0.5 text-slate-500 hover:text-slate-200" /> : null}
        </span>
        {network?.adapter ? <span className="text-slate-500">{network.adapter}</span> : null}
      </div>
      {network?.note ? <p className="mt-1.5 text-xs text-amber-200/90">{network.note}</p> : null}
    </div>
  )
}

function PlayerRow({ room, peer, busy }: { room: RoomSummary; peer: PeerSummary; busy: RoomAction | null }) {
  const t = useT()
  const pingPeer = useRoomsStore((s) => s.pingPeer)
  const kickPeer = useRoomsStore((s) => s.kickPeer)
  const presence = useGamesStore((s) => (peer.is_self ? s.presence[''] : s.presence[peer.peer_id]))
  const id = peer.peer_id || peer.link_id || ''
  const name = peer.display_name || (peer.peer_id ? peer.peer_id.slice(0, 10) : t('rooms.connecting'))
  const measuring = busy?.kind === 'ping' && busy.peerId === id
  const kicking = busy?.kind === 'kick' && busy.peerId === id
  const history = useQuality((s) => s.rtt[qualityKey(room.room_id, peer.peer_id)]) ?? EMPTY
  const limit = useGameLimit()

  return (
    <li
      className={`flex items-center gap-3 rounded-xl px-3 py-2.5 ${peer.is_self ? 'bg-accent/[0.06]' : 'hover:bg-white/[0.03]'}`}
      data-drop-peer={peer.is_self ? undefined : peer.peer_id || undefined}
      data-drop-room={peer.is_self ? undefined : room.room_id}
    >
      <Avatar name={peer.display_name || id || '?'} size={34} />
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="truncate text-sm font-medium text-slate-100">
            {name}
            {peer.is_self ? <span className="text-slate-500"> ({t('common.you')})</span> : null}
          </span>
          {peer.is_host ? <Crown size={13} className="shrink-0 text-amber-300" /> : null}
        </div>
        <div className="mt-0.5 flex items-center gap-3">
          {peer.is_self ? null : <StateDot state={peer.state} />}
          {presence?.game_name ? <span className="truncate text-xs text-accent-2">{presence.game_name}</span> : null}
        </div>
      </div>
      <span className="ltr hidden w-32 font-mono text-xs text-slate-400 md:inline">
        {peer.is_self ? room.local_address : peer.virtual_address ?? '—'}
      </span>
      {peer.is_self ? (
        <span className="w-20" />
      ) : (
        <>
          <span className="hidden w-20 text-xs text-slate-500 sm:inline">{t(`link.${peer.link_kind}` as never)}</span>
          <span className="hidden sm:inline" title={t('rooms.quality', { jitter: Math.round(peer.jitter_ms), loss: Math.round(peer.packet_loss * 100) })}>
            <Sparkline values={history} limit={limit} />
          </span>
          <span className="w-20">
            <PingBadge ms={peer.rtt_ms} measured={peer.round_trips > 0 && isUsable(peer.state)} />
            {peer.packet_loss >= 0.02 && isUsable(peer.state) ? <span className="ltr block text-[10px] text-amber-300">{t('rooms.loss', { n: Math.round(peer.packet_loss * 100) })}</span> : null}
          </span>
          <button
            type="button"
            className="btn btn-ghost btn-sm"
            onClick={() => void pingPeer(room.room_id, id)}
            disabled={measuring || !isUsable(peer.state)}
            title={t('rooms.ping')}
          >
            <Link2 size={14} className={measuring ? 'animate-pulse' : ''} />
          </button>
          {room.is_host ? (
            <button
              type="button"
              className="btn btn-ghost btn-sm text-red-300"
              disabled={kicking}
              title={t('rooms.kick')}
              onClick={() => {
                if (window.confirm(t('rooms.kickConfirm', { name }))) void kickPeer(room.room_id, id)
              }}
            >
              <X size={14} />
            </button>
          ) : null}
        </>
      )}
    </li>
  )
}

function formatLeft(seconds: number): string {
  const m = Math.floor(seconds / 60)
  const s = seconds % 60
  return `${m}:${String(s).padStart(2, '0')}`
}
