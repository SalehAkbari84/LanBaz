/**
 * Audio devices and the microphone pipeline shared by voice chat and the
 * settings page.
 *
 *   device (getUserMedia, with echo / noise / gain processing as chosen)
 *     → GainNode (input volume, 0–200%)
 *     → AnalyserNode (level meter, voice activation)
 *     → MediaStreamDestination (the track WebRTC sends)
 *
 * The sent track belongs to the destination, so switching the device or the
 * processing swaps only the source behind it: a call in progress keeps its
 * track and nobody has to reconnect.
 */

import type { Prefs } from '../stores/prefs'

export interface AudioDevice {
  id: string
  label: string
}

export async function listDevices(): Promise<{ inputs: AudioDevice[]; outputs: AudioDevice[]; named: boolean }> {
  const all = await navigator.mediaDevices.enumerateDevices()
  const pick = (kind: MediaDeviceKind) =>
    all
      .filter((d) => d.kind === kind && d.deviceId && d.deviceId !== 'default' && d.deviceId !== 'communications')
      .map((d, i) => ({ id: d.deviceId, label: d.label || `${kind === 'audioinput' ? 'Mic' : 'Speaker'} ${i + 1}` }))
  return { inputs: pick('audioinput'), outputs: pick('audiooutput'), named: all.some((d) => d.label) }
}

export function micConstraints(p: Pick<Prefs, 'micDeviceId' | 'echoCancel' | 'noiseSuppress' | 'autoGain'>): MediaTrackConstraints {
  return {
    deviceId: p.micDeviceId ? { exact: p.micDeviceId } : undefined,
    echoCancellation: p.echoCancel,
    noiseSuppression: p.noiseSuppress,
    autoGainControl: p.autoGain,
    channelCount: 1,
  }
}

/** Plays an element's audio on the chosen output device ('' = system default). */
export async function routeTo(el: HTMLMediaElement, deviceId: string): Promise<void> {
  const any = el as HTMLMediaElement & { setSinkId?: (id: string) => Promise<void>; sinkId?: string }
  if (!any.setSinkId || any.sinkId === deviceId) return
  try {
    await any.setSinkId(deviceId)
  } catch {
    // A device that vanished: stay on the current one.
  }
}

export class MicPipeline {
  readonly ctx: AudioContext
  readonly output: MediaStream
  private gain: GainNode
  private analyser: AnalyserNode
  private buf: Float32Array<ArrayBuffer>
  private raw: MediaStream | null = null
  private src: MediaStreamAudioSourceNode | null = null
  private key = ''

  private constructor() {
    this.ctx = new AudioContext()
    this.gain = this.ctx.createGain()
    this.analyser = this.ctx.createAnalyser()
    this.analyser.fftSize = 512
    this.buf = new Float32Array(this.analyser.fftSize)
    const dest = this.ctx.createMediaStreamDestination()
    this.gain.connect(this.analyser)
    this.gain.connect(dest)
    this.output = dest.stream
  }

  static async open(p: Prefs): Promise<MicPipeline> {
    const m = new MicPipeline()
    try {
      await m.apply(p)
    } catch (e) {
      m.close()
      throw e
    }
    return m
  }

  /** Applies the prefs; re-opens the device only when the device or processing changed. */
  async apply(p: Prefs): Promise<void> {
    this.gain.gain.value = Math.max(0, Math.min(2, p.micGain))
    const c = micConstraints(p)
    const key = JSON.stringify(c)
    if (key === this.key && this.raw) return
    let raw: MediaStream
    try {
      raw = await navigator.mediaDevices.getUserMedia({ audio: c })
    } catch (e) {
      // The chosen device is gone: fall back to the default one.
      if (!p.micDeviceId) throw e
      raw = await navigator.mediaDevices.getUserMedia({ audio: { ...c, deviceId: undefined } })
    }
    this.src?.disconnect()
    this.raw?.getTracks().forEach((t) => t.stop())
    this.raw = raw
    this.src = this.ctx.createMediaStreamSource(raw)
    this.src.connect(this.gain)
    this.key = key
    if (this.ctx.state === 'suspended') void this.ctx.resume()
  }

  /** RMS level, 0..1, after the input volume. Not affected by mute. */
  level(): number {
    this.analyser.getFloatTimeDomainData(this.buf)
    let sum = 0
    for (const v of this.buf) sum += v * v
    return Math.sqrt(sum / this.buf.length)
  }

  close(): void {
    this.src?.disconnect()
    this.raw?.getTracks().forEach((t) => t.stop())
    this.output.getTracks().forEach((t) => t.stop())
    void this.ctx.close().catch(() => undefined)
  }
}

/** Maps a 0..1 RMS to a 0..1 meter position (log scale, -60 dB .. 0 dB). */
export function meterPos(rms: number): number {
  if (rms <= 0) return 0
  const db = 20 * Math.log10(rms)
  return Math.max(0, Math.min(1, (db + 60) / 60))
}

/** Inverse of meterPos: the RMS a meter position stands for. */
export function rmsAt(pos: number): number {
  return Math.pow(10, (pos * 60 - 60) / 20)
}

/** A short two-note chime on the chosen output, to test the speakers. */
export async function testSpeaker(deviceId: string, volume: number): Promise<void> {
  const ctx = new AudioContext()
  const dest = ctx.createMediaStreamDestination()
  const g = ctx.createGain()
  g.connect(dest)
  const el = new Audio()
  el.srcObject = dest.stream
  el.volume = Math.max(0, Math.min(1, volume))
  await routeTo(el, deviceId)
  await el.play().catch(() => undefined)
  const t0 = ctx.currentTime + 0.05
  for (const [i, f] of [660, 880].entries()) {
    const o = ctx.createOscillator()
    o.frequency.value = f
    o.connect(g)
    o.start(t0 + i * 0.25)
    o.stop(t0 + i * 0.25 + 0.22)
  }
  g.gain.setValueAtTime(0.0001, t0)
  g.gain.exponentialRampToValueAtTime(0.4, t0 + 0.03)
  g.gain.setValueAtTime(0.4, t0 + 0.45)
  g.gain.exponentialRampToValueAtTime(0.0001, t0 + 0.5)
  window.setTimeout(() => {
    el.srcObject = null
    void ctx.close().catch(() => undefined)
  }, 900)
}
