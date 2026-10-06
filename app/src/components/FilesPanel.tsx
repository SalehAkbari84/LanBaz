import { getCurrentWebview } from '@tauri-apps/api/webview'
import { ArrowDownToLine, ArrowUpFromLine, Check, FolderOpen, X } from 'lucide-react'
import { useEffect, useMemo } from 'react'

import { useT } from '../i18n'
import { isTauri } from '../services/tauri'
import { useFiles, type FileTransfer } from '../stores/files'
import { useRoomsStore } from '../stores/rooms'
import { toast } from '../stores/toasts'
import type { RoomSummary } from '../types/protocol'

const size = (n: number) => (n >= 1 << 30 ? `${(n / (1 << 30)).toFixed(2)} GB` : n >= 1 << 20 ? `${(n / (1 << 20)).toFixed(1)} MB` : `${Math.max(1, Math.round(n / 1024))} KB`)

/**
 * Dropping a file anywhere on a player row (data-drop-peer) sends it to that
 * player; on a room card with exactly one other player, to them. Mounted once.
 */
export function useFileDrop(message: string) {
  const offer = useFiles((s) => s.offer)
  useEffect(() => {
    if (!isTauri()) return
    let off: (() => void) | undefined
    void getCurrentWebview()
      .onDragDropEvent((e) => {
        if (e.payload.type !== 'drop' || !e.payload.paths.length) return
        const dpr = window.devicePixelRatio || 1
        const el = document.elementFromPoint(e.payload.position.x / dpr, e.payload.position.y / dpr)
        let peer = el?.closest<HTMLElement>('[data-drop-peer]')?.dataset
        if (!peer) {
          const roomId = el?.closest<HTMLElement>('[data-drop-room]')?.dataset.dropRoom
          const room = roomId ? useRoomsStore.getState().rooms[roomId] : undefined
          const others = (room?.peers ?? []).filter((p) => !p.is_self && p.peer_id)
          if (room && others.length === 1) peer = { dropPeer: others[0]!.peer_id, dropRoom: room.room_id }
        }
        if (!peer?.dropPeer || !peer.dropRoom) {
          toast({ tone: 'warn', title: message })
          return
        }
        for (const path of e.payload.paths) void offer(peer.dropRoom, peer.dropPeer, path)
      })
      .then((u) => (off = u))
    return () => off?.()
  }, [offer, message])
}

/** The transfers of one room. */
export function FilesPanel({ room }: { room: RoomSummary }) {
  const t = useT()
  const all = useFiles((s) => s.transfers)
  const transfers = useMemo(() => Object.values(all).filter((x) => x.room_id === room.room_id), [all, room.room_id])
  if (!transfers.length) return null
  return (
    <ul className="space-y-2">
      {transfers.map((x) => (
        <Transfer key={x.id} x={x} />
      ))}
      <li className="text-[11px] text-slate-500">{t('files.hint')}</li>
    </ul>
  )
}

function Transfer({ x }: { x: FileTransfer }) {
  const t = useT()
  const { respond, cancel, reveal, dismiss } = useFiles()
  const pct = x.size ? Math.min(100, Math.round((x.done / x.size) * 100)) : 0
  const active = x.state === 'transferring' || x.state === 'offered' || x.state === 'incoming'
  const eta = x.state === 'transferring' && x.rate ? Math.ceil((x.size - x.done) / x.rate) : null
  return (
    <li className="rounded-xl border border-white/[0.06] bg-white/[0.02] p-3 text-xs">
      <div className="flex items-center gap-2">
        {x.direction === 'in' ? <ArrowDownToLine size={14} className="text-accent" /> : <ArrowUpFromLine size={14} className="text-accent-2" />}
        <span className="min-w-0 flex-1 truncate text-slate-200" title={x.name}>
          {x.name}
        </span>
        <span className="ltr text-slate-500">{size(x.size)}</span>
      </div>
      <div className="mt-1 text-slate-400">
        {t(`files.state.${x.state}` as never, { name: x.peer_name ?? '?' })}
        {x.state === 'transferring' ? (
          <span className="ltr ms-2 text-slate-500">
            {pct}% {x.rate ? `· ${size(x.rate)}/s` : ''} {eta !== null ? `· ${eta < 90 ? `${eta}s` : `${Math.ceil(eta / 60)}m`}` : ''}
          </span>
        ) : null}
        {x.error && x.state !== 'done' ? <span className="ms-2 text-red-300">{x.error}</span> : null}
      </div>
      {x.state === 'transferring' ? (
        <div className="mt-2 h-1.5 overflow-hidden rounded-full bg-white/10">
          <div className="h-full rounded-full bg-accent transition-[width]" style={{ width: `${pct}%` }} />
        </div>
      ) : null}
      <div className="mt-2 flex flex-wrap gap-2">
        {x.state === 'incoming' ? (
          <>
            <button type="button" className="btn btn-primary btn-sm" onClick={() => void respond(x.id, true)}>
              <Check size={14} /> {t('files.accept')}
            </button>
            <button type="button" className="btn btn-sm" onClick={() => void respond(x.id, false)}>
              <X size={14} /> {t('files.decline')}
            </button>
          </>
        ) : active ? (
          <button type="button" className="btn btn-sm" onClick={() => void cancel(x.id)}>
            <X size={14} /> {t('files.cancel')}
          </button>
        ) : (
          <>
            {x.state === 'done' && x.direction === 'in' && x.path ? (
              <button type="button" className="btn btn-sm" onClick={() => void reveal(x.path!)}>
                <FolderOpen size={14} /> {t('files.show')}
              </button>
            ) : null}
            <button type="button" className="btn btn-ghost btn-sm" onClick={() => dismiss(x.id)}>
              {t('files.dismiss')}
            </button>
          </>
        )}
      </div>
    </li>
  )
}
