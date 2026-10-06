import { Check, Gamepad2, LogIn, Send, ShieldCheck, Trash2, UserPlus, Users, X } from 'lucide-react'
import { useEffect, useState, type FormEvent } from 'react'

import { Avatar, Banner, Card, CopyButton, EmptyState, PageHeader, SectionTitle } from '../components/ui'
import { useT } from '../i18n'
import { useDaemonStore } from '../stores/daemon'
import { useFriendsStore } from '../stores/friends'
import { useRoomsStore } from '../stores/rooms'
import type { Friend } from '../types/friends'

export function FriendsPage() {
  const t = useT()
  const connected = useDaemonStore((s) => s.connection === 'connected')
  const { code, enabled, friends, busy, load, add } = useFriendsStore()
  const [input, setInput] = useState('')
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (connected) void load()
  }, [connected, load])

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError(null)
    try {
      await add(input)
      setInput('')
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    }
  }

  const incoming = friends.filter((f) => f.state === 'pending_in')
  const outgoing = friends.filter((f) => f.state === 'pending_out')
  const confirmed = friends.filter((f) => f.state === 'friend')
  const online = confirmed.filter((f) => f.online)
  const offline = confirmed.filter((f) => !f.online)

  return (
    <>
      <PageHeader title={t('friends.title')} subtitle={t('friends.subtitle')} />
      {!enabled && connected ? <Banner tone="warn">{t('friends.disabled')}</Banner> : null}

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <Card>
          <SectionTitle icon={<Users size={16} />}>{t('friends.myCode')}</SectionTitle>
          <p className="mb-3 text-xs text-slate-500">{t('friends.myCodeHint')}</p>
          <div className="flex items-center gap-3">
            <span className="ltr select-all rounded-lg bg-white/[0.04] px-4 py-2 font-mono text-xl tracking-widest text-white">{code || '…'}</span>
            {code ? <CopyButton value={code} label={t('common.copy')} className="btn btn-sm" /> : null}
          </div>
        </Card>
        <Card>
          <SectionTitle icon={<UserPlus size={16} />}>{t('friends.add')}</SectionTitle>
          <form onSubmit={(e) => void submit(e)} className="flex gap-2">
            <input
              className="input ltr flex-1 font-mono uppercase tracking-wider"
              value={input}
              placeholder="LBZ-XXXXX-XXXXX"
              onChange={(e) => setInput(e.target.value)}
              disabled={!connected || busy}
            />
            <button type="submit" className="btn btn-primary" disabled={!connected || busy || input.trim().length < 10}>
              <Send size={14} /> {busy ? t('friends.adding') : t('friends.send')}
            </button>
          </form>
          {error ? <p className="mt-2 text-xs text-red-300">{error}</p> : null}
        </Card>
      </div>

      {incoming.length > 0 ? (
        <Card>
          <SectionTitle icon={<UserPlus size={16} />} extra={<span className="chip">{incoming.length}</span>}>
            {t('friends.requests')}
          </SectionTitle>
          <ul className="flex flex-col gap-2">
            {incoming.map((f) => (
              <FriendRow key={f.pub} f={f} />
            ))}
          </ul>
        </Card>
      ) : null}

      <Card>
        <SectionTitle icon={<Users size={16} />} extra={<span className="chip">{online.length} / {confirmed.length}</span>}>
          {t('friends.list')}
        </SectionTitle>
        {confirmed.length === 0 && outgoing.length === 0 ? (
          <EmptyState icon={<Users size={26} />} title={t('friends.empty')} />
        ) : (
          <ul className="flex flex-col gap-2">
            {[...online, ...offline, ...outgoing].map((f) => (
              <FriendRow key={f.pub} f={f} />
            ))}
          </ul>
        )}
      </Card>
    </>
  )
}

function FriendRow({ f }: { f: Friend }) {
  const t = useT()
  const { respond, remove, trust, join, invite } = useFriendsStore()
  const hosting = useRoomsStore((s) => s.order.some((id) => s.rooms[id]?.is_host))
  const inTheirRoom = useRoomsStore((s) => (f.room_id ? Boolean(s.rooms[f.room_id]) : false))
  const [pending, setPending] = useState(false)
  const act = (fn: () => Promise<void>) => async () => {
    setPending(true)
    try {
      await fn()
    } catch {
      // the daemon reports failures as join.status events and toasts
    } finally {
      setPending(false)
    }
  }

  const status =
    f.state === 'pending_out'
      ? t('friends.waiting')
      : f.state === 'pending_in'
        ? t('friends.wantsToBeFriends')
        : !f.online
          ? t('friends.offline')
          : f.hosting
            ? t('friends.hosting', { room: f.room_name || '' })
            : t('friends.online')

  return (
    <li className="flex items-center gap-3 rounded-xl border border-white/[0.06] bg-white/[0.02] p-3">
      <div className="relative">
        <Avatar name={f.name || f.code} size={38} />
        <span
          className={`absolute -bottom-0.5 -end-0.5 h-3 w-3 rounded-full border-2 border-surface-raised ${f.online ? 'bg-emerald-400' : 'bg-slate-600'}`}
        />
      </div>
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="truncate text-sm font-medium text-slate-100">{f.name || f.code}</span>
          {f.trusted ? <ShieldCheck size={14} className="text-accent-2" aria-label={t('friends.trusted')} /> : null}
        </div>
        <div className="flex items-center gap-2 truncate text-xs text-slate-500">
          <span>{status}</span>
          {f.game ? (
            <span className="flex items-center gap-1 text-accent-2">
              <Gamepad2 size={12} /> {f.game}
            </span>
          ) : null}
        </div>
      </div>
      {f.state === 'pending_in' ? (
        <>
          <button type="button" className="btn btn-primary btn-sm" disabled={pending} onClick={act(() => respond(f.pub, true))}>
            <Check size={14} /> {t('friends.accept')}
          </button>
          <button type="button" className="btn btn-sm" disabled={pending} onClick={act(() => respond(f.pub, false))}>
            <X size={14} />
          </button>
        </>
      ) : f.state === 'friend' ? (
        <>
          {f.online && f.hosting && !inTheirRoom ? (
            <button type="button" className="btn btn-primary btn-sm" disabled={pending} onClick={act(() => join(f.pub, f.room_id))}>
              <LogIn size={14} /> {t('friends.join')}
            </button>
          ) : null}
          {hosting ? (
            <button type="button" className="btn btn-sm" disabled={pending} onClick={act(() => invite(f.pub))}>
              <Send size={14} /> {t('friends.invite')}
            </button>
          ) : null}
          <button
            type="button"
            className={`btn btn-sm ${f.trusted ? 'border-accent-2/40 text-accent-2' : ''}`}
            title={t('friends.trustHint')}
            disabled={pending}
            onClick={act(() => trust(f.pub, !f.trusted))}
          >
            <ShieldCheck size={14} />
          </button>
          <button type="button" className="btn btn-sm" title={t('friends.remove')} disabled={pending} onClick={act(() => remove(f.pub))}>
            <Trash2 size={14} />
          </button>
        </>
      ) : (
        <button type="button" className="btn btn-sm" title={t('friends.cancel')} disabled={pending} onClick={act(() => remove(f.pub))}>
          <X size={14} />
        </button>
      )}
    </li>
  )
}
