import { ShieldCheck } from 'lucide-react'

import { Card, SectionTitle } from '../../components/ui'
import { useT } from '../../i18n'
import type { Settings } from '../../types/protocol'

/** Settings → Network: a free Metered relay as the automatic fallback path. */
export function SmartRelay({ draft, setDraft, disabled }: { draft: Settings; setDraft: (s: Settings) => void; disabled: boolean }) {
  const t = useT()
  const m = draft.metered ?? { app: '', key: '' }
  const set = (patch: Partial<typeof m>) => setDraft({ ...draft, metered: { ...m, ...patch } })
  const on = Boolean(m.app && m.key)
  return (
    <Card className={on ? 'border-emerald-400/30' : ''}>
      <SectionTitle icon={<ShieldCheck size={16} />} extra={on ? <span className="chip border-emerald-400/40 text-emerald-300">{t('relay.on')}</span> : null}>
        {t('relay.title')}
      </SectionTitle>
      <p className="mb-2 text-xs text-slate-400">{t('relay.what')}</p>
      <ol className="mb-3 list-decimal space-y-1 ps-5 text-xs text-slate-500">
        <li>{t('relay.step1')}</li>
        <li>{t('relay.step2')}</li>
        <li>{t('relay.step3')}</li>
      </ol>
      <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
        <label className="text-xs text-slate-400">
          {t('relay.app')}
          <div className="ltr mt-1 flex items-center gap-1">
            <input className="input ltr text-xs" value={m.app} placeholder="myapp" disabled={disabled} onChange={(e) => set({ app: e.target.value })} />
            <span className="text-slate-500">.metered.live</span>
          </div>
        </label>
        <label className="text-xs text-slate-400">
          {t('relay.key')}
          <input className="input ltr mt-1 text-xs" type="password" value={m.key} placeholder="••••••••" disabled={disabled} onChange={(e) => set({ key: e.target.value })} />
        </label>
      </div>
      <p className="mt-3 text-xs text-slate-500">{t('relay.note')}</p>
    </Card>
  )
}
