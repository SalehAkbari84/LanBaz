import { AnimatePresence, motion } from 'framer-motion'
import { MessageSquare, SendHorizontal } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'

import { Avatar, Card, EmptyState, PageHeader } from '../components/ui'
import { useT } from '../i18n'
import { useChatStore } from '../stores/chat'
import { usePrefs } from '../stores/prefs'
import { useRoomsStore } from '../stores/rooms'
import type { ChatMessage, RoomSummary } from '../types/protocol'

export function ChatPage() {
  const t = useT()
  const rooms = useRoomsStore((s) => s.rooms)
  const order = useRoomsStore((s) => s.order)
  const list = useMemo(() => order.map((id) => rooms[id]).filter((r): r is RoomSummary => Boolean(r)), [order, rooms])
  const active = useChatStore((s) => s.activeRoom)
  const setActive = useChatStore((s) => s.setActiveRoom)

  const roomId = active && rooms[active] ? active : list[0]?.room_id ?? null

  useEffect(() => {
    if (roomId && roomId !== active) setActive(roomId)
  }, [roomId, active, setActive])

  return (
    <>
      <PageHeader title={t('chat.title')} />
      {list.length === 0 || !roomId ? (
        <Card>
          <EmptyState icon={<MessageSquare size={26} />} title={t('chat.noRoom')} />
        </Card>
      ) : (
        <>
          {list.length > 1 ? (
            <div className="flex flex-wrap gap-2">
              {list.map((r) => (
                <button
                  key={r.room_id}
                  type="button"
                  className={`btn btn-sm ${r.room_id === roomId ? 'border-accent/50 bg-accent/15 text-white' : ''}`}
                  onClick={() => setActive(r.room_id)}
                >
                  {r.name}
                </button>
              ))}
            </div>
          ) : null}
          <ChatPanel roomId={roomId} />
        </>
      )}
    </>
  )
}

/** Message list + composer. Also used by the overlay in compact form. */
export function ChatPanel({ roomId, compact = false, autoFocus = false }: { roomId: string; compact?: boolean; autoFocus?: boolean }) {
  const t = useT()
  const messages = useChatStore((s) => s.messages[roomId])
  const send = useChatStore((s) => s.send)
  const sending = useChatStore((s) => s.sending)
  const error = useChatStore((s) => s.error)
  const lang = usePrefs((s) => s.language)
  const [text, setText] = useState('')
  const end = useRef<HTMLDivElement | null>(null)
  const input = useRef<HTMLInputElement | null>(null)
  const list = messages ?? []

  useEffect(() => {
    end.current?.scrollIntoView({ behavior: 'smooth', block: 'end' })
  }, [list.length])

  useEffect(() => {
    if (autoFocus) input.current?.focus()
  }, [autoFocus])

  const submit = async () => {
    const v = text.trim()
    if (!v) return
    setText('')
    try {
      await send(roomId, v)
    } catch {
      setText(v)
    }
  }

  return (
    <div className={`flex flex-col ${compact ? 'h-full gap-2' : 'glass h-[calc(100vh-230px)] min-h-[380px] p-4'}`}>
      <div className={`flex-1 overflow-y-auto ${compact ? 'pe-1' : 'pe-2'}`}>
        {list.length === 0 ? (
          <div className="grid h-full place-items-center text-sm text-slate-500">{t('chat.empty')}</div>
        ) : (
          <ul className="flex flex-col gap-2">
            <AnimatePresence initial={false}>
              {list.map((m, i) => (
                <Bubble key={m.id} m={m} grouped={i > 0 && list[i - 1]?.from === m.from} compact={compact} lang={lang} />
              ))}
            </AnimatePresence>
          </ul>
        )}
        <div ref={end} />
      </div>
      {error ? <div className="mt-1 text-xs text-status-error">{error}</div> : null}
      <form
        className={`flex items-center gap-2 ${compact ? '' : 'mt-3'}`}
        onSubmit={(e) => {
          e.preventDefault()
          void submit()
        }}
      >
        <input
          ref={input}
          className={`input ${compact ? 'py-1.5 text-xs' : ''}`}
          value={text}
          maxLength={500}
          placeholder={t('chat.placeholder')}
          onChange={(e) => setText(e.target.value)}
        />
        <button type="submit" className={`btn btn-primary ${compact ? 'btn-sm' : ''}`} disabled={sending || !text.trim()} aria-label={t('chat.send')}>
          <SendHorizontal size={compact ? 14 : 16} className="rtl:-scale-x-100" />
        </button>
      </form>
    </div>
  )
}

function Bubble({ m, grouped, compact, lang }: { m: ChatMessage; grouped: boolean; compact: boolean; lang: string }) {
  const time = new Date(m.at).toLocaleTimeString(lang === 'fa' ? 'fa-IR' : 'en-US', { hour: '2-digit', minute: '2-digit' })
  return (
    <motion.li
      layout
      initial={{ opacity: 0, y: 6 }}
      animate={{ opacity: 1, y: 0 }}
      className={`flex items-end gap-2 ${m.self ? 'flex-row-reverse' : ''} ${grouped ? '-mt-1' : ''}`}
    >
      {compact ? null : grouped ? <span className="w-8 shrink-0" /> : <Avatar name={m.name || '?'} size={32} />}
      <div className={`max-w-[78%] ${m.self ? 'items-end' : 'items-start'} flex flex-col`}>
        {!grouped && !m.self ? <span className="mb-0.5 px-1 text-[11px] text-slate-500">{m.name}</span> : null}
        <div
          className={`whitespace-pre-wrap break-words rounded-2xl px-3 py-2 ${compact ? 'text-xs' : 'text-sm'} ${
            m.self ? 'bg-accent-gradient text-white' : 'border border-white/[0.07] bg-white/[0.05] text-slate-100'
          }`}
          dir="auto"
        >
          {m.text}
        </div>
        {compact ? null : <span className="mt-0.5 px-1 text-[10px] text-slate-600">{time}</span>}
      </div>
    </motion.li>
  )
}
