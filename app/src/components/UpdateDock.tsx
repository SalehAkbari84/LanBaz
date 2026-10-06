import { AnimatePresence, motion } from 'framer-motion'
import { Download, Loader2, X } from 'lucide-react'

import { useT } from '../i18n'
import { useRoomsStore } from '../stores/rooms'
import { useUpdates } from '../stores/updates'

const mb = (n: number) => (n / 1048576).toFixed(1)

/**
 * The update on the main screen: a question when a new version is found, then
 * a small download box, then "closing rooms and installing".
 */
export function UpdateDock() {
  const t = useT()
  const { prompt, phase, progress, install, snooze, status } = useUpdates()
  const inRooms = useRoomsStore((s) => Object.keys(s.rooms).length)
  const pct = progress?.total ? Math.min(100, Math.round((progress.done / progress.total) * 100)) : null

  return (
    <AnimatePresence mode="wait">
      {prompt && phase === 'idle' ? (
        <motion.div
          key="ask"
          initial={{ opacity: 0, y: -16 }}
          animate={{ opacity: 1, y: 0 }}
          exit={{ opacity: 0, y: -16 }}
          className="glass fixed top-5 end-5 z-50 w-80 bg-surface-raised/95 p-4"
          role="dialog"
          aria-label={t('update.askTitle', { version: prompt.version })}
        >
          <div className="flex items-start gap-3">
            <Download size={18} className="mt-0.5 shrink-0 text-accent" />
            <div className="min-w-0 flex-1">
              <div className="text-sm font-semibold text-white">{t('update.askTitle', { version: prompt.version })}</div>
              <div className="ltr mt-0.5 text-[11px] text-slate-500">
                v{status?.current ?? '?'} → v{prompt.version}
              </div>
              {prompt.notes ? <p dir="auto" className="mt-2 max-h-24 overflow-auto whitespace-pre-wrap text-xs text-slate-300">{prompt.notes}</p> : null}
              {inRooms ? <p className="mt-2 text-xs text-amber-200/90">{t('update.askRooms')}</p> : null}
              <div className="mt-3 flex gap-2">
                <button type="button" className="btn btn-primary btn-sm" onClick={() => void install()}>
                  {t('update.askYes')}
                </button>
                <button type="button" className="btn btn-sm" onClick={snooze}>
                  {t('update.askLater')}
                </button>
              </div>
            </div>
            <button type="button" className="text-slate-500 hover:text-slate-200" onClick={snooze} aria-label={t('update.askLater')}>
              <X size={16} />
            </button>
          </div>
        </motion.div>
      ) : null}

      {phase !== 'idle' ? (
        <motion.div
          key="dl"
          initial={{ opacity: 0, y: -16 }}
          animate={{ opacity: 1, y: 0 }}
          exit={{ opacity: 0, y: -16 }}
          className="glass fixed top-5 end-5 z-50 w-72 bg-surface-raised/95 p-3"
          role="status"
          aria-live="polite"
        >
          <div className="flex items-center gap-2 text-xs text-slate-200">
            <Loader2 size={14} className="animate-spin text-accent" />
            {phase === 'downloading' ? (pct === null ? t('update.downloading') : t('update.downloadingPct', { pct })) : t('update.applying')}
          </div>
          {phase === 'downloading' ? (
            <>
              <div className="mt-2 h-1.5 overflow-hidden rounded-full bg-white/10">
                <div className="h-full rounded-full bg-accent transition-[width]" style={{ width: `${pct ?? 5}%` }} />
              </div>
              {progress ? (
                <div className="ltr mt-1 text-[11px] text-slate-500">
                  {mb(progress.done)}
                  {progress.total ? ` / ${mb(progress.total)}` : ''} MB
                </div>
              ) : null}
            </>
          ) : (
            <p className="mt-1 text-[11px] text-slate-500">{t('update.applyingNote')}</p>
          )}
        </motion.div>
      ) : null}

    </AnimatePresence>
  )
}
