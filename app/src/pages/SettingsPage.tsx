import { Globe, Info, Mic, MonitorUp, Network, Plus, Save, Trash2, User } from 'lucide-react'
import { useEffect, useState } from 'react'

import { Banner, Card, PageHeader, Row, Segmented, SectionTitle, Switch } from '../components/ui'
import { useT } from '../i18n'
import { onHotkeyError, overlayStatus, showOverlay } from '../services/overlay'
import { autostartIsEnabled, daemonStateDir, isTauri, setAutostart } from '../services/tauri'
import { useDaemonStore } from '../stores/daemon'
import { usePrefs, type Corner, type Language } from '../stores/prefs'
import { applyVoicePrefs } from '../stores/voice'
import type { RelayServer, Settings } from '../types/protocol'
import { AdvancedNetwork } from './settings/AdvancedNetwork'
import { DeveloperLog } from './settings/DeveloperLog'
import { SmartRelay } from './settings/SmartRelay'
import { ConnectionCheck } from './settings/ConnectionCheck'
import { Updates } from './settings/Updates'

type Tab = 'general' | 'network' | 'overlay' | 'developer' | 'about'

export function SettingsPage() {
  const t = useT()
  const [tab, setTab] = useState<Tab>('general')
  return (
    <>
      <PageHeader title={t('settings.title')} />
      <Segmented<Tab>
        value={tab}
        onChange={setTab}
        options={[
          { value: 'general', label: t('settings.tab.general') },
          { value: 'network', label: t('settings.tab.network') },
          { value: 'overlay', label: t('settings.tab.overlay') },
          { value: 'developer', label: t('settings.tab.developer') },
          { value: 'about', label: t('settings.tab.about') },
        ]}
      />
      {tab === 'general' ? <General /> : tab === 'network' ? <NetworkTab /> : tab === 'overlay' ? <OverlayTab /> : tab === 'developer' ? <DeveloperLog /> : <About />}
    </>
  )
}

/** The daemon settings form state, shared by the General and Network tabs. */
function useDaemonSettingsForm() {
  const settings = useDaemonStore((s) => s.settings)
  const saveSettings = useDaemonStore((s) => s.saveSettings)
  const connected = useDaemonStore((s) => s.connection === 'connected')
  const [draft, setDraft] = useState<Settings | null>(settings)
  const [saving, setSaving] = useState(false)
  const [message, setMessage] = useState<{ ok: boolean; text: string } | null>(null)
  useEffect(() => setDraft(settings), [settings])

  const save = async (next: Settings) => {
    setSaving(true)
    setMessage(null)
    try {
      await saveSettings(next)
      setMessage({ ok: true, text: '' })
    } catch (err) {
      setMessage({
        ok: false,
        text: err instanceof Error ? err.message : String(err),
      })
    } finally {
      setSaving(false)
    }
  }
  return { draft, setDraft, save, saving, message, connected }
}

function General() {
  const t = useT()
  const language = usePrefs((s) => s.language)
  const prefs = usePrefs()
  const elevated = useDaemonStore((s) => s.elevated)
  const { draft, setDraft, save, saving, message, connected } = useDaemonSettingsForm()
  const [autostart, setAutostartState] = useState<boolean | null>(null)
  const [autoErr, setAutoErr] = useState<string | null>(null)

  useEffect(() => {
    if (!isTauri()) return
    void autostartIsEnabled()
      .then(setAutostartState)
      .catch(() => setAutostartState(null))
  }, [])

  const toggleAutostart = async (on: boolean) => {
    setAutoErr(null)
    try {
      setAutostartState(await setAutostart(on))
    } catch (err) {
      setAutoErr(err instanceof Error ? err.message : String(err))
    }
  }

  return (
    <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
      <Card>
        <SectionTitle icon={<User size={16} />}>{t('settings.displayName')}</SectionTitle>
        <p className="mb-3 text-xs text-slate-500">{t('settings.displayNameHint')}</p>
        <div className="flex gap-2">
          <input
            className="input"
            maxLength={32}
            value={draft?.display_name ?? ''}
            disabled={!connected || saving || !draft}
            onChange={(e) => draft && setDraft({ ...draft, display_name: e.target.value })}
          />
          <button type="button" className="btn btn-primary" disabled={!connected || saving || !draft} onClick={() => draft && void save(draft)}>
            <Save size={16} />
          </button>
        </div>
        {message ? <p className={`mt-2 text-xs ${message.ok ? 'text-status-running' : 'text-status-error'}`}>{message.ok ? t('settings.saved') : message.text}</p> : null}
      </Card>

      <Card>
        <SectionTitle icon={<Globe size={16} />}>{t('settings.language')}</SectionTitle>
        <Segmented<Language>
          value={language}
          onChange={(v) => prefs.set({ language: v })}
          options={[
            { value: 'fa', label: 'فارسی' },
            { value: 'en', label: 'English' },
          ]}
        />
        <div className="mt-4 divide-y divide-white/[0.05]">
          <Row title={t('settings.autostart')} hint={elevated === false ? t('settings.needsAdmin') : t('settings.autostartHint')}>
            <Switch checked={autostart === true} disabled={autostart === null || elevated === false} onChange={(v) => void toggleAutostart(v)} />
          </Row>
        </div>
        {autoErr ? <p className="text-xs text-status-error">{autoErr}</p> : null}
      </Card>

      <Card className="lg:col-span-2">
        <SectionTitle>{t('settings.notifications')}</SectionTitle>
        <div className="divide-y divide-white/[0.05]">
          <Row title={t('settings.toastJoinLeave')}>
            <Switch checked={prefs.toastJoinLeave} onChange={(v) => prefs.set({ toastJoinLeave: v })} />
          </Row>
          <Row title={t('settings.toastChat')}>
            <Switch checked={prefs.toastChat} onChange={(v) => prefs.set({ toastChat: v })} />
          </Row>
          <Row title={t('settings.toastGames')}>
            <Switch checked={prefs.toastGames} onChange={(v) => prefs.set({ toastGames: v })} />
          </Row>
          <Row title={t('settings.sound')}>
            <Switch checked={prefs.soundOnChat} onChange={(v) => prefs.set({ soundOnChat: v })} />
          </Row>
        </div>
      </Card>
    </div>
  )
}

function NetworkTab() {
  const t = useT()
  const { draft, setDraft, save, saving, message, connected } = useDaemonSettingsForm()
  const [stun, setStun] = useState('')
  useEffect(() => setStun((draft?.stun_servers ?? []).join('\n')), [draft?.stun_servers])
  if (!draft) return <Card>{t('common.loading')}</Card>
  const turn = draft.turn_servers ?? []
  const disabled = !connected || saving
  const updateTurn = (i: number, patch: Partial<RelayServer>) =>
    setDraft({
      ...draft,
      turn_servers: turn.map((x, j) => (j === i ? { ...x, ...patch } : x)),
    })

  const submit = () =>
    void save({
      ...draft,
      stun_servers: stun
        .split(/\r?\n/)
        .map((s) => s.trim())
        .filter(Boolean),
      turn_servers: turn.filter((x) => x.url.trim()),
      metered: draft.metered && (draft.metered.app.trim() || draft.metered.key.trim()) ? draft.metered : null,
    })

  return (
    <div className="flex flex-col gap-4">
      <ConnectionCheck />
      <Card>
        <SectionTitle icon={<Network size={16} />}>{t('settings.stun')}</SectionTitle>
        <p className="mb-3 text-xs text-slate-500">{t('settings.stunHint')}</p>
        <textarea className="input ltr h-24 font-mono text-xs" value={stun} disabled={disabled} onChange={(e) => setStun(e.target.value)} placeholder="stun:stun.cloudflare.com:3478" />
      </Card>

      <SmartRelay draft={draft} setDraft={setDraft} disabled={disabled} />

      <Card>
        <SectionTitle icon={<Network size={16} />}>{t('settings.turn')}</SectionTitle>
        <p className="mb-3 text-xs text-slate-500">{t('settings.turnHint')}</p>
        <div className="flex flex-col gap-2">
          {turn.map((x, i) => (
            <div key={i} className="grid grid-cols-1 gap-2 sm:grid-cols-[2fr_1fr_1fr_auto]">
              <input className="input ltr font-mono text-xs" value={x.url} placeholder="turn:relay.example.com:3478" disabled={disabled} onChange={(e) => updateTurn(i, { url: e.target.value })} />
              <input className="input ltr text-xs" value={x.username ?? ''} placeholder="user" disabled={disabled} onChange={(e) => updateTurn(i, { username: e.target.value })} />
              <input className="input ltr text-xs" type="password" value={x.credential ?? ''} placeholder="••••" disabled={disabled} onChange={(e) => updateTurn(i, { credential: e.target.value })} />
              <button
                type="button"
                className="btn btn-ghost"
                disabled={disabled}
                onClick={() =>
                  setDraft({
                    ...draft,
                    turn_servers: turn.filter((_, j) => j !== i),
                  })
                }
              >
                <Trash2 size={14} />
              </button>
            </div>
          ))}
          <button type="button" className="btn btn-sm self-start" disabled={disabled || turn.length >= 8} onClick={() => setDraft({ ...draft, turn_servers: [...turn, { url: '' }] })}>
            <Plus size={14} /> {t('settings.addTurn')}
          </button>
        </div>
        <div className="mt-3 border-t border-white/[0.05]">
          <Row title={t('settings.allowRelay')}>
            <Switch checked={draft.allow_relay} disabled={disabled} onChange={(v) => setDraft({ ...draft, allow_relay: v })} />
          </Row>
        </div>
      </Card>

      <AdvancedNetwork draft={draft} setDraft={setDraft} disabled={disabled} />

      <div className="flex items-center gap-3">
        <button type="button" className="btn btn-primary" disabled={disabled} onClick={submit}>
          <Save size={16} /> {saving ? t('common.saving') : t('common.save')}
        </button>
        {message ? <span className={`text-xs ${message.ok ? 'text-status-running' : 'text-status-error'}`}>{message.ok ? t('settings.saved') : message.text}</span> : null}
      </div>
    </div>
  )
}

function OverlayTab() {
  const t = useT()
  const prefs = usePrefs()
  const [hotkey, setHotkey] = useState<{ keys: string; error: string | null }>({
    keys: 'Ctrl+Alt+L',
    error: null,
  })
  useEffect(() => {
    let off: (() => void) | undefined
    void overlayStatus()
      .then((s) => setHotkey({ keys: s.keys, error: s.registered ? null : s.error }))
      .catch(() => undefined)
    if (isTauri()) void onHotkeyError((e) => setHotkey((h) => ({ ...h, error: e }))).then((u) => (off = u))
    return () => off?.()
  }, [])

  return (
    <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
      <Card>
        <SectionTitle icon={<MonitorUp size={16} />}>{t('settings.tab.overlay')}</SectionTitle>
        <div className="divide-y divide-white/[0.05]">
          <Row title={t('settings.overlayHotkey')} hint={hotkey.error ? t('settings.hotkeyTaken', { keys: hotkey.keys }) : undefined}>
            <kbd className="ltr rounded-lg border border-white/10 bg-white/[0.05] px-2 py-1 font-mono text-xs text-slate-200">{hotkey.keys}</kbd>
          </Row>
          <Row title={t('settings.overlayCorner')}>
            <select className="input w-40" value={prefs.overlayCorner} onChange={(e) => prefs.set({ overlayCorner: e.target.value as Corner })}>
              {(['top-right', 'top-left', 'bottom-right', 'bottom-left'] as Corner[]).map((c) => (
                <option key={c} value={c}>
                  {t(`settings.corner.${c}` as never)}
                </option>
              ))}
            </select>
          </Row>
          <Row title={t('settings.overlayOpacity')}>
            <input
              type="range"
              min={50}
              max={100}
              value={Math.round(prefs.overlayOpacity * 100)}
              onChange={(e) => prefs.set({ overlayOpacity: Number(e.target.value) / 100 })}
              className="w-40 accent-[#7c5cff]"
            />
          </Row>
          <Row title={t('settings.overlayScale')}>
            <input
              type="range"
              min={80}
              max={130}
              value={Math.round(prefs.overlayScale * 100)}
              onChange={(e) => prefs.set({ overlayScale: Number(e.target.value) / 100 })}
              className="w-40 accent-[#7c5cff]"
            />
          </Row>
        </div>
        <button type="button" className="btn mt-4" onClick={() => void showOverlay('panel')}>
          <MonitorUp size={16} /> {t('settings.overlayTest')}
        </button>
      </Card>
      <Card>
        <SectionTitle icon={<Mic size={16} />}>{t('voice.title')}</SectionTitle>
        <div className="divide-y divide-white/[0.05]">
          <Row title={t('voice.ptt')} hint={t('voice.pttDesc')}>
            <Switch
              checked={prefs.voicePtt}
              onChange={(on) => {
                prefs.set({ voicePtt: on })
                applyVoicePrefs()
              }}
            />
          </Row>
          {prefs.voicePtt ? (
            <Row title={t('voice.pttKey')}>
              <select
                className="input w-40"
                value={prefs.voicePttKey}
                onChange={(e) => {
                  prefs.set({ voicePttKey: Number(e.target.value) })
                  applyVoicePrefs()
                }}
              >
                {PTT_KEYS.map(([vk, label]) => (
                  <option key={vk} value={vk}>
                    {label}
                  </option>
                ))}
              </select>
            </Row>
          ) : null}
        </div>
        <p className="mt-3 text-xs text-slate-500">{t('voice.note')}</p>
      </Card>
      <Card>
        <SectionTitle icon={<Info size={16} />}>{t('settings.advanced')}</SectionTitle>
        <p className="text-sm text-slate-400">{t('settings.overlayNote')}</p>
      </Card>
    </div>
  )
}

/** Push-to-talk keys offered, as Windows virtual-key codes. */
const PTT_KEYS: [number, string][] = [
  [0x05, 'Mouse 4'],
  [0x06, 'Mouse 5'],
  [0x04, 'Mouse wheel click'],
  [0x56, 'V'],
  [0x42, 'B'],
  [0x54, 'T'],
  [0xc0, '` (~)'],
  [0x14, 'Caps Lock'],
  [0xa5, 'Right Alt'],
  [0xa3, 'Right Ctrl'],
]

function About() {
  const t = useT()
  const status = useDaemonStore((s) => s.status)
  const [stateDir, setStateDir] = useState<string | null>(null)
  useEffect(() => {
    void daemonStateDir()
      .then(setStateDir)
      .catch(() => setStateDir(null))
  }, [])
  return (
    <div className="space-y-4">
      <Updates />
      <Card>
        <SectionTitle icon={<Info size={16} />}>{t('app.name')}</SectionTitle>
        <div className="grid grid-cols-1 gap-3 text-sm sm:grid-cols-2">
          <div>
            <div className="label">{t('home.version')}</div>
            <div className="ltr font-mono text-xs text-slate-300">{status?.version ?? '—'}</div>
          </div>
          <div>
            <div className="label">{t('settings.stateDir')}</div>
            <div className="ltr break-all font-mono text-xs text-slate-300">{stateDir ?? status?.state_dir ?? '—'}</div>
          </div>
        </div>
        <div className="mt-4">
          <Banner tone="info">{t('settings.overlayNote')}</Banner>
        </div>
      </Card>
    </div>
  )
}
