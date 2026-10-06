import { Download, RefreshCw } from 'lucide-react'
import { useEffect, useState } from 'react'

import { Banner, Card, SectionTitle } from '../../components/ui'
import { useT } from '../../i18n'
import { useUpdates } from '../../stores/updates'

const mb = (n: number) => (n / 1048576).toFixed(1)

/** Settings → About: check for, and install, a newer signed release. */
export function Updates() {
  const t = useT()
  const { status, available, checking, phase, progress, error, refresh, check, install, setRepo } = useUpdates()
  const [repo, setRepoDraft] = useState('')
  const [checked, setChecked] = useState(false)
  useEffect(() => {
    void refresh()
  }, [refresh])
  useEffect(() => setRepoDraft(status?.repo ?? ''), [status?.repo])

  const pct = progress?.total ? Math.min(100, Math.round((progress.done / progress.total) * 100)) : null
  return (
    <Card>
      <SectionTitle icon={<Download size={16} />} extra={status ? <span className="chip ltr font-mono">v{status.current}</span> : null}>
        {t('update.title')}
      </SectionTitle>
      <p className="mb-3 text-xs text-slate-400">{t('update.what')}</p>

      <label className="text-xs text-slate-400">
        {t('update.repo')}
        <div className="ltr mt-1 flex items-center gap-2">
          <span className="text-slate-500">github.com/</span>
          <input className="input ltr text-xs" value={repo} placeholder="owner/lanbaz" disabled={phase !== 'idle'} onChange={(e) => setRepoDraft(e.target.value)} />
          <button
            type="button"
            className="btn btn-sm"
            disabled={phase !== 'idle' || repo.trim() === (status?.repo ?? '')}
            onClick={() => void setRepo(repo).catch((e) => useUpdates.setState({ error: String(e) }))}
          >
            {t('update.saveRepo')}
          </button>
        </div>
      </label>

      <div className="mt-3 flex flex-wrap items-center gap-2">
        <button type="button" className="btn btn-sm" disabled={!status?.configured || checking || phase !== 'idle'} onClick={() => void check().then(() => setChecked(true))}>
          <RefreshCw size={14} className={checking ? 'animate-spin' : ''} />
          {checking ? t('update.checking') : t('update.check')}
        </button>
        {available ? (
          <button type="button" className="btn btn-primary btn-sm" disabled={phase !== 'idle'} onClick={() => void install()}>
            <Download size={14} />
            {phase !== 'idle' ? (pct === null ? t('update.downloading') : t('update.downloadingPct', { pct })) : t('update.install', { version: available.version })}
          </button>
        ) : checked && !checking && !error ? (
          <span className="text-xs text-emerald-300">{t('update.upToDate')}</span>
        ) : null}
        {phase !== 'idle' && progress ? (
          <span className="ltr text-xs text-slate-500">
            {mb(progress.done)}
            {progress.total ? ` / ${mb(progress.total)}` : ''} MB
          </span>
        ) : null}
      </div>

      {available?.notes ? <pre dir="auto" className="mt-3 max-h-40 overflow-auto whitespace-pre-wrap rounded-lg bg-black/20 p-3 text-xs text-slate-300">{available.notes}</pre> : null}
      {!status?.configured ? <p className="mt-3 text-xs text-slate-500">{t('update.notConfigured')}</p> : null}
      {error ? (
        <div className="mt-3">
          <Banner tone="error">{error}</Banner>
        </div>
      ) : null}
      {phase !== 'idle' ? <p className="mt-3 text-xs text-slate-500">{t('update.installNote')}</p> : null}
    </Card>
  )
}
