/**
 * One place where a pairing code arrives, however it arrived: a lanbaz:// link,
 * a .lanbaz file, or text the user just copied. The daemon says what it is
 * (invite or reply, for which room, from whom) and the user confirms with one
 * click - no hunting for the right paste box.
 */

import { ClipboardCheck, DoorOpen, UserPlus } from 'lucide-react'
import { useCallback, useEffect, useRef, useState } from 'react'

import { useT } from '../i18n'
import { onInvite, readClipboardText, takePendingInvite } from '../services/tauri'
import { daemonClient, useDaemonStore } from '../stores/daemon'
import { useRoomsStore } from '../stores/rooms'
import { toast } from '../stores/toasts'
import type { PairingInfo } from '../types/protocol'
import { Banner, Modal, writeClipboard } from './ui'

type Source = 'link' | 'clipboard'

function looksLikeCode(text: string | null): string | null {
  if (!text) return null
  let s = text.trim()
  if (s.toLowerCase().startsWith('lanbaz://')) s = s.replace(/^lanbaz:\/\/(join|reply)\//i, '')
  if (!/^lbz-[0-9a-z-\s]+$/i.test(s) || s.length < 40) return null
  return s
}

export function IncomingCode() {
  const t = useT()
  const connected = useDaemonStore((s) => s.connection === 'connected')
  const setPage = useDaemonStore((s) => s.setPage)
  const joinRoom = useRoomsStore((s) => s.joinRoom)
  const acceptAnswer = useRoomsStore((s) => s.acceptAnswer)

  const [queue, setQueue] = useState<Array<{ code: string; source: Source }>>([])
  const [current, setCurrent] = useState<{ code: string; source: Source; info: PairingInfo | null; error?: string } | null>(null)
  const [busy, setBusy] = useState(false)
  const handled = useRef(new Set<string>())

  const offer = useCallback((code: string, source: Source) => {
    if (handled.current.has(code)) return
    // Never offer back a code this app produced itself.
    const { pairingCodes, answerCodes } = useRoomsStore.getState()
    if (Object.values(pairingCodes).includes(code) || Object.values(answerCodes).includes(code)) return
    handled.current.add(code)
    setQueue((q) => [...q, { code, source }])
  }, [])

  // Links and files.
  useEffect(() => {
    let off: (() => void) | undefined
    void takePendingInvite().then((c) => c && offer(c, 'link'))
    void onInvite((c) => offer(c, 'link')).then((u) => (off = u))
    return () => off?.()
  }, [offer])

  // Clipboard, checked when the window gains focus.
  useEffect(() => {
    const check = () => {
      void readClipboardText().then((text) => {
        const code = looksLikeCode(text)
        if (code) offer(code, 'clipboard')
      })
    }
    window.addEventListener('focus', check)
    check()
    return () => window.removeEventListener('focus', check)
  }, [offer])

  // Inspect the next code once connected.
  useEffect(() => {
    if (current || queue.length === 0 || !connected) return
    const [next, ...rest] = queue
    if (!next) return
    setQueue(rest)
    const client = daemonClient()
    if (!client) return
    void client
      .inspectPairing(next.code)
      .then((info) => {
        // A clipboard code that is not actionable is not worth a dialog.
        if (next.source === 'clipboard' && (!info.valid || info.joined_here || (info.kind === 'reply' && !info.hosted_here))) return
        setCurrent({ ...next, info })
      })
      .catch((err: unknown) => {
        if (next.source === 'link') setCurrent({ ...next, info: null, error: err instanceof Error ? err.message : String(err) })
      })
  }, [queue, current, connected])

  const close = () => setCurrent(null)

  const act = async () => {
    if (!current?.info) return
    setBusy(true)
    try {
      if (current.info.kind === 'invite') {
        const room = await joinRoom(current.code)
        const reply = useRoomsStore.getState().answerCodes[room.room_id]
        if (reply) await writeClipboard(reply)
        setPage('rooms')
      } else {
        await acceptAnswer(current.code)
        toast({ tone: 'ok', title: t('rooms.accepting') })
        setPage('rooms')
      }
      close()
    } catch (err) {
      setCurrent((c) => (c ? { ...c, error: err instanceof Error ? err.message : String(err) } : c))
    } finally {
      setBusy(false)
    }
  }

  const info = current?.info
  const invite = info?.kind === 'invite'
  const room = info?.room_name || info?.room_id || ''
  const blocked = info && (!info.valid || (invite && info.joined_here) || (!invite && !info.hosted_here))

  return (
    <Modal open={current !== null} onClose={close}>
      {current ? (
        <div className="flex flex-col gap-4">
          <div className="flex items-center gap-3">
            <div className="grid h-11 w-11 place-items-center rounded-xl bg-accent-gradient text-white shadow-glow">
              {invite ? <DoorOpen size={22} /> : <UserPlus size={22} />}
            </div>
            <div className="min-w-0">
              <div className="text-base font-semibold text-white">
                {invite ? t('incoming.inviteTitle') : t('incoming.replyTitle')}
              </div>
              {current.source === 'clipboard' ? (
                <div className="flex items-center gap-1 text-xs text-slate-500">
                  <ClipboardCheck size={12} /> {t('incoming.fromClipboard')}
                </div>
              ) : null}
            </div>
          </div>

          {info ? (
            <p className="text-sm text-slate-200">
              {invite
                ? info.host_name
                  ? t('incoming.invite', { host: info.host_name, room })
                  : t('incoming.inviteNoName', { room })
                : info.guest_name
                  ? t('incoming.reply', { guest: info.guest_name, room })
                  : t('incoming.replyNoName', { room })}
            </p>
          ) : null}

          {info?.mode === 'l2' ? <span className="chip self-start border-accent-2/40 text-accent-2">{t('incoming.classic')}</span> : null}
          {info && !info.valid ? <Banner tone="error">{t('incoming.invalid', { problem: info.problem ?? '' })}</Banner> : null}
          {info && invite && info.joined_here ? <Banner tone="warn">{t('incoming.alreadyJoined')}</Banner> : null}
          {info && !invite && !info.hosted_here ? <Banner tone="warn">{t('incoming.notForUs')}</Banner> : null}
          {current.error ? <Banner tone="error">{current.error}</Banner> : null}

          <div className="flex justify-end gap-2">
            <button type="button" className="btn" onClick={close}>
              {t('common.cancel')}
            </button>
            {info && !blocked ? (
              <button type="button" className="btn btn-primary" onClick={() => void act()} disabled={busy}>
                {busy ? t('rooms.accepting') : invite ? t('incoming.join') : t('incoming.accept')}
              </button>
            ) : null}
          </div>
        </div>
      ) : null}
    </Modal>
  )
}
