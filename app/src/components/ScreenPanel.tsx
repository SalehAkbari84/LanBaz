import { invoke } from '@tauri-apps/api/core'
import { Eye, MonitorUp, Square } from 'lucide-react'

import { useT } from '../i18n'
import { HEIGHTS, FPS } from '../services/screenQuality'
import { isTauri } from '../services/tauri'
import { useScreen } from '../stores/screen'
import type { RoomSummary } from '../types/protocol'

const rate = (kbps: number) => (kbps >= 1000 ? `${(kbps / 1000).toFixed(1)} Mbps` : `${kbps} kbps`)

/** Screen share for one room: share with caps, or watch somebody else's. */
export function ScreenPanel({ room }: { room: RoomSummary }) {
  const t = useT()
  const { sharing, settings, viewers, sentBytes, shares, detected, broadcast, start, stop, setSettings } = useScreen()
  const mine = sharing?.roomId === room.room_id
  const other = shares[room.room_id]
  const names = Object.fromEntries((room.peers ?? []).map((p) => [p.peer_id, p.display_name || p.peer_id.slice(0, 8)]))

  const watch = (peerId: string, name: string) => {
    if (isTauri()) void invoke('open_screen_viewer', { room: room.room_id, peer: peerId, name })
  }

  const quality = (
    <div className="flex flex-wrap items-center gap-2 text-xs text-slate-400">
      <label className="flex items-center gap-1">
        {t('screen.quality')}
        <select className="input w-24 py-1 text-xs" value={settings.rung} onChange={(e) => setSettings({ rung: Number(e.target.value) })}>
          <option value={-1}>{t('screen.auto')}</option>
          {HEIGHTS.map((h, i) => (
            <option key={h} value={i}>
              {h}p
            </option>
          ))}
        </select>
      </label>
      <label className="flex items-center gap-1">
        {t('screen.fps')}
        <select className="input w-20 py-1 text-xs" value={settings.fps} onChange={(e) => setSettings({ fps: Number(e.target.value) })}>
          <option value={0}>{t('screen.auto')}</option>
          {FPS.map((f) => (
            <option key={f} value={f}>
              {f}
            </option>
          ))}
        </select>
      </label>
      <label className="flex items-center gap-1">
        {t('screen.content')}
        <select className="input w-28 py-1 text-xs" value={settings.content} onChange={(e) => setSettings({ content: e.target.value as typeof settings.content })}>
          <option value="auto">
            {t('screen.auto')}
            {mine ? ` (${t(detected === 'motion' ? 'screen.motion' : 'screen.detail')})` : ''}
          </option>
          <option value="motion">{t('screen.motion')}</option>
          <option value="detail">{t('screen.detail')}</option>
        </select>
      </label>
      <label className="flex items-center gap-1" title={t('screen.saverHint')}>
        <input type="checkbox" checked={settings.dataSaver} onChange={(e) => setSettings({ dataSaver: e.target.checked })} /> {t('screen.saver')}
      </label>
      {!mine ? (
        <label className="flex items-center gap-1" title={t('screen.soundHint')}>
          <input type="checkbox" checked={settings.sound} onChange={(e) => setSettings({ sound: e.target.checked })} /> {t('screen.sound')}
        </label>
      ) : null}
    </div>
  )

  if (mine) {
    const list = Object.entries(viewers)
    return (
      <div className="rounded-2xl border border-red-400/30 bg-red-400/[0.05] p-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <span className="flex items-center gap-2 text-xs text-red-200">
            <span className="h-2 w-2 animate-pulse rounded-full bg-red-500" />
            {t('screen.live')} · {t('screen.viewers', { n: list.length })} · <span className="ltr">{(sentBytes / 1048576).toFixed(1)} MB</span>
          </span>
          <button type="button" className="btn btn-danger btn-sm" onClick={stop}>
            <Square size={13} /> {t('screen.stop')}
          </button>
        </div>
        <div className="mt-2">{quality}</div>
        {broadcast ? (
          <p className="mt-2 text-[11px] text-emerald-300/90" title={t('screen.broadcastHint')}>
            {t('screen.broadcast', { n: list.length, codec: broadcast })}
          </p>
        ) : null}
        {list.length ? (
          <ul className="mt-2 space-y-1">
            {list.map(([id, v]) => (
              <li key={id} className="flex items-center justify-between text-[11px] text-slate-300">
                <span>{names[id] ?? v.name}</span>
                {v.paused ? (
                  <span className="text-slate-500">{t('screen.paused')}</span>
                ) : (
                  <span className="ltr font-mono text-slate-400">
                    {v.height ? `${v.height}p` : '…'} · {v.fps} fps · {rate(v.kbps)}
                    {v.codec ? ` · ${v.codec}` : ''}
                    {v.broadcast && v.layers ? ` · ${v.layers}/3` : ''}
                    {v.rttMs != null ? ` · ${Math.round(v.rttMs)} ms` : ''}
                  </span>
                )}
              </li>
            ))}
          </ul>
        ) : (
          <p className="mt-2 text-[11px] text-slate-500">{t('screen.noViewers')}</p>
        )}
      </div>
    )
  }

  return (
    <div className="rounded-2xl border border-white/[0.06] bg-white/[0.02] px-3 py-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="flex items-center gap-2 text-xs text-slate-400">
          <MonitorUp size={15} />
          {other ? t('screen.isSharing', { name: names[other.peerId] ?? other.name }) : t('screen.title')}
        </span>
        <div className="flex gap-2">
          {other ? (
            <button type="button" className="btn btn-primary btn-sm" onClick={() => watch(other.peerId, names[other.peerId] ?? other.name)}>
              <Eye size={14} /> {t('screen.watch')}
            </button>
          ) : null}
          <button type="button" className="btn btn-sm" disabled={Boolean(sharing)} onClick={() => void start(room.room_id)}>
            <MonitorUp size={14} /> {t('screen.share')}
          </button>
        </div>
      </div>
      <details className="mt-1">
        <summary className="cursor-pointer text-[11px] text-slate-500">{t('screen.settings')}</summary>
        <div className="mt-2">{quality}</div>
      </details>
    </div>
  )
}
