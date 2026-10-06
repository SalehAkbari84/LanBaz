import { motion } from 'framer-motion'
import { Gamepad2, Home, Layers, MessageSquare, MonitorUp, Network, Settings, Users } from 'lucide-react'
import type { ReactNode } from 'react'

import { useT } from '../i18n'
import { showOverlay } from '../services/overlay'
import { useChatStore } from '../stores/chat'
import { useFriendsStore } from '../stores/friends'
import { useDaemonStore, type Page } from '../stores/daemon'

const ITEMS: Array<{ id: Page; icon: ReactNode; key: 'nav.home' | 'nav.friends' | 'nav.rooms' | 'nav.chat' | 'nav.games' | 'nav.settings' }> = [
  { id: 'home', icon: <Home size={18} />, key: 'nav.home' },
  { id: 'friends', icon: <Users size={18} />, key: 'nav.friends' },
  { id: 'rooms', icon: <Layers size={18} />, key: 'nav.rooms' },
  { id: 'chat', icon: <MessageSquare size={18} />, key: 'nav.chat' },
  { id: 'games', icon: <Gamepad2 size={18} />, key: 'nav.games' },
  { id: 'settings', icon: <Settings size={18} />, key: 'nav.settings' },
]

export function Sidebar() {
  const t = useT()
  const page = useDaemonStore((s) => s.page)
  const setPage = useDaemonStore((s) => s.setPage)
  const openRooms = useDaemonStore((s) => s.openRooms)
  const connection = useDaemonStore((s) => s.connection)
  const unread = useChatStore((s) => s.unread)
  const waiting = useFriendsStore((s) => s.friends.filter((f) => f.state === 'pending_in').length + s.prompts.length)

  const go = (id: Page) => (id === 'rooms' ? openRooms() : setPage(id))
  const online = connection === 'connected'

  return (
    <aside className="flex w-60 shrink-0 flex-col border-e border-white/[0.06] bg-surface-sunken/60 px-3 py-5 backdrop-blur-xl">
      <div className="mb-8 flex items-center gap-3 px-2">
        <div className="grid h-10 w-10 place-items-center rounded-xl bg-accent-gradient shadow-glow">
          <Network size={20} className="text-white" />
        </div>
        <div>
          <div className="text-base font-bold tracking-tight text-white">{t('app.name')}</div>
          <div className="text-[11px] text-slate-500">{t('app.tagline')}</div>
        </div>
      </div>

      <nav className="flex flex-1 flex-col gap-1">
        {ITEMS.map((item) => {
          const active = page === item.id
          return (
            <button
              key={item.id}
              type="button"
              onClick={() => go(item.id)}
              aria-current={active ? 'page' : undefined}
              className={`relative flex items-center gap-3 rounded-xl px-3 py-2.5 text-sm font-medium transition-colors ${
                active ? 'text-white' : 'text-slate-400 hover:bg-white/[0.04] hover:text-slate-200'
              }`}
            >
              {active ? (
                <motion.span
                  layoutId="nav-active"
                  className="absolute inset-0 rounded-xl border border-accent/30 bg-accent/15"
                  transition={{ type: 'spring', stiffness: 500, damping: 35 }}
                />
              ) : null}
              <span className="relative">{item.icon}</span>
              <span className="relative flex-1 text-start">{t(item.key)}</span>
              {item.id === 'chat' && unread > 0 ? (
                <span className="relative rounded-full bg-accent px-1.5 text-[10px] font-semibold text-white">{unread > 99 ? '99+' : unread}</span>
              ) : null}
              {item.id === 'friends' && waiting > 0 ? (
                <span className="relative rounded-full bg-emerald-500 px-1.5 text-[10px] font-semibold text-white">{waiting}</span>
              ) : null}
            </button>
          )
        })}
      </nav>

      <button type="button" className="btn mb-3 w-full justify-start" onClick={() => void showOverlay('panel')}>
        <MonitorUp size={16} />
        <span className="flex-1 text-start">{t('nav.overlay')}</span>
        <kbd className="ltr rounded bg-white/10 px-1.5 text-[10px] text-slate-400">Ctrl+Alt+L</kbd>
      </button>

      <div className="flex items-center gap-2 rounded-xl border border-white/[0.06] bg-white/[0.02] px-3 py-2 text-xs">
        <span className={`h-2 w-2 rounded-full ${online ? 'bg-status-running' : connection === 'error' ? 'bg-status-error' : 'animate-pulse-dot bg-status-stopping'}`} />
        <span className="text-slate-300">{t(`conn.${connection}` as never)}</span>
      </div>
    </aside>
  )
}
