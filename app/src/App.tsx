import { AnimatePresence, motion } from 'framer-motion'
import { useEffect } from 'react'

import { IncomingCode } from './components/IncomingCode'
import { Sidebar } from './components/Sidebar'
import { Toaster } from './components/Toaster'
import { UpdateDock } from './components/UpdateDock'
import { RequirementsPrompt } from './components/RequirementsPrompt'
import { useOverlayVoiceListener, useTraySync, useVoiceBroadcast } from './services/bridges'
import { useDocumentDirection } from './i18n'
import { JoinPrompts } from './components/JoinPrompts'
import { ChatPage } from './pages/ChatPage'
import { FriendsPage } from './pages/FriendsPage'
import { GamesPage } from './pages/GamesPage'
import { HomePage } from './pages/HomePage'
import { OverlayPage } from './pages/OverlayPage'
import { RoomsPage } from './pages/RoomsPage'
import { SettingsPage } from './pages/SettingsPage'
import { isOverlaySurface } from './services/overlay'
import { useChatStore } from './stores/chat'
import { useDaemonStore } from './stores/daemon'
import { useUpdates } from './stores/updates'

export default function App() {
  const page = useDaemonStore((s) => s.page)
  const connect = useDaemonStore((s) => s.connect)
  useDocumentDirection()

  // Which webview this is decides which app renders. Both surfaces share one
  // bundle and one set of stores; only the chrome differs.
  const overlay = isOverlaySurface()

  useEffect(() => {
    void connect()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // Updates: shortly after start, then every 10 minutes; a new version is
  // asked about on the main screen (UpdateDock). Main window only.
  useEffect(() => {
    if (overlay) return
    const run = () => void useUpdates.getState().checkAndAsk()
    const first = setTimeout(run, 15000)
    const every = setInterval(run, 10 * 60 * 1000)
    return () => {
      clearTimeout(first)
      clearInterval(every)
    }
  }, [overlay])

  // The overlay window is transparent; painting the body would put an opaque
  // rectangle over the game.
  useEffect(() => {
    if (!overlay) return
    document.body.classList.add('overlay-surface')
    return () => document.body.classList.remove('overlay-surface')
  }, [overlay])

  // Unread counting needs to know when the chat page is on screen.
  useEffect(() => {
    useChatStore.getState().setViewing(page === 'chat')
  }, [page])

  if (overlay) {
    return (
      <>
        <OverlayBridges />
        <OverlayPage />
      </>
    )
  }

  return (
    <div className="flex h-full">
      <Sidebar />
      <main className="relative flex-1 overflow-y-auto">
        <AnimatePresence mode="wait">
          <motion.div
            key={page}
            initial={{ opacity: 0, y: 8 }}
            animate={{ opacity: 1, y: 0 }}
            exit={{ opacity: 0, y: -6 }}
            transition={{ duration: 0.18 }}
            className="mx-auto flex max-w-6xl flex-col gap-6 p-8"
          >
            {page === 'home' ? (
              <HomePage />
            ) : page === 'friends' ? (
              <FriendsPage />
            ) : page === 'rooms' ? (
              <RoomsPage />
            ) : page === 'chat' ? (
              <ChatPage />
            ) : page === 'games' ? (
              <GamesPage />
            ) : (
              <SettingsPage />
            )}
          </motion.div>
        </AnimatePresence>
      </main>
      <IncomingCode />
      <JoinPrompts />
      <Toaster />
      <UpdateDock />
      <RequirementsPrompt />
      <MainBridges />
    </div>
  )
}

function MainBridges() {
  useTraySync()
  useVoiceBroadcast()
  return null
}

function OverlayBridges() {
  useOverlayVoiceListener()
  return null
}
