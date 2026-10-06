import { Headphones, Mic, MicOff, PhoneOff, Volume2, VolumeX } from 'lucide-react'

import { useT } from '../i18n'
import { usePrefs } from '../stores/prefs'
import { useVoice } from '../stores/voice'
import type { RoomSummary } from '../types/protocol'
import { Avatar } from './ui'

/** Voice chat for one room: join, mute, who is talking, per-player volume. */
export function VoicePanel({ room }: { room: RoomSummary }) {
  const t = useT()
  const v = useVoice()
  const ptt = usePrefs((s) => s.voicePtt)
  const here = v.roomId === room.room_id
  const names = Object.fromEntries((room.peers ?? []).map((p) => [p.peer_id, p.display_name || p.peer_id.slice(0, 8)]))

  if (!here) {
    return (
      <div className="flex items-center justify-between gap-3 rounded-2xl border border-white/[0.06] bg-white/[0.02] px-3 py-2">
        <span className="flex items-center gap-2 text-xs text-slate-400">
          <Headphones size={15} /> {t('voice.title')}
        </span>
        <button type="button" className="btn btn-sm" disabled={v.joining} onClick={() => void v.join(room.room_id)}>
          <Mic size={14} /> {v.joining ? t('voice.joining') : t('voice.join')}
        </button>
      </div>
    )
  }
  const live = ptt ? v.pttHeld : !v.muted
  return (
    <div className="rounded-2xl border border-emerald-400/25 bg-emerald-400/[0.04] p-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="flex items-center gap-2 text-xs text-emerald-200">
          <span className={`h-2 w-2 rounded-full ${live && v.speaking ? 'bg-emerald-400' : live ? 'bg-emerald-400/40' : 'bg-slate-600'}`} />
          {ptt ? (v.pttHeld ? t('voice.talking') : t('voice.pttHint')) : v.muted ? t('voice.muted') : t('voice.open')}
        </span>
        <div className="flex items-center gap-2">
          {ptt ? null : (
            <button type="button" className="btn btn-sm" aria-pressed={v.muted} onClick={v.toggleMute}>
              {v.muted ? <MicOff size={14} /> : <Mic size={14} />} {v.muted ? t('voice.unmute') : t('voice.mute')}
            </button>
          )}
          <button type="button" className="btn btn-danger btn-sm" onClick={v.leave}>
            <PhoneOff size={14} /> {t('voice.leave')}
          </button>
        </div>
      </div>
      <ul className="mt-2 space-y-1.5">
        {Object.entries(v.peers).map(([id, p]) => (
          <li key={id} className="flex items-center gap-2 text-xs">
            <span className={`rounded-full ${p.speaking && !p.muted ? 'ring-2 ring-emerald-400' : ''}`}>
              <Avatar name={names[id] ?? id} size={24} />
            </span>
            <span className="min-w-0 flex-1 truncate text-slate-200">{names[id] ?? id.slice(0, 8)}</span>
            <input
              type="range"
              min={0}
              max={1}
              step={0.05}
              value={p.volume}
              className="ltr w-24 accent-emerald-400"
              aria-label={t('voice.volume')}
              onChange={(e) => v.setPeerVolume(id, Number(e.target.value))}
            />
            <button type="button" className="btn btn-ghost btn-sm" title={p.muted ? t('voice.unmute') : t('voice.mute')} onClick={() => v.togglePeerMute(id)}>
              {p.muted ? <VolumeX size={14} /> : <Volume2 size={14} />}
            </button>
          </li>
        ))}
        {Object.keys(v.peers).length === 0 ? <li className="text-xs text-slate-500">{t('voice.alone')}</li> : null}
      </ul>
    </div>
  )
}
