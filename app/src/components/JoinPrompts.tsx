import { AnimatePresence, motion } from 'framer-motion'
import { Check, ShieldCheck, X } from 'lucide-react'

import { useT } from '../i18n'
import { useFriendsStore } from '../stores/friends'
import { Avatar } from './ui'

/** Floating cards for join requests and invitations from friends. */
export function JoinPrompts() {
  const t = useT()
  const prompts = useFriendsStore((s) => s.prompts)
  const answer = useFriendsStore((s) => s.answer)
  return (
    <div className="pointer-events-none fixed end-5 top-5 z-50 flex w-96 flex-col gap-3">
      <AnimatePresence initial={false}>
        {prompts.map((p) => (
          <motion.div
            key={p.id}
            layout
            initial={{ opacity: 0, y: -12, scale: 0.97 }}
            animate={{ opacity: 1, y: 0, scale: 1 }}
            exit={{ opacity: 0, x: 40 }}
            className="glass pointer-events-auto flex flex-col gap-3 border-accent/30 bg-surface-raised/95 p-4 shadow-glow"
            role="alertdialog"
          >
            <div className="flex items-center gap-3">
              <Avatar name={p.friend.name} size={40} />
              <div className="min-w-0">
                <div className="truncate text-sm font-semibold text-white">{p.friend.name}</div>
                <div className="text-xs text-slate-400">
                  {p.kind === 'request'
                    ? t('join.promptRequest', { room: p.room_name || '' })
                    : t('join.promptInvite', { room: p.room_name || '' })}
                </div>
              </div>
            </div>
            <div className="flex gap-2">
              <button type="button" className="btn btn-primary btn-sm flex-1" onClick={() => void answer(p.id, true)}>
                <Check size={14} /> {t('friends.accept')}
              </button>
              <button type="button" className="btn btn-sm flex-1" title={t('friends.trustHint')} onClick={() => void answer(p.id, true, true)}>
                <ShieldCheck size={14} /> {t('join.always')}
              </button>
              <button type="button" className="btn btn-sm" onClick={() => void answer(p.id, false)} aria-label={t('join.decline')}>
                <X size={14} />
              </button>
            </div>
          </motion.div>
        ))}
      </AnimatePresence>
    </div>
  )
}
