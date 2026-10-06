import { AnimatePresence, motion } from 'framer-motion'
import { AlertTriangle, CheckCircle2, Info, XCircle } from 'lucide-react'

import { useToasts } from '../stores/toasts'
import { Avatar } from './ui'

/** Stack of transient notifications in the bottom corner. */
export function Toaster({ compact = false }: { compact?: boolean }) {
  const toasts = useToasts((s) => s.toasts)
  const dismiss = useToasts((s) => s.dismiss)
  return (
    <div className={`pointer-events-none fixed z-50 flex flex-col gap-2 ${compact ? 'inset-x-2 bottom-2' : 'bottom-5 end-5 w-80'}`}>
      <AnimatePresence initial={false}>
        {toasts.map((t) => (
          <motion.div
            key={t.id}
            layout
            initial={{ opacity: 0, y: 12, scale: 0.96 }}
            animate={{ opacity: 1, y: 0, scale: 1 }}
            exit={{ opacity: 0, x: 40, scale: 0.96 }}
            transition={{ type: 'spring', stiffness: 420, damping: 32 }}
            onClick={() => dismiss(t.id)}
            className="glass pointer-events-auto flex cursor-pointer items-start gap-3 bg-surface-raised/90 px-3.5 py-3"
          >
            {t.who ? (
              <Avatar name={t.who} size={30} />
            ) : (
              <span className="mt-0.5">
                {t.tone === 'ok' ? (
                  <CheckCircle2 size={18} className="text-status-running" />
                ) : t.tone === 'warn' ? (
                  <AlertTriangle size={18} className="text-status-stopping" />
                ) : t.tone === 'error' ? (
                  <XCircle size={18} className="text-status-error" />
                ) : (
                  <Info size={18} className="text-accent" />
                )}
              </span>
            )}
            <div className="min-w-0">
              <div className="truncate text-sm font-medium text-slate-100">{t.title}</div>
              {t.body ? <div className="mt-0.5 line-clamp-3 break-words text-xs text-slate-400">{t.body}</div> : null}
            </div>
          </motion.div>
        ))}
      </AnimatePresence>
    </div>
  )
}
