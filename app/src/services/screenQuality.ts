/**
 * The screen-share quality controller: decides, once a second per viewer, the
 * resolution, frame rate and bitrate ceiling to send. Pure functions only
 * (tested without a browser); screen.ts feeds it WebRTC stats and applies the
 * answer with RTCRtpSender.setParameters, which needs no renegotiation.
 *
 * Division of labour. The browser's own congestion control (GCC) already does
 * the fast part well: it probes the path, backs off within a frame or two when
 * delay builds, and scales resolution and frame rate down and back up by
 * itself. It can only do that if it is allowed to send: a ceiling tied to its
 * own estimate keeps it application-limited, the estimate never grows, and the
 * picture spirals down to the lowest rung and stays there.
 *
 * So the ceilings here are fixed by what the chosen quality needs (never by
 * the estimate), and this controller only steps in on sustained trouble the
 * browser is not fixing - heavy loss or a delay that keeps climbing - by
 * lowering the target one rung, and gives it back after a clean stretch.
 */

export const HEIGHTS = [144, 240, 360, 480, 720, 1080] as const
export const FPS = [10, 15, 24, 30, 45, 60] as const

/** Ceiling at 30 fps per rung, kbps: what the rung needs to look sharp in motion. */
const BASE_KBPS = [200, 400, 800, 1300, 2500, 4500]

export type Content = 'motion' | 'detail'

export interface Caps {
  /** Highest rung index the user allows (HEIGHTS). */
  maxRung: number
  /** Highest frame rate the user allows. */
  maxFps: number
  dataSaver: boolean
  content: Content
  /** A LAN game is running: the share gets a lower ceiling and lower priority. */
  gameRunning: boolean
  /** Device pixels the viewer actually shows the picture at; null = unknown. */
  viewHeight?: number | null
  /** The codec in use, which sets how many bits a rung needs. */
  codec?: Codec | null
}

export interface Stats {
  now: number // ms
  rttMs: number | null
  /** 0..1, from the viewer's reports. */
  loss: number
}

export interface QState {
  /** Rungs below the target, from sustained trouble. */
  penalty: number
  /** When the current trouble started, or null. */
  badSince: number | null
  /** When the last trouble ended (or the last step). */
  goodSince: number
  rttBase: number | null
}

export interface Decision {
  rung: number
  fps: number
  maxBitrateBps: number
  degradation: 'balanced' | 'maintain-resolution'
  /** Why it changed, for the stats line; '' when it did not. */
  reason: '' | 'loss' | 'delay' | 'up'
}

const IDX_720 = HEIGHTS.indexOf(720)
/** Trouble must last this long before the target drops (the browser gets first go). */
const DOWN_AFTER_MS = 3_000
/** A clean stretch this long gives one rung back. */
const UP_AFTER_MS = 8_000
const LOSSY = 0.05
const SEVERE_LOSS = 0.15
/** Never step below 360p on our own: under that the browser's own scaling is better. */
const FLOOR_RUNG = HEIGHTS.indexOf(360)

// ------------------------------------------------------------------ codecs --

export type Codec = 'AV1' | 'H265' | 'VP9' | 'H264' | 'VP8'
export const CODECS: Codec[] = ['AV1', 'H265', 'VP9', 'H264', 'VP8']

export interface CodecSupport {
  supported: boolean
  /** Keeps up in real time on this machine. */
  smooth: boolean
  /** Done by the GPU / media engine rather than the CPU. */
  powerEfficient: boolean
}
export type CodecCaps = Partial<Record<Codec, CodecSupport>>

/** Bits a codec needs for the same picture, relative to H.264. */
const FACTOR: Record<Codec, number> = { AV1: 0.65, H265: 0.7, VP9: 0.8, H264: 1, VP8: 1.15 }
const EFFICIENCY: Record<Codec, number> = { AV1: 5, H265: 4, VP9: 3, H264: 2, VP8: 1 }
/** How light each codec is on the CPU when it has to encode in software. */
const CPU_LIGHT: Record<Codec, number> = { H264: 4, VP8: 3, VP9: 2, AV1: 1, H265: 0 }

export function codecFactor(c: Codec | null | undefined): number {
  return c ? FACTOR[c] : 1
}

/**
 * Codec order for one viewer, from what this machine can encode and what the
 * viewer's machine can decode.
 *
 * Light work (text, desktop, small sizes): the most efficient codec wins - AV1
 * needs about a third fewer bits than H.264 for the same picture.
 *
 * Heavy work (720p+ at 30+ fps): a codec the GPU encodes wins, the most
 * efficient of those first (AV1, then HEVC, VP9, H.264). With no hardware
 * encoder at all, the lightest software encoder wins (H.264), because a
 * CPU-bound encoder next to a running game is what makes a share stutter,
 * blur and slow the game down.
 *
 * Unknown capabilities fall back to a safe order.
 */
export function codecOrder(enc: CodecCaps | null, dec: CodecCaps | null, content: Content, heavy: boolean): Codec[] {
  const fallback: Codec[] = content === 'motion' ? ['H264', 'VP9', 'VP8'] : ['VP9', 'H264', 'VP8']
  if (!enc) return fallback
  const scored: Array<{ c: Codec; score: number }> = []
  for (const c of CODECS) {
    const e = enc[c]
    const d = dec?.[c]
    if (!e?.supported) continue
    // The viewer must be able to play it in real time; unknown counts as yes
    // for the codecs every WebRTC engine decodes.
    if (d ? !d.supported || !d.smooth : c === 'AV1' || c === 'H265') continue
    if (heavy && !e.smooth && !e.powerEfficient) continue
    let score = heavy ? (e.powerEfficient ? 10 + EFFICIENCY[c] : CPU_LIGHT[c]) : EFFICIENCY[c] + (e.powerEfficient ? 1 : 0)
    if (d?.powerEfficient) score += 0.5
    scored.push({ c, score })
  }
  if (!scored.length) return fallback
  scored.sort((a, b) => b.score - a.score || CODECS.indexOf(a.c) - CODECS.indexOf(b.c))
  const order = scored.map((x) => x.c)
  for (const c of fallback) if (!order.includes(c)) order.push(c)
  return order
}

// -------------------------------------------------------------- the viewer --

/**
 * The lowest rung that still fills what the viewer shows: sending 1080p to a
 * window 500 pixels tall is bits the viewer's screen throws away.
 */
export function viewRung(viewHeight: number | null | undefined): number {
  if (!viewHeight || viewHeight <= 0) return HEIGHTS.length - 1
  const i = HEIGHTS.findIndex((h) => h >= viewHeight * 0.9)
  return i < 0 ? HEIGHTS.length - 1 : i
}

// ---------------------------------------------------------- content (Auto) --

export interface AutoContent {
  kind: Content
  /** Consecutive seconds of the opposite evidence. */
  streak: number
}

/**
 * Auto content: the screen capturer only produces a frame when something on
 * screen changed, so the frame rate actually captured says what is being
 * shared. A game or a video changes constantly; a desktop, a document or a
 * chat changes in bursts. Switches to motion fast (2 s), back to detail slowly
 * (6 s), so a scroll does not flip it.
 */
export function classifyContent(prev: AutoContent, capturedFps: number): AutoContent {
  if (prev.kind === 'detail') {
    if (capturedFps >= 18) return prev.streak + 1 >= 2 ? { kind: 'motion', streak: 0 } : { kind: 'detail', streak: prev.streak + 1 }
    return { kind: 'detail', streak: 0 }
  }
  if (capturedFps < 8) return prev.streak + 1 >= 6 ? { kind: 'detail', streak: 0 } : { kind: 'motion', streak: prev.streak + 1 }
  return { kind: 'motion', streak: 0 }
}

/** Frames a text / desktop share needs at most. */
const DETAIL_MAX_FPS = 30

export function effectiveCaps(c: Caps): { maxRung: number; maxFps: number } {
  let maxRung = Math.max(0, Math.min(c.maxRung, HEIGHTS.length - 1))
  let maxFps = Math.max(FPS[0], Math.min(c.maxFps, 60))
  if (c.dataSaver) {
    maxRung = Math.min(maxRung, IDX_720)
    maxFps = Math.min(maxFps, 30)
  }
  return { maxRung, maxFps }
}

/** Bitrate ceiling for a rung and frame rate, kbps. */
export function budgetKbps(rung: number, fps: number, dataSaver: boolean): number {
  const fpsFactor = fps >= 45 ? 1.5 : fps >= 30 ? 1 : fps >= 24 ? 0.85 : fps >= 15 ? 0.65 : 0.5
  return Math.round(BASE_KBPS[rung]! * fpsFactor * (dataSaver ? 0.6 : 1))
}

/** A good starting bitrate for the browser's estimator, kbps. */
export function startKbps(c: Caps): number {
  const { maxRung, maxFps } = effectiveCaps(c)
  const rung = Math.min(maxRung, viewRung(c.viewHeight))
  return Math.min(Math.round(budgetKbps(rung, maxFps, c.dataSaver) * codecFactor(c.codec)), 2000)
}

export function initialState(_c: Caps, now: number): QState {
  return { penalty: 0, badSince: null, goodSince: now, rttBase: null }
}

/** One controller step. Returns the new state and what to apply. */
export function decide(prev: QState, s: Stats, c: Caps): { state: QState; decision: Decision } {
  const st: QState = { ...prev }
  const eff = effectiveCaps(c)
  const maxRung = Math.min(eff.maxRung, viewRung(c.viewHeight))
  const maxFps = c.content === 'detail' ? Math.min(eff.maxFps, DETAIL_MAX_FPS) : eff.maxFps
  let reason: Decision['reason'] = ''

  // The lowest delay seen is the baseline; it drifts up slowly so a route
  // change does not look like congestion forever.
  if (s.rttMs != null && s.rttMs > 0) st.rttBase = st.rttBase == null ? s.rttMs : Math.min(s.rttMs, st.rttBase * 1.002 + 0.05)

  const lossy = s.loss > LOSSY
  const delayed = s.rttMs != null && st.rttBase != null && s.rttMs > st.rttBase * 2 && s.rttMs - st.rttBase > 80
  const floor = Math.min(FLOOR_RUNG, maxRung)
  const maxPenalty = maxRung - floor

  if (lossy || delayed) {
    st.badSince ??= s.now
    const severe = s.loss > SEVERE_LOSS
    if ((severe || s.now - st.badSince >= DOWN_AFTER_MS) && st.penalty < maxPenalty) {
      st.penalty += 1
      st.badSince = s.now
      reason = lossy ? 'loss' : 'delay'
    }
    st.goodSince = s.now
  } else {
    st.badSince = null
    if (st.penalty > 0 && s.now - st.goodSince >= UP_AFTER_MS) {
      st.penalty -= 1
      st.goodSince = s.now
      reason = 'up'
    }
  }
  st.penalty = Math.max(0, Math.min(st.penalty, maxPenalty))

  const rung = maxRung - st.penalty
  const fps = maxFps
  let kbps = Math.round(budgetKbps(rung, fps, c.dataSaver) * codecFactor(c.codec))
  // A running game keeps headroom on the line; the lower RTP priority set by
  // screen.ts does the rest.
  if (c.gameRunning) kbps = Math.round(kbps * 0.75)
  return {
    state: st,
    decision: {
      rung,
      fps,
      maxBitrateBps: kbps * 1000,
      // Text keeps its sharpness; everything else lets the browser trade
      // lines and frames as it sees fit, which recovers by itself.
      degradation: c.content === 'detail' ? 'maintain-resolution' : 'balanced',
      reason,
    },
  }
}

/** scaleResolutionDownBy for a rung, given the captured height. */
export function scaleFor(capturedHeight: number, rung: number): number {
  const h = HEIGHTS[rung]!
  return capturedHeight > h ? capturedHeight / h : 1
}

/**
 * Raises the browser's starting bitrate and sets a floor, so the picture is
 * sharp within a second or two instead of climbing up from 300 kbps, and never
 * collapses to a smear. Applied to the video codecs' fmtp lines.
 */
export function withBitrateHints(sdp: string, startKbps: number, minKbps = 250): string {
  const lines = sdp.split('\r\n')
  const video = new Set<string>()
  let inVideo = false
  for (const l of lines) {
    if (l.startsWith('m=')) inVideo = l.startsWith('m=video')
    const m = inVideo && /^a=rtpmap:(\d+) (VP9|VP8|H264|H265|AV1)\//.exec(l)
    if (m) video.add(m[1]!)
  }
  const hint = `x-google-start-bitrate=${Math.round(startKbps)};x-google-min-bitrate=${Math.min(minKbps, Math.round(startKbps))}`
  const seen = new Set<string>()
  const out: string[] = []
  for (const l of lines) {
    const f = /^a=fmtp:(\d+) (.*)$/.exec(l)
    if (f && video.has(f[1]!)) {
      seen.add(f[1]!)
      out.push(/x-google-start-bitrate/.test(l) ? l : `${l};${hint}`)
      continue
    }
    out.push(l)
    const r = /^a=rtpmap:(\d+) /.exec(l)
    // A codec with no fmtp line (VP8) gets one right after its rtpmap.
    if (r && video.has(r[1]!) && !lines.some((x) => x.startsWith(`a=fmtp:${r[1]} `))) {
      out.push(`a=fmtp:${r[1]} ${hint}`)
      seen.add(r[1]!)
    }
  }
  return out.join('\r\n')
}
