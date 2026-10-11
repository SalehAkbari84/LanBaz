import { Loader2, Plus, Radio, Users } from 'lucide-react'
import { useCallback, useEffect, useState } from 'react'

import { Card, CopyButton, Row, SectionTitle, Switch } from '../../components/ui'
import { useT } from '../../i18n'
import { daemonClient, reloadSettings, useDaemonStore } from '../../stores/daemon'
import { toast } from '../../stores/toasts'
import type { FriendRelayStatus, Settings } from '../../types/protocol'

/**
 * Settings → Network: playing during an international outage.
 *  - Be a relay for friends (needs a home line with a public address, or a
 *    router that forwards ports); share the code.
 *  - Use a friend's relay: paste their code.
 *  - Extra (domestic) relays for friends and online status.
 */
export function FriendRelay({ draft, setDraft, disabled }: { draft: Settings; setDraft: (s: Settings) => void; disabled: boolean }) {
  const t = useT()
  const connected = useDaemonStore((s) => s.connection === 'connected')
  const [st, setSt] = useState<FriendRelayStatus | null>(null)
  const [busy, setBusy] = useState(false)
  const [code, setCode] = useState('')
  const [nostr, setNostr] = useState((draft.nostr_relays ?? []).join('\n'))
  useEffect(() => setNostr((draft.nostr_relays ?? []).join('\n')), [draft.nostr_relays])

  const load = useCallback(async () => {
    const c = daemonClient()
    if (!c) return
    try {
      setSt(await c.request<FriendRelayStatus>('relay.friend.status'))
    } catch {
      setSt(null)
    }
  }, [])
  useEffect(() => {
    if (connected) void load()
  }, [connected, load])

  const toggle = async (on: boolean) => {
    const c = daemonClient()
    if (!c) return
    setBusy(true)
    try {
      setSt(await c.request<FriendRelayStatus>('relay.friend.set', { enabled: on }, 90_000))
      await reloadSettings()
    } catch (e) {
      toast({ tone: 'error', title: t('frelay.failed'), body: e instanceof Error ? e.message : String(e) }, 9000)
    } finally {
      setBusy(false)
    }
  }

  const add = async () => {
    const c = daemonClient()
    if (!c || !code.trim()) return
    setBusy(true)
    try {
      await c.request('relay.friend.add', { code: code.trim() })
      await reloadSettings()
      setCode('')
      toast({ tone: 'ok', title: t('frelay.added'), key: 'frelay' })
    } catch (e) {
      toast({ tone: 'error', title: t('frelay.badCode'), body: e instanceof Error ? e.message : String(e) }, 9000)
    } finally {
      setBusy(false)
    }
  }

  const problem = (e?: string): string | null => {
    if (!e) return null
    if (e === 'cgnat') return t('frelay.errCgnat')
    if (e.startsWith('no_router')) return t('frelay.errRouter')
    if (e.startsWith('router')) return t('frelay.errForward')
    return e
  }

  return (
    <Card className={st?.running ? 'border-emerald-400/30' : ''}>
      <SectionTitle icon={<Radio size={16} />} extra={st?.running ? <span className="chip border-emerald-400/40 text-emerald-300">{t('frelay.live')}</span> : null}>
        {t('frelay.title')}
      </SectionTitle>
      <p className="mb-3 text-xs text-slate-400">{t('frelay.what')}</p>

      <div className="divide-y divide-white/[0.05]">
        <Row title={t('frelay.host')} hint={t('frelay.hostHint')}>
          <div className="flex items-center gap-2">
            {busy ? <Loader2 size={14} className="animate-spin text-slate-400" /> : null}
            <Switch checked={Boolean(st?.enabled)} disabled={!connected || busy} onChange={(on) => void toggle(on)} />
          </div>
        </Row>
        {st?.enabled && !st.running ? <p className="py-2 text-xs text-amber-200/90">{problem(st.error) ?? t('frelay.starting')}</p> : null}
        {st?.running && st.code ? (
          <div className="py-3">
            <div className="mb-1 text-xs text-slate-400">
              {t('frelay.shareCode')} <span className="ltr font-mono text-slate-500">· {st.public}:{st.port} · {st.via === 'public' ? t('frelay.viaPublic') : st.via} · {st.relayed_mb} MB</span>
            </div>
            <div className="flex items-center gap-2">
              <code className="ltr block min-w-0 flex-1 truncate rounded-lg bg-black/30 px-2 py-1.5 text-[11px] text-slate-300">{st.code}</code>
              <CopyButton value={st.code} iconOnly />
            </div>
            <p className="mt-1 text-[11px] text-slate-500">{t('frelay.shareHint')}</p>
          </div>
        ) : null}

        <div className="py-3">
          <div className="mb-1 flex items-center gap-1 text-sm text-slate-100">
            <Users size={14} /> {t('frelay.use')}
          </div>
          <p className="mb-2 text-xs text-slate-500">{t('frelay.useHint')}</p>
          <div className="flex gap-2">
            <input className="input ltr flex-1 text-xs" value={code} placeholder="lanbaz-relay:…" disabled={!connected || busy} onChange={(e) => setCode(e.target.value)} />
            <button type="button" className="btn btn-sm" disabled={!connected || busy || !code.trim()} onClick={() => void add()}>
              <Plus size={14} /> {t('frelay.add')}
            </button>
          </div>
        </div>

        <div className="py-3">
          <div className="mb-1 text-sm text-slate-100">{t('frelay.nostr')}</div>
          <p className="mb-2 text-xs text-slate-500">{t('frelay.nostrHint')}</p>
          <textarea
            className="input ltr min-h-[60px] font-mono text-xs"
            value={nostr}
            placeholder="wss://relay.example.ir"
            disabled={disabled}
            onChange={(e) => {
              setNostr(e.target.value)
              setDraft({
                ...draft,
                nostr_relays: e.target.value
                  .split(/\r?\n/)
                  .map((s) => s.trim())
                  .filter(Boolean),
              })
            }}
          />
        </div>
      </div>
    </Card>
  )
}
