import { CloudOff } from 'lucide-react'
import { useEffect, useState } from 'react'

import { useT } from '../i18n'
import { daemonClient, useDaemonStore } from '../stores/daemon'
import type { OutageStatus } from '../types/protocol'
import { Banner } from './ui'

/**
 * Says so when only the domestic network answers (an international outage),
 * and what still works: rooms with codes passed over a domestic messenger,
 * a friend's relay, Steam in offline mode. Checked at start and every few
 * minutes; the check is three TCP connections each way, nothing is sent.
 */
export function OutageBanner() {
  const t = useT()
  const connected = useDaemonStore((s) => s.connection === 'connected')
  const setPage = useDaemonStore((s) => s.setPage)
  const [st, setSt] = useState<OutageStatus | null>(null)

  useEffect(() => {
    if (!connected) return
    const check = () =>
      void daemonClient()
        ?.request<OutageStatus>('net.outage_check', {}, 20_000)
        .then(setSt)
        .catch(() => undefined)
    check()
    const id = window.setInterval(check, 3 * 60_000)
    return () => window.clearInterval(id)
  }, [connected])

  if (!st || st.mode === 'normal') return null
  if (st.mode === 'offline') return <Banner tone="warn">{t('outage.offline')}</Banner>
  return (
    <Banner tone="warn">
      <div className="flex items-start gap-2">
        <CloudOff size={16} className="mt-0.5 shrink-0" />
        <div>
          <div className="font-medium">{t('outage.title')}</div>
          <ul className="mt-1 list-disc space-y-0.5 ps-5 text-xs opacity-90">
            <li>{t('outage.codes')}</li>
            <li>{t('outage.relay')}</li>
            <li>{t('outage.steam')}</li>
            <li>{t('outage.friends')}</li>
          </ul>
          <div className="mt-2 flex gap-2">
            <button type="button" className="btn btn-sm" onClick={() => setPage('settings')}>
              {t('outage.openRelay')}
            </button>
            <button type="button" className="btn btn-ghost btn-sm" onClick={() => setPage('games')}>
              {t('outage.openSteam')}
            </button>
          </div>
        </div>
      </div>
    </Banner>
  )
}
