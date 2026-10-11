import { Ear, Headphones, Keyboard, Mic, Play, RefreshCw, Square } from 'lucide-react'
import { useCallback, useEffect, useRef, useState } from 'react'

import { Card, Row, SectionTitle, Switch } from '../../components/ui'
import { useT } from '../../i18n'
import { listDevices, meterPos, MicPipeline, routeTo, testSpeaker, type AudioDevice } from '../../services/audio'
import { usePrefs } from '../../stores/prefs'
import { applyVoicePrefs, useVoice } from '../../stores/voice'

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

/** Settings → Sound: devices, volumes, processing, voice activation, tests. */
export function AudioSettings() {
  const t = useT()
  const prefs = usePrefs()
  const inVoice = useVoice((s) => Boolean(s.roomId))
  const [devices, setDevices] = useState<{ inputs: AudioDevice[]; outputs: AudioDevice[]; named: boolean }>({ inputs: [], outputs: [], named: true })
  const [testing, setTesting] = useState(false)
  const [hearSelf, setHearSelf] = useState(false)
  const [level, setLevel] = useState(0)
  const [error, setError] = useState<string | null>(null)
  const pipe = useRef<MicPipeline | null>(null)
  const echo = useRef<HTMLAudioElement | null>(null)

  const refresh = useCallback(() => {
    void listDevices()
      .then(setDevices)
      .catch(() => undefined)
  }, [])

  useEffect(() => {
    refresh()
    navigator.mediaDevices?.addEventListener?.('devicechange', refresh)
    return () => navigator.mediaDevices?.removeEventListener?.('devicechange', refresh)
  }, [refresh])

  const stopTest = useCallback(() => {
    if (echo.current) echo.current.srcObject = null
    echo.current = null
    pipe.current?.close()
    pipe.current = null
    setTesting(false)
    setHearSelf(false)
    setLevel(0)
  }, [])

  const startTest = async () => {
    setError(null)
    try {
      pipe.current = await MicPipeline.open(usePrefs.getState())
      setTesting(true)
      refresh() // labels become visible once access is granted
    } catch (e) {
      setError(String(e))
    }
  }

  // Close the test when leaving the page.
  useEffect(() => stopTest, [stopTest])

  // The meter.
  useEffect(() => {
    if (!testing) return
    const id = window.setInterval(() => {
      if (pipe.current) setLevel(meterPos(pipe.current.level()))
    }, 50)
    return () => window.clearInterval(id)
  }, [testing])

  // Settings apply to the test as they change.
  useEffect(() => {
    void pipe.current?.apply(prefs).catch((e) => setError(String(e)))
  }, [prefs])

  // Hear yourself: the processed microphone on the chosen output.
  useEffect(() => {
    if (!testing || !hearSelf || !pipe.current) {
      if (echo.current) echo.current.srcObject = null
      echo.current = null
      return
    }
    const el = echo.current ?? new Audio()
    echo.current = el
    el.srcObject = pipe.current.output
    el.volume = Math.max(0, Math.min(1, prefs.outputVolume))
    void routeTo(el, prefs.speakerDeviceId).then(() => el.play().catch(() => undefined))
  }, [testing, hearSelf, prefs.speakerDeviceId, prefs.outputVolume])

  const pct = (v: number) => `${Math.round(v * 100)}%`

  return (
    <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
      <Card>
        <SectionTitle
          icon={<Mic size={16} />}
          extra={
            <button type="button" className="btn btn-ghost btn-sm" onClick={refresh} title={t('audio.refresh')} aria-label={t('audio.refresh')}>
              <RefreshCw size={14} />
            </button>
          }
        >
          {t('audio.input')}
        </SectionTitle>
        <div className="divide-y divide-white/[0.05]">
          <Row title={t('audio.device')} hint={!devices.named ? t('audio.namesHint') : undefined}>
            <select className="input w-56" value={prefs.micDeviceId} onChange={(e) => prefs.set({ micDeviceId: e.target.value })}>
              <option value="">{t('audio.default')}</option>
              {devices.inputs.map((d) => (
                <option key={d.id} value={d.id}>
                  {d.label}
                </option>
              ))}
            </select>
          </Row>
          <Row title={t('audio.inputVolume')}>
            <div className="flex items-center gap-2">
              <input type="range" min={0} max={200} step={5} value={Math.round(prefs.micGain * 100)} onChange={(e) => prefs.set({ micGain: Number(e.target.value) / 100 })} className="w-40 accent-[#7c5cff]" />
              <span className="ltr w-11 text-end font-mono text-xs text-slate-400">{pct(prefs.micGain)}</span>
            </div>
          </Row>
          <div className="py-3">
            <div className="flex items-center justify-between gap-3">
              <div className="text-sm text-slate-100">{t('audio.test')}</div>
              <div className="flex items-center gap-2">
                {testing ? (
                  <>
                    <label className="flex items-center gap-1 text-xs text-slate-400" title={t('audio.hearSelfHint')}>
                      <input type="checkbox" checked={hearSelf} onChange={(e) => setHearSelf(e.target.checked)} />
                      <Ear size={13} /> {t('audio.hearSelf')}
                    </label>
                    <button type="button" className="btn btn-sm" onClick={stopTest}>
                      <Square size={13} /> {t('audio.stopTest')}
                    </button>
                  </>
                ) : (
                  <button type="button" className="btn btn-sm" onClick={() => void startTest()}>
                    <Play size={13} /> {t('audio.startTest')}
                  </button>
                )}
              </div>
            </div>
            <Meter level={testing ? level : 0} threshold={prefs.voiceActivation ? prefs.voiceThreshold : null} />
            <p className="mt-1 text-[11px] text-slate-500">{testing ? t('audio.testHint') : inVoice ? t('audio.inVoiceHint') : t('audio.testIdle')}</p>
            {error ? <p className="mt-1 text-xs text-red-300">{t('voice.noMic')}: {error}</p> : null}
          </div>
          <Row title={t('audio.voiceActivation')} hint={t('audio.voiceActivationHint')}>
            <Switch checked={prefs.voiceActivation} onChange={(on) => prefs.set({ voiceActivation: on })} disabled={prefs.voicePtt} />
          </Row>
          {prefs.voiceActivation && !prefs.voicePtt ? (
            <Row title={t('audio.threshold')} hint={t('audio.thresholdHint')}>
              <input type="range" min={10} max={90} value={Math.round(prefs.voiceThreshold * 100)} onChange={(e) => prefs.set({ voiceThreshold: Number(e.target.value) / 100 })} className="w-40 accent-[#7c5cff]" />
            </Row>
          ) : null}
          <Row title={t('audio.echo')} hint={t('audio.echoHint')}>
            <Switch checked={prefs.echoCancel} onChange={(on) => prefs.set({ echoCancel: on })} />
          </Row>
          <Row title={t('audio.noise')} hint={t('audio.noiseHint')}>
            <Switch checked={prefs.noiseSuppress} onChange={(on) => prefs.set({ noiseSuppress: on })} />
          </Row>
          <Row title={t('audio.agc')} hint={t('audio.agcHint')}>
            <Switch checked={prefs.autoGain} onChange={(on) => prefs.set({ autoGain: on })} />
          </Row>
        </div>
      </Card>

      <div className="flex flex-col gap-4">
        <Card>
          <SectionTitle icon={<Headphones size={16} />}>{t('audio.output')}</SectionTitle>
          <div className="divide-y divide-white/[0.05]">
            <Row title={t('audio.device')}>
              <select className="input w-56" value={prefs.speakerDeviceId} onChange={(e) => prefs.set({ speakerDeviceId: e.target.value })}>
                <option value="">{t('audio.default')}</option>
                {devices.outputs.map((d) => (
                  <option key={d.id} value={d.id}>
                    {d.label}
                  </option>
                ))}
              </select>
            </Row>
            <Row title={t('audio.outputVolume')} hint={t('audio.outputVolumeHint')}>
              <div className="flex items-center gap-2">
                <input type="range" min={0} max={100} step={5} value={Math.round(prefs.outputVolume * 100)} onChange={(e) => prefs.set({ outputVolume: Number(e.target.value) / 100 })} className="w-40 accent-[#7c5cff]" />
                <span className="ltr w-11 text-end font-mono text-xs text-slate-400">{pct(prefs.outputVolume)}</span>
              </div>
            </Row>
            <Row title={t('audio.testSpeaker')}>
              <button type="button" className="btn btn-sm" onClick={() => void testSpeaker(prefs.speakerDeviceId, prefs.outputVolume)}>
                <Play size={13} /> {t('audio.play')}
              </button>
            </Row>
          </div>
        </Card>

        <Card>
          <SectionTitle icon={<Keyboard size={16} />}>{t('voice.ptt')}</SectionTitle>
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
      </div>
    </div>
  )
}

/** A level meter (-60..0 dB) with the voice-activation threshold marked. */
function Meter({ level, threshold }: { level: number; threshold: number | null }) {
  const over = threshold != null && level >= threshold
  return (
    <div className="ltr relative mt-2 h-2.5 overflow-hidden rounded-full bg-white/[0.06]" role="meter" aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(level * 100)}>
      <div
        className={`h-full rounded-full transition-[width] duration-75 ${threshold == null || over ? 'bg-emerald-400' : 'bg-slate-500'}`}
        style={{ width: `${Math.round(level * 100)}%` }}
      />
      {threshold != null ? <div className="absolute inset-y-0 w-0.5 bg-amber-300" style={{ left: `${Math.round(threshold * 100)}%` }} /> : null}
    </div>
  )
}
