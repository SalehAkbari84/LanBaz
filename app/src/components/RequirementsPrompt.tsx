import { invoke } from '@tauri-apps/api/core'
import { AnimatePresence, motion } from 'framer-motion'
import { Download, Loader2, X } from 'lucide-react'
import { useState } from 'react'

import { useT } from '../i18n'
import { installTapDriver, isTauri } from '../services/tauri'
import { useDaemonStore } from '../stores/daemon'
import { toast } from '../stores/toasts'

/**
 * On start: if something LanBaz needs is missing (today the TAP driver for
 * classic LAN rooms), say so and install it with one click, then restart the
 * app so everything picks it up. The driver ships inside LanBaz; nothing is
 * downloaded.
 */
export function RequirementsPrompt() {
  const t = useT()
  const caps = useDaemonStore((s) => s.capabilities)
  const connected = useDaemonStore((s) => s.connection === 'connected')
  const [later, setLater] = useState(false)
  const [phase, setPhase] = useState<'ask' | 'installing' | 'restarting'>('ask')

  const missing = isTauri() && connected && caps !== null && !caps.classic_lan && !later
  const install = async () => {
    setPhase('installing')
    try {
      await installTapDriver()
      setPhase('restarting')
      toast({ tone: 'ok', title: t('req.installed') })
      await new Promise((r) => setTimeout(r, 1200))
      await invoke('restart_app')
    } catch (e) {
      setPhase('ask')
      toast({ tone: 'error', title: t('req.failed'), body: e instanceof Error ? e.message : String(e) }, 10000)
    }
  }

  return (
    <AnimatePresence>
      {missing ? (
        <motion.div
          key="req"
          initial={{ opacity: 0, y: 16 }}
          animate={{ opacity: 1, y: 0 }}
          exit={{ opacity: 0, y: 16 }}
          className="glass fixed bottom-24 end-5 z-50 w-80 bg-surface-raised/95 p-4"
          role="dialog"
          aria-label={t('req.title')}
        >
          <div className="flex items-start gap-3">
            <Download size={18} className="mt-0.5 shrink-0 text-accent-2" />
            <div className="min-w-0 flex-1">
              <div className="text-sm font-semibold text-white">{t('req.title')}</div>
              <p className="mt-1 text-xs text-slate-400">{t('req.tap')}</p>
              <div className="mt-3 flex gap-2">
                <button type="button" className="btn btn-primary btn-sm" disabled={phase !== 'ask'} onClick={() => void install()}>
                  {phase === 'ask' ? null : <Loader2 size={14} className="animate-spin" />}
                  {phase === 'installing' ? t('req.installing') : phase === 'restarting' ? t('req.restarting') : t('req.install')}
                </button>
                {phase === 'ask' ? (
                  <button type="button" className="btn btn-sm" onClick={() => setLater(true)}>
                    {t('update.askLater')}
                  </button>
                ) : null}
              </div>
            </div>
            {phase === 'ask' ? (
              <button type="button" className="text-slate-500 hover:text-slate-200" onClick={() => setLater(true)} aria-label={t('update.askLater')}>
                <X size={16} />
              </button>
            ) : null}
          </div>
        </motion.div>
      ) : null}
    </AnimatePresence>
  )
}
