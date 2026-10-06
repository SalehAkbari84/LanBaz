import { ShieldAlert } from 'lucide-react'
import { useState } from 'react'

import { useT } from '../i18n'
import { daemonClient } from '../stores/daemon'
import { useGameFirewall, type GameFirewall } from '../stores/gameFirewall'
import { toast } from '../stores/toasts'

/** Windows Firewall blocks the running game: explain, and fix on request. */
export function GameFirewallBanner() {
  const t = useT()
  const fw = useGameFirewall((s) => s.fw)
  const [busy, setBusy] = useState(false)
  if (!fw?.blocked?.length) return null
  const fix = async () => {
    setBusy(true)
    try {
      const next = await daemonClient()?.request<GameFirewall>('game.firewall_fix', {}, 60_000)
      if (next) useGameFirewall.setState({ fw: next })
      toast({ tone: 'ok', title: t('fw.fixed', { game: fw.game_name ?? '' }) })
    } catch (e) {
      toast({ tone: 'error', title: t('fw.failed'), body: e instanceof Error ? e.message : String(e) }, 9000)
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className="flex flex-wrap items-center gap-3 rounded-2xl border border-amber-400/30 bg-amber-400/[0.06] p-4">
      <ShieldAlert size={20} className="shrink-0 text-amber-300" />
      <div className="min-w-0 flex-1 text-sm">
        <div className="font-medium text-amber-100">{t('fw.title', { game: fw.game_name ?? '' })}</div>
        <div className="mt-0.5 text-xs text-amber-200/80">{t('fw.body')}</div>
      </div>
      <button type="button" className="btn btn-primary btn-sm" disabled={busy} onClick={() => void fix()}>
        {busy ? t('fw.fixing') : t('fw.fix')}
      </button>
    </div>
  )
}
