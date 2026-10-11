/**
 * Screen share. The sharer (main window) captures the screen and sends it to
 * each player who presses Watch, over WebRTC on the room's own addresses (the
 * LanBaz tunnel) - nothing is sent to anybody who is not watching. Each viewer
 * watches in its own window (ScreenViewer), which runs the receiving side.
 *
 * Quality is decided per viewer, every second, by services/screenQuality.ts
 * from the live stats, within the caps the sharer picked, and applied with
 * RTCRtpSender.setParameters (no renegotiation, no glitch). On top of the
 * browser's own congestion control it adds what only the app can know:
 *  - the codec both machines handle best, preferring what the GPU encodes;
 *  - the size the viewer actually shows the picture at (no pixels the
 *    viewer's screen would throw away), and a pause while it is minimized;
 *  - what is being shared (Auto): a game or video, or text and desktop.
 *
 * Signalling (kind "screen"): share/unshare (announced to the room, repeated
 * every 10 s for late joiners), watch (with the viewer's decoders) / view
 * (its picture size, or hidden) / unwatch, offer/answer.
 */

import { create } from 'zustand'

import { translate } from '../i18n'
import { BROADCAST_FROM, Chain, encoderKbps, nextFlow, shouldSend, type Frame, type ViewerFlow } from '../services/broadcast'
import { BroadcastEncoder, broadcastSupported, sendFrame } from '../services/broadcastMedia'
import { gathered, tunnelOnly } from '../services/rtc'
import {
  budgetKbps,
  classifyContent,
  codecFactor,
  codecOrder,
  CODECS,
  decide,
  effectiveCaps,
  HEIGHTS,
  initialState,
  scaleFor,
  startKbps,
  withBitrateHints,
  type AutoContent,
  type Caps,
  type Codec,
  type CodecCaps,
  type Content,
  type QState,
  viewRung,
} from '../services/screenQuality'
import { daemonClient, useDaemonStore } from './daemon'
import { useGamesStore } from './games'
import { usePrefs } from './prefs'
import { toast } from './toasts'

export type ScreenSignal =
  | { t: 'share'; name?: string; sound?: boolean }
  | { t: 'unshare' }
  | { t: 'watch'; dec?: CodecCaps }
  | { t: 'view'; h: number; hidden?: boolean }
  | { t: 'unwatch' }
  | { t: 'offer' | 'answer'; sdp: string }
  // Broadcast mode: the codec frames come in on the "bc" channel, or null
  // when it ended; a viewer asks for a keyframe, or says it cannot decode.
  | { t: 'bc'; codec: Codec | null }
  | { t: 'bc-key' }
  | { t: 'bc-fail' }

export interface ViewerInfo {
  name: string
  height: number
  fps: number
  kbps: number
  rttMs: number | null
  reason: string
  codec?: string
  paused?: boolean
  /** Fed by the shared encoder (broadcast mode), and how many layers it gets. */
  broadcast?: boolean
  layers?: number
}

export interface ShareSettings {
  /** HEIGHTS index, or -1 for Auto (the controller's own choice up to 1080p). */
  rung: number
  /** Frame cap; 0 = Auto (up to 60). */
  fps: number
  content: 'auto' | Content
  sound: boolean
  dataSaver: boolean
}

interface ScreenState {
  sharing: { roomId: string; startedAt: number } | null
  settings: ShareSettings
  viewers: Record<string, ViewerInfo>
  sentBytes: number
  /** What Auto decided is being shared. */
  detected: Content
  /** Broadcast mode: the shared encoder's codec, or null. */
  broadcast: Codec | null
  /** Others sharing, by room: the sharer's peer id and name. */
  shares: Record<string, { peerId: string; name: string; at: number }>
  start: (roomId: string) => Promise<void>
  stop: () => void
  setSettings: (patch: Partial<ShareSettings>) => void
  onSignal: (roomId: string, from: string, data: ScreenSignal) => void
}

const tr = (k: Parameters<typeof translate>[1], v?: Record<string, string | number>) => translate(usePrefs.getState().language, k, v)

export async function sendScreen(roomId: string, to: string, data: ScreenSignal): Promise<void> {
  await daemonClient()?.request('voice.signal', { room_id: roomId, to, data, kind: 'screen' })
}

// ------------------------------------------------------------------ codecs --

const MIME: Record<Codec, string> = { AV1: 'video/AV1', H265: 'video/H265', VP9: 'video/VP9', H264: 'video/H264', VP8: 'video/VP8' }

export function codecOf(mime: string | undefined): Codec | null {
  const c = CODECS.find((k) => MIME[k].toLowerCase() === mime?.toLowerCase())
  return c ?? null
}

/**
 * What this machine encodes ('enc') or decodes ('dec') in real time, and which
 * of it the GPU does. Asked once, for a 1080p30 picture; null when the browser
 * cannot say.
 */
const capsCache: Partial<Record<'enc' | 'dec', Promise<CodecCaps | null>>> = {}
export function codecCaps(dir: 'enc' | 'dec'): Promise<CodecCaps | null> {
  capsCache[dir] ??= (async () => {
    const mc = navigator.mediaCapabilities
    if (!mc) return null
    const out: CodecCaps = {}
    for (const c of CODECS) {
      const cfg = { type: 'webrtc', video: { contentType: MIME[c], width: 1920, height: 1080, bitrate: 4_000_000, framerate: 30 } } as never
      try {
        const r = dir === 'enc' ? await mc.encodingInfo(cfg) : await mc.decodingInfo(cfg)
        out[c] = { supported: r.supported, smooth: r.smooth, powerEfficient: r.powerEfficient }
      } catch {
        return null // the browser does not do "webrtc" queries at all
      }
    }
    return out
  })()
  return capsCache[dir]!
}

/** Puts the codecs in this order; drops the ones we never use. */
export function preferCodecs(t: RTCRtpTransceiver, order: Codec[]): void {
  const caps = RTCRtpSender.getCapabilities?.('video')
  if (!caps || !t.setCodecPreferences) return
  const rank = (mime: string) => {
    const i = order.indexOf(codecOf(mime) as Codec)
    return i < 0 ? order.length : i
  }
  // Only what we use (plus retransmission and FEC): a shorter offer stays well
  // inside the room's 16 KB signalling frame.
  const extras = new Set(['video/rtx', 'video/red', 'video/ulpfec'])
  const sorted = [...caps.codecs]
    .filter((c) => order.includes(codecOf(c.mimeType) as Codec) || extras.has(c.mimeType))
    .sort((a, b) => rank(a.mimeType) - rank(b.mimeType))
  if (!sorted.some((c) => rank(c.mimeType) < order.length)) return
  try {
    t.setCodecPreferences(sorted)
  } catch {
    // Keep the browser's order.
  }
}

// ---------------------------------------------------------------- sharer --

interface Viewer {
  pc: RTCPeerConnection
  sender: RTCRtpSender
  q: QState
  lastBytes: number
  lastAt: number
  applied: string
  /** Starting bitrate hint for the browser's estimator, kbps. */
  start: number
  /** Device pixels the viewer shows the picture at, and whether it is minimized. */
  viewH: number | null
  hidden: boolean
  codec: Codec | null
  /** The viewer's decoders, from its watch message. */
  dec: CodecCaps | null
  /** Broadcast mode: its channel, its layers, what it was sent. */
  dc: RTCDataChannel
  bcOn: boolean
  bcFailed: boolean
  flow: ViewerFlow
  sent: Chain
  bcBytes: number
  bcFrames: number
  bcLast: { bytes: number; frames: number; at: number }
}

let stream: MediaStream | null = null
const viewers = new Map<string, Viewer>()
/** A view report that arrived before its viewer's connection. */
const pendingView = new Map<string, { h: number; hidden: boolean }>()
let tick: number | null = null
let announce: number | null = null
let auto: AutoContent = { kind: 'motion', streak: 0 }
let bcast: { enc: BroadcastEncoder; codec: Codec; height: number } | null = null

function myName(): string {
  return useDaemonStore.getState().settings?.display_name || ''
}

function contentOf(s: ShareSettings): Content {
  return s.content === 'auto' ? auto.kind : s.content
}

function capsOf(s: ShareSettings): Caps {
  const game = useGamesStore.getState().presence['']
  return {
    maxRung: s.rung < 0 ? HEIGHTS.length - 1 : s.rung,
    maxFps: s.fps <= 0 ? 60 : s.fps,
    dataSaver: s.dataSaver,
    content: contentOf(s),
    gameRunning: Boolean(game?.game_id),
  }
}

function closeViewer(id: string): void {
  const v = viewers.get(id)
  pendingView.delete(id)
  if (!v) return
  v.pc.close()
  viewers.delete(id)
  useScreen.setState((s) => {
    const next = { ...s.viewers }
    delete next[id]
    return { viewers: next }
  })
}

async function addViewer(roomId: string, id: string, dec: CodecCaps | null): Promise<void> {
  if (!stream) return
  closeViewer(id)
  const video = stream.getVideoTracks()[0]
  if (!video) return
  const pc = new RTCPeerConnection({ iceServers: [] })
  // Broadcast mode's channel: unordered and unreliable, like the video it
  // carries - a late frame is worth less than the next one.
  const dc = pc.createDataChannel('bc', { ordered: false, maxRetransmits: 0 })
  dc.binaryType = 'arraybuffer'
  const tv = pc.addTransceiver(video, { direction: 'sendonly', streams: [stream] })
  const caps = capsOf(useScreen.getState().settings)
  const { maxRung, maxFps } = effectiveCaps(caps)
  // Settings can change mid-share; the codec is picked for the heaviest
  // picture they allow, and Auto starts out assuming a game.
  const heavy = caps.content === 'motion' && HEIGHTS[maxRung]! >= 720 && maxFps >= 30
  const order = codecOrder(await codecCaps('enc'), dec, caps.content, heavy)
  preferCodecs(tv, order)
  for (const a of stream.getAudioTracks()) pc.addTransceiver(a, { direction: 'sendonly', streams: [stream] })
  const now = Date.now()
  const view = pendingView.get(id)
  pendingView.delete(id)
  const start = startKbps({ ...caps, codec: order[0], viewHeight: view?.h })
  viewers.set(id, {
    pc,
    sender: tv.sender,
    q: initialState(caps, now),
    lastBytes: 0,
    lastAt: now,
    applied: '',
    start,
    viewH: view?.h ?? null,
    hidden: view?.hidden ?? false,
    codec: null,
    dec,
    dc,
    bcOn: false,
    bcFailed: false,
    flow: { maxLayer: 2, calmSince: now },
    sent: new Chain(),
    bcBytes: 0,
    bcFrames: 0,
    bcLast: { bytes: 0, frames: 0, at: now },
  })
  pc.onconnectionstatechange = () => {
    if (pc.connectionState === 'failed' || pc.connectionState === 'closed') closeViewer(id)
  }
  useScreen.setState((s) => ({ viewers: { ...s.viewers, [id]: { name: id.slice(0, 8), height: 0, fps: 0, kbps: 0, rttMs: null, reason: '' } } }))
  await pc.setLocalDescription(await pc.createOffer())
  await gathered(pc)
  await sendScreen(roomId, id, { t: 'offer', sdp: withBitrateHints(tunnelOnly(pc.localDescription!.sdp), start) })
}

interface Parsed {
  rttMs: number | null
  loss: number
  bytes: number
  height: number
  fps: number
  /** Frames the screen capturer delivered per second: how much is changing. */
  capturedFps: number | null
  codec: Codec | null
}

async function readStats(pc: RTCPeerConnection): Promise<Parsed> {
  const out: Parsed = { rttMs: null, loss: 0, bytes: 0, height: 0, fps: 0, capturedFps: null, codec: null }
  const report = await pc.getStats()
  report.forEach((r: any) => {
    if (r.type === 'candidate-pair' && r.state === 'succeeded' && (r.nominated || r.selected)) {
      if (typeof r.currentRoundTripTime === 'number') out.rttMs = r.currentRoundTripTime * 1000
    } else if (r.type === 'outbound-rtp' && r.kind === 'video') {
      out.bytes += r.bytesSent ?? 0
      out.height = r.frameHeight ?? out.height
      out.fps = r.framesPerSecond ?? out.fps
      if (r.codecId) out.codec = codecOf(report.get(r.codecId)?.mimeType)
    } else if (r.type === 'remote-inbound-rtp' && r.kind === 'video') {
      if (typeof r.fractionLost === 'number') out.loss = r.fractionLost
      if (out.rttMs == null && typeof r.roundTripTime === 'number') out.rttMs = r.roundTripTime * 1000
    } else if (r.type === 'media-source' && r.kind === 'video' && typeof r.framesPerSecond === 'number') {
      out.capturedFps = r.framesPerSecond
    }
  })
  return out
}

// ------------------------------------------------------- broadcast mode --

/** Codecs every viewer in broadcast mode can play in real time. */
function sharedDecoders(list: Viewer[]): CodecCaps | null {
  const known = list.map((v) => v.dec).filter((d): d is CodecCaps => Boolean(d))
  if (!known.length) return null
  const out: CodecCaps = {}
  for (const c of CODECS) {
    const all = known.every((d) => d[c]?.supported && d[c]?.smooth)
    out[c] = { supported: all, smooth: all, powerEfficient: known.every((d) => d[c]?.powerEfficient) }
  }
  return out
}

/** The shared encoder's picture: the largest any viewer shows, within the caps. */
function broadcastTarget(list: Viewer[]): { width: number; height: number; fps: number; kbps: number } {
  const caps = capsOf(useScreen.getState().settings)
  const eff = effectiveCaps(caps)
  const shown = list.filter((v) => !v.hidden)
  const viewMax = shown.length && shown.every((v) => v.viewH) ? Math.max(...shown.map((v) => v.viewH!)) : null
  const rung = Math.min(eff.maxRung, viewRung(viewMax))
  const src = stream?.getVideoTracks()[0]?.getSettings()
  const srcH = src?.height ?? 1080
  const srcW = src?.width ?? 1920
  const height = Math.round(Math.min(srcH, HEIGHTS[rung]!) / 2) * 2
  const width = Math.round((height * srcW) / srcH / 2) * 2
  const fps = caps.content === 'detail' ? Math.min(eff.maxFps, 30) : eff.maxFps
  let kbps = budgetKbps(rung, fps, caps.dataSaver) * codecFactor(bcast?.codec)
  if (caps.gameRunning) kbps *= 0.75
  return { width, height, fps, kbps: encoderKbps(Math.round(kbps), list.map((v) => v.flow)) }
}

/** Hands one encoded frame to every viewer in broadcast mode that can take it. */
function fanOut(f: Frame): void {
  for (const v of viewers.values()) {
    if (!v.bcOn || v.hidden || v.dc.readyState !== 'open') continue
    if (shouldSend(f, v.flow, v.dc.bufferedAmount, v.sent)) {
      try {
        sendFrame(v.dc, f)
      } catch {
        v.sent.miss(f)
        continue
      }
      v.sent.take(f)
      v.bcBytes += f.data.length
      v.bcFrames++
    } else {
      v.sent.miss(f)
      if (v.sent.broken) bcast?.enc.requestKey()
    }
  }
}

function stopBroadcast(): void {
  if (!bcast) return
  bcast.enc.close()
  bcast = null
  const roomId = useScreen.getState().sharing?.roomId
  for (const [id, v] of viewers) {
    if (!v.bcOn) continue
    v.bcOn = false
    v.applied = '' // its own WebRTC video comes back on
    if (roomId) void sendScreen(roomId, id, { t: 'bc', codec: null }).catch(() => undefined)
  }
  useScreen.setState({ broadcast: null })
}

/**
 * From BROADCAST_FROM viewers on, one encode for all of them; back to one
 * connection each below two (the gap keeps it from flapping).
 */
async function manageBroadcast(): Promise<void> {
  const roomId = useScreen.getState().sharing?.roomId
  if (!roomId || !stream || !broadcastSupported()) return
  const live = [...viewers.values()].filter((v) => v.pc.connectionState === 'connected' && v.dc.readyState === 'open' && !v.bcFailed)
  if (!bcast && live.length >= BROADCAST_FROM) {
    const caps = capsOf(useScreen.getState().settings)
    const t = broadcastTarget(live)
    const order = codecOrder(await codecCaps('enc'), sharedDecoders(live), caps.content, t.height >= 720 && t.fps >= 30)
    const enc = new BroadcastEncoder(fanOut, () => stopBroadcast())
    try {
      const codec = await enc.start(stream.getVideoTracks()[0]!, order, t)
      bcast = { enc, codec, height: t.height }
    } catch {
      enc.close()
      return
    }
    for (const [id, v] of viewers) {
      if (!live.includes(v)) continue
      v.bcOn = true
      v.sent = new Chain()
      v.flow = { maxLayer: 2, calmSince: Date.now() }
      v.applied = '' // its own WebRTC video goes quiet
      void sendScreen(roomId, id, { t: 'bc', codec: bcast.codec }).catch(() => undefined)
    }
    useScreen.setState({ broadcast: bcast.codec })
    return
  }
  if (bcast && live.length < 2) {
    stopBroadcast()
    return
  }
  if (bcast) {
    const now = Date.now()
    for (const [id, v] of viewers) {
      // Somebody who starts watching while broadcast mode is on joins it.
      if (!v.bcOn && live.includes(v)) {
        v.bcOn = true
        v.sent = new Chain()
        v.flow = { maxLayer: 2, calmSince: now }
        v.applied = ''
        void sendScreen(roomId, id, { t: 'bc', codec: bcast.codec }).catch(() => undefined)
      }
      if (v.bcOn) v.flow = nextFlow(v.flow, v.dc.bufferedAmount, now)
    }
    const t = broadcastTarget([...viewers.values()].filter((v) => v.bcOn))
    bcast.height = t.height
    bcast.enc.retarget(t)
  }
}

async function control(): Promise<void> {
  await manageBroadcast()
  const settings = useScreen.getState().settings
  const video = stream?.getVideoTracks()[0]
  const captured = video?.getSettings().height ?? 1080
  let total = 0
  let sampled = false
  for (const [id, v] of viewers) {
    if (v.pc.connectionState !== 'connected') continue
    let st: Parsed
    try {
      st = await readStats(v.pc)
    } catch {
      continue
    }
    // Auto content, from the first live viewer's capture rate (a paused
    // viewer encodes nothing and says nothing about the screen).
    if (!sampled && !v.hidden && st.capturedFps != null && settings.content === 'auto') {
      sampled = true
      const next = classifyContent(auto, st.capturedFps)
      if (next.kind !== auto.kind) {
        auto = next
        capture(settings)
        useScreen.setState({ detected: next.kind })
      } else auto = next
    }
    v.codec = st.codec ?? v.codec
    const caps: Caps = { ...capsOf(settings), viewHeight: v.viewH, codec: v.codec }
    const now = Date.now()
    const { state, decision } = decide(v.q, { now, rttMs: st.rttMs, loss: st.loss }, caps)
    v.q = state
    const key = `${decision.rung}|${decision.fps}|${decision.maxBitrateBps}|${decision.degradation}|${caps.gameRunning}|${v.hidden}|${v.bcOn}`
    if (key !== v.applied) {
      const p = v.sender.getParameters()
      if (p.encodings?.[0]) {
        // A minimized viewer gets nothing: no encoding, no bits. In broadcast
        // mode the shared encoder feeds it instead.
        p.encodings[0].active = !v.hidden && !v.bcOn
        p.encodings[0].maxBitrate = decision.maxBitrateBps
        p.encodings[0].maxFramerate = decision.fps
        p.encodings[0].scaleResolutionDownBy = scaleFor(captured, decision.rung)
        // Below the game's packets on a shared line while a game runs.
        const prio: RTCPriorityType = caps.gameRunning ? 'low' : 'medium'
        p.encodings[0].priority = prio
        p.encodings[0].networkPriority = prio
        ;(p as any).degradationPreference = decision.degradation
        try {
          await v.sender.setParameters(p)
          v.applied = key
        } catch {
          // Try again next second.
        }
      }
    }
    const dt = (now - v.lastAt) / 1000
    let kbps = dt > 0 && v.lastBytes ? Math.round(((st.bytes - v.lastBytes) * 8) / 1000 / dt) : 0
    v.lastBytes = st.bytes
    v.lastAt = now
    total += st.bytes + v.bcBytes
    let height = st.height || HEIGHTS[decision.rung]!
    let fps = Math.round(st.fps || decision.fps)
    if (v.bcOn && bcast) {
      const bdt = (now - v.bcLast.at) / 1000
      kbps = bdt > 0 ? Math.round(((v.bcBytes - v.bcLast.bytes) * 8) / 1000 / bdt) : 0
      fps = bdt > 0 ? Math.round((v.bcFrames - v.bcLast.frames) / bdt) : 0
      height = bcast.height
      v.bcLast = { bytes: v.bcBytes, frames: v.bcFrames, at: now }
    }
    useScreen.setState((s) => ({
      viewers: {
        ...s.viewers,
        [id]: {
          ...(s.viewers[id] ?? { name: id.slice(0, 8) }),
          height: v.hidden ? 0 : height,
          fps: v.hidden ? 0 : fps,
          kbps: v.hidden ? 0 : kbps,
          rttMs: st.rttMs,
          reason: decision.reason,
          codec: (v.bcOn ? bcast?.codec : v.codec) ?? undefined,
          paused: v.hidden,
          broadcast: v.bcOn,
          layers: v.bcOn ? v.flow.maxLayer + 1 : undefined,
        },
      },
    }))
  }
  useScreen.setState({ sentBytes: total })
}

function capture(settings: ShareSettings): void {
  const video = stream?.getVideoTracks()[0]
  if (!video) return
  const content = contentOf(settings)
  const { maxFps } = effectiveCaps(capsOf(settings))
  video.contentHint = content === 'motion' ? 'motion' : 'detail'
  // Capturing fewer frames than will be sent is free quality: do it at the
  // source. Text and desktop never need more than 30.
  void video.applyConstraints({ frameRate: { max: content === 'detail' ? Math.min(maxFps, 30) : maxFps } }).catch(() => undefined)
}

export const useScreen = create<ScreenState>((set, get) => ({
  sharing: null,
  settings: { rung: -1, fps: 0, content: 'auto', sound: false, dataSaver: false },
  viewers: {},
  sentBytes: 0,
  detected: 'motion',
  broadcast: null,
  shares: {},

  start: async (roomId) => {
    if (get().sharing) get().stop()
    const s = get().settings
    void codecCaps('enc') // warm up: the first viewer should not wait for it
    try {
      stream = await navigator.mediaDevices.getDisplayMedia({
        video: { frameRate: { ideal: 60, max: 60 }, width: { max: 1920 }, height: { max: 1080 } },
        audio: s.sound,
      })
    } catch (e) {
      stream = null
      // Closing the picker is "denied by user"; anything else (a policy, the
      // system refusing capture) is worth showing.
      const cancelled = (e as DOMException)?.name === 'NotAllowedError' && /by user/i.test(String((e as Error)?.message))
      if (!cancelled) toast({ tone: 'error', title: tr('screen.failed'), body: String(e) }, 9000)
      return
    }
    stream.getVideoTracks()[0]?.addEventListener('ended', () => get().stop())
    auto = { kind: 'motion', streak: 0 }
    capture(s)
    set({ sharing: { roomId, startedAt: Date.now() }, viewers: {}, sentBytes: 0, detected: 'motion' })
    const hello = () => void sendScreen(roomId, '', { t: 'share', name: myName(), sound: stream?.getAudioTracks().length ? true : false }).catch(() => undefined)
    hello()
    announce = window.setInterval(hello, 10_000)
    tick = window.setInterval(() => void control(), 1000)
  },

  stop: () => {
    const sh = get().sharing
    if (tick) window.clearInterval(tick)
    if (announce) window.clearInterval(announce)
    tick = announce = null
    stopBroadcast()
    for (const id of [...viewers.keys()]) closeViewer(id)
    pendingView.clear()
    stream?.getTracks().forEach((t) => t.stop())
    stream = null
    if (sh) void sendScreen(sh.roomId, '', { t: 'unshare' }).catch(() => undefined)
    set({ sharing: null, viewers: {}, sentBytes: 0 })
  },

  setSettings: (patch) => {
    const settings = { ...get().settings, ...patch }
    set({ settings })
    capture(settings) // caps apply on the next controller second
  },

  onSignal: (roomId, from, data) => {
    if (!from) return
    switch (data.t) {
      case 'share':
        set((s) => ({ shares: { ...s.shares, [roomId]: { peerId: from, name: data.name || from.slice(0, 8), at: Date.now() } } }))
        break
      case 'unshare':
        set((s) => {
          const shares = { ...s.shares }
          if (shares[roomId]?.peerId === from) delete shares[roomId]
          return { shares }
        })
        break
      case 'watch':
        if (get().sharing?.roomId === roomId) void addViewer(roomId, from, data.dec ?? null)
        break
      case 'view': {
        if (get().sharing?.roomId !== roomId) break
        const h = Number.isFinite(data.h) ? Math.max(0, Math.min(4320, data.h)) : 0
        const v = viewers.get(from)
        if (v) {
          v.viewH = h || null
          v.hidden = Boolean(data.hidden)
        } else pendingView.set(from, { h, hidden: Boolean(data.hidden) })
        break
      }
      case 'unwatch':
        closeViewer(from)
        break
      case 'bc-key':
        if (viewers.get(from)?.bcOn) bcast?.enc.requestKey()
        break
      case 'bc-fail': {
        // That viewer cannot decode the shared stream: back to its own video.
        const v = viewers.get(from)
        if (v) {
          v.bcFailed = true
          v.bcOn = false
          v.applied = ''
        }
        break
      }
      case 'answer': {
        const v = viewers.get(from)
        if (v && v.pc.signalingState === 'have-local-offer')
          void v.pc.setRemoteDescription({ type: 'answer', sdp: withBitrateHints(data.sdp, v.start) }).catch(() => undefined)
        break
      }
    }
    for (const fn of viewerHandlers) fn(roomId, from, data)
  },
}))

// Shares nobody re-announced for 30 s are gone (the sharer's app closed).
setInterval(() => {
  const now = Date.now()
  const shares = useScreen.getState().shares
  const alive = Object.fromEntries(Object.entries(shares).filter(([, v]) => now - v.at < 30_000))
  if (Object.keys(alive).length !== Object.keys(shares).length) useScreen.setState({ shares: alive })
}, 5000)

/** The viewer window listens to the same signals through this. */
export const viewerHandlers = new Set<(roomId: string, from: string, data: ScreenSignal) => void>()
