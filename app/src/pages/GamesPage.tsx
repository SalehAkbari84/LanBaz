import { Gamepad2, LogIn, Play, Radio, Search, Server, Users } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'

import { Avatar, Card, CopyButton, EmptyState, PageHeader, SectionTitle, Segmented } from '../components/ui'
import { GameApplyButton } from '../components/GameApplyButton'
import { useT } from '../i18n'
import { openGameUri } from '../services/tauri'
import { useDaemonStore } from '../stores/daemon'
import { useFriendsStore } from '../stores/friends'
import { useRoomsStore } from '../stores/rooms'
import { useGamesStore } from '../stores/games'
import type { Presence } from '../types/protocol'

export function GamesPage() {
  const t = useT()
  const presence = useGamesStore((s) => s.presence)
  const library = useGamesStore((s) => s.library)
  const loadLibrary = useGamesStore((s) => s.loadLibrary)
  const connected = useDaemonStore((s) => s.connection === 'connected')
  const [query, setQuery] = useState('')
  const [kind, setKind] = useState<'all' | 'nosteam' | 'free'>('all')
  const shown = useMemo(() => {
    const q = query.trim().toLowerCase()
    return library.filter((g) => {
      const a = g.availability ?? []
      if (kind === 'free' && !a.includes('free')) return false
      if (kind === 'nosteam' && a.length === 0) return false
      return !q || g.name.toLowerCase().includes(q) || g.id.includes(q)
    })
  }, [library, query, kind])

  useEffect(() => {
    if (connected && library.length === 0) void loadLibrary()
  }, [connected, library.length, loadLibrary])

  const playing = useMemo(
    () => Object.values(presence).sort((a, b) => Number(b.hosting) - Number(a.hosting) || (a.name ?? '').localeCompare(b.name ?? '')),
    [presence],
  )

  return (
    <>
      <PageHeader title={t('games.title')} subtitle={t('games.subtitle')} />

      <FriendsPlaying />

      <Card>
        <SectionTitle icon={<Radio size={16} />}>{t('games.nowPlaying')}</SectionTitle>
        {playing.length === 0 ? (
          <EmptyState icon={<Gamepad2 size={26} />} title={t('games.nobodyPlaying')} />
        ) : (
          <ul className="grid grid-cols-1 gap-3 md:grid-cols-2">
            {playing.map((p) => (
              <PresenceCard key={`${p.self ? 'self' : p.peer_id}`} p={p} />
            ))}
          </ul>
        )}
      </Card>

      <Card>
        <SectionTitle icon={<Gamepad2 size={16} />} extra={<span className="chip">{library.length}</span>}>
          {t('games.library')}
        </SectionTitle>
        <label className="relative mb-4 block">
          <Search size={16} className="pointer-events-none absolute start-3 top-1/2 -translate-y-1/2 text-slate-500" />
          <input className="input ps-9" value={query} placeholder={t('games.search', { n: library.length })} onChange={(e) => setQuery(e.target.value)} />
        </label>
        <div className="mb-4">
          <Segmented
            value={kind}
            onChange={setKind}
            options={[
              { value: 'all', label: t('games.filterAll') },
              { value: 'nosteam', label: t('games.filterNoSteam') },
              { value: 'free', label: t('games.filterFree') },
            ]}
          />
          {kind !== 'all' ? <p className="mt-2 text-xs text-slate-500">{t('games.noSteamHint')}</p> : null}
        </div>
        {shown.length === 0 ? <p className="text-sm text-slate-500">{t('games.noMatch')}</p> : null}
        <ul className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3">
          {shown.map((g) => (
            <li key={g.id} className="rounded-xl border border-white/[0.06] bg-white/[0.02] p-4">
              <div className="flex items-center gap-3">
                <div className="grid h-10 w-10 place-items-center rounded-lg bg-accent/15 text-accent">
                  <Gamepad2 size={18} />
                </div>
                <div className="min-w-0">
                  <div className="truncate text-sm font-medium text-slate-100">{g.name}</div>
                  <div className="ltr text-xs text-slate-500">
                    {g.protocol.toUpperCase()} · {g.ports.slice(0, 3).join(', ') || '—'}
                  </div>
                </div>
              </div>
              {g.join_hint ? <p className="mt-2 text-xs text-slate-400">{g.join_hint}</p> : null}
              {g.requires ? <p className="mt-1 text-xs text-amber-200/80">{t('games.requires', { what: g.requires })}</p> : null}
              <div className="mt-2 flex flex-wrap gap-1.5">
                {g.availability?.includes('free') ? <span className="chip border-emerald-400/40 text-emerald-300">{t('games.badgeFree')}</span> : null}
                {g.availability?.includes('drm_free') ? <span className="chip border-sky-400/40 text-sky-300">{t('games.badgeDrmFree')}</span> : null}
                {g.needs_l2 ? <span className="chip border-accent-2/40 text-accent-2">{t('games.needsL2')}</span> : null}
              </div>
              <GameApplyButton gameId={g.id} />
            </li>
          ))}
        </ul>
      </Card>
    </>
  )
}

function PresenceCard({ p }: { p: Presence }) {
  const t = useT()
  const name = p.self ? `${p.name ?? ''} (${t('common.you')})` : p.name ?? p.peer_id.slice(0, 10)
  const endpoint = p.endpoints?.[0]
  return (
    <li className="flex items-center gap-3 rounded-xl border border-white/[0.06] bg-white/[0.02] p-4">
      <Avatar name={p.name || p.peer_id || '?'} size={40} />
      <div className="min-w-0 flex-1">
        <div className="truncate text-sm font-medium text-slate-100">{name}</div>
        <div className="flex items-center gap-2 text-xs">
          <span className="truncate text-accent-2">{p.game_name || p.exe || t('games.unknown')}</span>
          {p.hosting ? (
            <span className="chip border-emerald-400/30 px-2 py-0 text-[10px] text-emerald-300">
              <Server size={10} /> {t('games.hosting')}
            </span>
          ) : null}
        </div>
        {endpoint ? <div className="ltr mt-0.5 font-mono text-[11px] text-slate-500">{endpoint}</div> : null}
      </div>
      {!p.self && endpoint ? <CopyButton value={endpoint} label={t('games.copyAddress')} className="btn btn-sm" /> : null}
      {!p.self && p.join_uri ? (
        <button type="button" className="btn btn-primary btn-sm" onClick={() => void openGameUri(p.join_uri!)}>
          <Play size={14} /> {t('games.join')}
        </button>
      ) : null}
    </li>
  )
}

/** Friends who are online and playing or hosting: one click to get into their room. */
function FriendsPlaying() {
  const t = useT()
  const friends = useFriendsStore((s) => s.friends)
  const join = useFriendsStore((s) => s.join)
  const rooms = useRoomsStore((s) => s.rooms)
  const active = friends.filter((f) => f.state === 'friend' && f.online && (f.game || f.hosting))
  if (active.length === 0) return null
  return (
    <Card>
      <SectionTitle icon={<Users size={16} />}>{t('games.friendsPlaying')}</SectionTitle>
      <ul className="grid grid-cols-1 gap-3 md:grid-cols-2">
        {active.map((f) => {
          const inside = f.room_id ? Boolean(rooms[f.room_id]) : false
          return (
            <li key={f.pub} className="flex items-center gap-3 rounded-xl border border-white/[0.06] bg-white/[0.02] p-4">
              <Avatar name={f.name} size={40} />
              <div className="min-w-0 flex-1">
                <div className="truncate text-sm font-medium text-slate-100">{f.name}</div>
                <div className="truncate text-xs text-accent-2">{f.game || t('friends.hosting', { room: f.room_name || '' })}</div>
              </div>
              {f.hosting && !inside ? (
                <button type="button" className="btn btn-primary btn-sm" onClick={() => void join(f.pub, f.room_id).catch(() => undefined)}>
                  <LogIn size={14} /> {t('games.joinFriend')}
                </button>
              ) : inside ? (
                <span className="chip">{t('games.together')}</span>
              ) : null}
            </li>
          )
        })}
      </ul>
    </Card>
  )
}
