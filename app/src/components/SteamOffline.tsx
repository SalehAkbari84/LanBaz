import { invoke } from '@tauri-apps/api/core'
import { Check, CloudOff, FolderSearch, Loader2, Play, RefreshCw, Rocket, Wifi, X } from 'lucide-react'
import { useCallback, useEffect, useState } from 'react'

import { useT } from '../i18n'
import { isTauri, openGameUri } from '../services/tauri'
import { daemonClient, useDaemonStore } from '../stores/daemon'
import { toast } from '../stores/toasts'
import { Card, SectionTitle } from './ui'

interface SteamGame {
  app_id: string
  name: string
  install_dir: string
  profile_id?: string
  profile_name?: string
  needs_l2?: boolean
  availability?: string[]
  exe?: string
  exe_chosen?: boolean
}

interface SteamStatus {
  installed: boolean
  path?: string
  running: boolean
  account?: { name: string; remembered: boolean; offline: boolean; last_login?: string }
  games: SteamGame[]
}

/**
 * Steam during an internet outage, with Steam's own Offline Mode: a readiness
 * check, one click to switch Steam to offline, and the installed games with
 * a launch button. Nothing here gets around Steam - a game that needs Steam
 * still runs through it, offline.
 */
export function SteamOffline() {
  const t = useT()
  const connected = useDaemonStore((s) => s.connection === 'connected')
  const [st, setSt] = useState<SteamStatus | null>(null)
  const [busy, setBusy] = useState(false)
  const [open, setOpen] = useState(false)

  const load = useCallback(async () => {
    const c = daemonClient()
    if (!c) return
    try {
      setSt(await c.request<SteamStatus>('steam.status', {}, 60_000))
    } catch {
      setSt(null)
    }
  }, [])

  useEffect(() => {
    if (connected) void load()
  }, [connected, load])

  const setOffline = async (on: boolean) => {
    const c = daemonClient()
    if (!c) return
    setBusy(true)
    try {
      setSt(await c.request<SteamStatus>('steam.set_offline', { on }, 30_000))
      toast({ tone: 'ok', title: on ? t('steam.offlineOn') : t('steam.offlineOff'), key: 'steam' })
    } catch (e) {
      toast({ tone: 'error', title: t('steam.failed'), body: e instanceof Error ? e.message : String(e) }, 10_000)
      void load()
    } finally {
      setBusy(false)
    }
  }

  const chooseExe = async (g: SteamGame) => {
    const c = daemonClient()
    if (!c || !isTauri()) return
    const picked = await invoke<string[]>('pick_files', { title: t('steam.pickExe', { game: g.name }) }).catch(() => [])
    if (!picked[0]) return
    try {
      setSt(await c.request<SteamStatus>('steam.set_exe', { app_id: g.app_id, path: picked[0] }))
    } catch (e) {
      toast({ tone: 'error', title: t('steam.failed'), body: e instanceof Error ? e.message : String(e) }, 8000)
    }
  }

  const launchSteam = (g: SteamGame) => void openGameUri(`steam://rungameid/${g.app_id}`).catch((e) => toast({ tone: 'error', title: t('steam.failed'), body: String(e) }))
  const launchDirect = (g: SteamGame) => {
    if (!g.exe) return
    void invoke('launch_exe', { path: g.exe }).catch((e) => toast({ tone: 'error', title: t('steam.failed'), body: String(e) }))
  }

  if (!st) return null
  if (!st.installed) {
    return (
      <Card>
        <SectionTitle icon={<CloudOff size={16} />}>{t('steam.title')}</SectionTitle>
        <p className="text-sm text-slate-400">{t('steam.notInstalled')}</p>
      </Card>
    )
  }

  const acc = st.account
  const ready = Boolean(acc?.remembered)
  const last = acc?.last_login ? new Date(acc.last_login) : null
  const item = (ok: boolean, text: string, hint?: string) => (
    <li className="flex items-start gap-2">
      {ok ? <Check size={15} className="mt-0.5 shrink-0 text-emerald-400" /> : <X size={15} className="mt-0.5 shrink-0 text-amber-300" />}
      <div>
        <div className="text-sm text-slate-200">{text}</div>
        {hint ? <div className="text-xs text-slate-500">{hint}</div> : null}
      </div>
    </li>
  )

  return (
    <Card>
      <SectionTitle
        icon={<CloudOff size={16} />}
        extra={
          <button type="button" className="btn btn-ghost btn-sm" onClick={() => void load()} aria-label={t('steam.refresh')} title={t('steam.refresh')}>
            <RefreshCw size={14} />
          </button>
        }
      >
        {t('steam.title')}
      </SectionTitle>
      <p className="mb-3 text-xs text-slate-400">{t('steam.intro')}</p>
      <ul className="space-y-2">
        {item(true, t('steam.installed'))}
        {item(Boolean(acc), acc ? t('steam.account', { name: acc.name }) : t('steam.noAccount'), acc ? undefined : t('steam.noAccountHint'))}
        {acc ? item(ready, ready ? t('steam.remembered') : t('steam.notRemembered'), ready ? (last ? t('steam.lastLogin', { date: last.toLocaleDateString() }) : undefined) : t('steam.notRememberedHint')) : null}
        {acc ? item(acc.offline, acc.offline ? t('steam.isOffline') : t('steam.isOnline'), acc.offline ? t('steam.isOfflineHint') : t('steam.isOnlineHint')) : null}
      </ul>
      <div className="mt-4 flex flex-wrap items-center gap-2">
        {acc?.offline ? (
          <button type="button" className="btn btn-sm" disabled={busy} onClick={() => void setOffline(false)}>
            {busy ? <Loader2 size={14} className="animate-spin" /> : <Wifi size={14} />} {t('steam.goOnline')}
          </button>
        ) : (
          <button type="button" className="btn btn-primary btn-sm" disabled={busy || !ready} onClick={() => void setOffline(true)}>
            {busy ? <Loader2 size={14} className="animate-spin" /> : <CloudOff size={14} />} {t('steam.goOffline')}
          </button>
        )}
        {st.running ? <span className="text-xs text-amber-200/80">{t('steam.running')}</span> : null}
      </div>

      {st.games.length ? (
        <details className="mt-4" open={open} onToggle={(e) => setOpen((e.target as HTMLDetailsElement).open)}>
          <summary className="cursor-pointer text-sm text-slate-300">{t('steam.games', { n: st.games.length })}</summary>
          <ul className="mt-2 divide-y divide-white/[0.05]">
            {st.games.map((g) => (
              <li key={g.app_id} className="flex flex-wrap items-center gap-2 py-2">
                <div className="min-w-0 flex-1">
                  <div className="truncate text-sm text-slate-100">{g.name}</div>
                  <div className="mt-0.5 flex flex-wrap gap-1.5">
                    {g.profile_id ? <span className="chip border-emerald-400/40 text-emerald-300">{t('steam.hasLan')}</span> : null}
                    {g.needs_l2 ? <span className="chip border-accent-2/40 text-accent-2">{t('games.needsL2')}</span> : null}
                    {g.availability?.includes('drm_free') ? <span className="chip border-sky-400/40 text-sky-300">{t('games.badgeDrmFree')}</span> : null}
                    {g.availability?.includes('free') ? <span className="chip border-emerald-400/40 text-emerald-300">{t('games.badgeFree')}</span> : null}
                  </div>
                </div>
                <button type="button" className="btn btn-sm" onClick={() => launchSteam(g)} title={t('steam.launchHint')}>
                  <Play size={13} /> {t('steam.launch')}
                </button>
                {g.exe ? (
                  <button type="button" className="btn btn-ghost btn-sm" onClick={() => launchDirect(g)} title={t('steam.launchDirectHint')}>
                    <Rocket size={13} /> {t('steam.launchDirect')}
                  </button>
                ) : null}
                <button type="button" className="btn btn-ghost btn-sm" onClick={() => void chooseExe(g)} title={t('steam.pickExeHint')} aria-label={t('steam.pickExeHint')}>
                  <FolderSearch size={13} />
                </button>
              </li>
            ))}
          </ul>
          <p className="mt-2 text-[11px] text-slate-500">{t('steam.directNote')}</p>
        </details>
      ) : null}
    </Card>
  )
}
