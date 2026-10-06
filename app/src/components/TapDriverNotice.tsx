import { Download, RefreshCw } from 'lucide-react'
import { useState } from 'react'

import { useT } from '../i18n'
import { installTapDriver } from '../services/tauri'
import { useDaemonStore } from '../stores/daemon'
import { toast } from '../stores/toasts'

/** Shown wherever Classic LAN is chosen and the driver is not installed yet. */
export function TapDriverNotice() {
  const t = useT()
  const caps = useDaemonStore((s) => s.capabilities)
  const refresh = useDaemonStore((s) => s.refreshCapabilities)
  const [busy, setBusy] = useState(false)
  if (!caps || caps.classic_lan) return null

  const install = async () => {
    setBusy(true)
    try {
      await installTapDriver()
      await refresh()
      toast({ tone: 'ok', title: t('adv.driverInstalled') })
    } catch (err) {
      toast({ tone: 'error', title: err instanceof Error ? err.message : String(err) })
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="flex flex-wrap items-center gap-3 rounded-xl border border-accent-2/30 bg-accent-2/[0.06] px-3 py-2.5 text-xs text-slate-300">
      <span className="flex-1">{t('adv.driverMissing')}</span>
      <button type="button" className="btn btn-sm" onClick={() => void install()} disabled={busy}>
        {busy ? <RefreshCw size={14} className="animate-spin" /> : <Download size={14} />}
        {busy ? t('adv.installing') : t('adv.installDriver')}
      </button>
    </div>
  )
}
