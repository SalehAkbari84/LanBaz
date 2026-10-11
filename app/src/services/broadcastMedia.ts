/**
 * The browser half of broadcast mode (see broadcast.ts for the design): one
 * WebCodecs encoder for every viewer on the sharer, a WebCodecs decoder per
 * viewer window.
 */

import { Chain, fragment, LayerRefs, Reassembler, type Frame, type FrameMeta } from './broadcast'
import type { Codec } from './screenQuality'

/** WebCodecs strings, with the profile and level a 1080p60 picture needs. */
export const WEBCODECS: Record<Codec, string> = {
  AV1: 'av01.0.09M.08',
  H265: 'hvc1.1.6.L123.90',
  VP9: 'vp09.00.41.08',
  H264: 'avc1.64002A',
  VP8: 'vp8',
}

type AnyCtor = new (...args: never[]) => unknown
const g = globalThis as unknown as Record<string, AnyCtor | undefined>

/** WebCodecs plus the track processor and generator (Chromium). */
export function broadcastSupported(): boolean {
  return Boolean(g.VideoEncoder && g.VideoDecoder && g.MediaStreamTrackProcessor && g.MediaStreamTrackGenerator)
}

function encoderConfig(codec: Codec, width: number, height: number, kbps: number, fps: number, svc: boolean, hw: HardwareAcceleration): VideoEncoderConfig {
  const cfg: VideoEncoderConfig & Record<string, unknown> = {
    codec: WEBCODECS[codec],
    width,
    height,
    bitrate: kbps * 1000,
    framerate: fps,
    hardwareAcceleration: hw,
    latencyMode: 'realtime',
    bitrateMode: 'variable',
  }
  if (svc) cfg.scalabilityMode = 'L1T3'
  // Self-contained chunks: parameter sets travel with every keyframe.
  if (codec === 'H264') cfg.avc = { format: 'annexb' }
  if (codec === 'H265') cfg.hevc = { format: 'annexb' }
  return cfg
}

export interface EncoderTarget {
  width: number
  height: number
  fps: number
  kbps: number
}

/**
 * One encoder for every viewer. Reads frames from the captured track, encodes
 * them in three temporal layers when the codec allows, and hands each encoded
 * frame to onFrame once - the caller fans it out.
 */
export class BroadcastEncoder {
  codec: Codec | null = null
  svc = false
  hw: HardwareAcceleration = 'prefer-hardware'
  private enc: VideoEncoder | null = null
  private reader: ReadableStreamDefaultReader<VideoFrame> | null = null
  private track: MediaStreamTrack | null = null
  private target: EncoderTarget | null = null
  private wantKey = true
  private lastKey = 0
  private id = 0
  private refs = new LayerRefs()
  private closed = false
  private lastFrameAt = 0

  constructor(private onFrame: (f: Frame) => void, private onError: (e: unknown) => void) {}

  /**
   * Picks a codec and starts. The graphics card first: the first codec in
   * order it encodes; only when it encodes none, the first one software does.
   * Temporal layers when the encoder can make them.
   */
  async start(source: MediaStreamTrack, order: Codec[], target: EncoderTarget): Promise<Codec> {
    search: for (const hw of ['prefer-hardware', 'no-preference'] as HardwareAcceleration[]) {
      for (const c of order) {
        for (const svc of [true, false]) {
          const cfg = encoderConfig(c, target.width, target.height, target.kbps, target.fps, svc, hw)
          try {
            if ((await VideoEncoder.isConfigSupported(cfg)).supported) {
              this.codec = c
              this.svc = svc
              this.hw = hw
              break search
            }
          } catch {
            // try the next
          }
        }
      }
    }
    if (!this.codec) throw new Error('no codec the encoder supports')
    this.target = target
    this.configure()
    this.track = source.clone()
    const Proc = g.MediaStreamTrackProcessor as unknown as new (o: { track: MediaStreamTrack }) => { readable: ReadableStream<VideoFrame> }
    this.reader = new Proc({ track: this.track }).readable.getReader()
    void this.pump()
    return this.codec
  }

  private configure(): void {
    if (!this.codec || !this.target) return
    if (!this.enc) {
      this.enc = new VideoEncoder({
        output: (chunk, meta) => this.output(chunk, meta),
        error: (e) => this.onError(e),
      })
    }
    const t = this.target
    this.enc.configure(encoderConfig(this.codec, t.width, t.height, t.kbps, t.fps, this.svc, this.hw))
    this.wantKey = true
  }

  /** New size, frame rate or bitrate. Small bitrate wobbles are ignored. */
  retarget(t: EncoderTarget): void {
    const p = this.target
    if (!p) return
    const sizeChanged = p.width !== t.width || p.height !== t.height
    const rateChanged = Math.abs(t.kbps - p.kbps) / p.kbps > 0.15 || p.fps !== t.fps
    if (!sizeChanged && !rateChanged) return
    this.target = t
    this.configure()
  }

  requestKey(): void {
    // At most one forced keyframe a second: they are big.
    if (Date.now() - this.lastKey > 1000) this.wantKey = true
  }

  private async pump(): Promise<void> {
    while (!this.closed && this.reader) {
      let r: ReadableStreamReadResult<VideoFrame>
      try {
        r = await this.reader.read()
      } catch {
        break
      }
      if (r.done || !r.value) break
      const frame = r.value
      const now = performance.now()
      const minGap = 1000 / (this.target?.fps ?? 60) - 2
      // Keep latency low: skip a frame rather than queue behind the encoder,
      // and never encode faster than the target frame rate.
      if (!this.enc || this.enc.encodeQueueSize > 2 || now - this.lastFrameAt < minGap) {
        frame.close()
        continue
      }
      this.lastFrameAt = now
      const key = this.wantKey || Date.now() - this.lastKey > 5000
      try {
        this.enc.encode(frame, { keyFrame: key })
        if (key) {
          this.wantKey = false
          this.lastKey = Date.now()
        }
      } catch (e) {
        this.onError(e)
      }
      frame.close()
    }
  }

  private output(chunk: EncodedVideoChunk, meta?: EncodedVideoChunkMetadata): void {
    const data = new Uint8Array(chunk.byteLength)
    chunk.copyTo(data)
    const key = chunk.type === 'key'
    const layer = key ? 0 : ((meta as { svc?: { temporalLayerId?: number } } | undefined)?.svc?.temporalLayerId ?? 0)
    const id = this.id++
    const ref = this.refs.next(id, layer, key)
    this.onFrame({ id, ref, key, layer, ts: chunk.timestamp, data })
  }

  close(): void {
    this.closed = true
    void this.reader?.cancel().catch(() => undefined)
    this.track?.stop()
    try {
      this.enc?.close()
    } catch {
      // already closed
    }
    this.enc = null
  }
}

/** Sends one encoded frame to one viewer's channel, as pieces. */
export function sendFrame(dc: RTCDataChannel, f: Frame): void {
  for (const part of fragment(f)) dc.send(part)
}

/**
 * The viewer's side: pieces in, a video track out. Only frames whose
 * reference was decoded are decoded; a broken base layer asks for a keyframe.
 */
export class BroadcastReceiver {
  readonly track: MediaStreamTrack
  private writer: WritableStreamDefaultWriter<VideoFrame>
  private dec: VideoDecoder
  private parts = new Reassembler()
  private chain = new Chain()
  private lastAsk = 0
  frames = 0
  bytes = 0
  height = 0
  private configured = false

  constructor(private codec: Codec, private askKey: () => void, private onError: (e: unknown) => void) {
    const Gen = g.MediaStreamTrackGenerator as unknown as new (o: { kind: 'video' }) => MediaStreamTrack & { writable: WritableStream<VideoFrame> }
    const gen = new Gen({ kind: 'video' })
    this.track = gen
    this.writer = gen.writable.getWriter()
    this.dec = new VideoDecoder({
      output: (frame) => {
        this.frames++
        this.height = frame.displayHeight
        void this.writer.write(frame).catch(() => frame.close())
      },
      error: (e) => {
        this.chain.broken = true
        this.configured = false
        this.onError(e)
      },
    })
  }

  push(buf: ArrayBuffer): void {
    this.bytes += buf.byteLength
    const f = this.parts.push(buf)
    if (!f) return
    this.decode(f)
  }

  private decode(f: Frame): void {
    if (!this.configured) {
      if (!f.key) return this.ask()
      try {
        // The browser uses the graphics card's decoder when it has one.
        this.dec.configure({ codec: WEBCODECS[this.codec], optimizeForLatency: true })
        this.configured = true
      } catch (e) {
        this.onError(e)
        return
      }
    }
    const meta: FrameMeta = f
    if (!this.chain.canTake(meta)) {
      this.chain.miss(meta)
      if (this.chain.broken) this.ask()
      return
    }
    // A decoder falling behind is better skipped than queued (latency).
    if (this.dec.decodeQueueSize > 4 && !f.key) {
      this.chain.miss(meta)
      if (this.chain.broken) this.ask()
      return
    }
    try {
      this.dec.decode(new EncodedVideoChunk({ type: f.key ? 'key' : 'delta', timestamp: f.ts, data: f.data }))
      this.chain.take(meta)
    } catch (e) {
      this.chain.broken = true
      this.onError(e)
    }
  }

  private ask(): void {
    if (Date.now() - this.lastAsk < 1000) return
    this.lastAsk = Date.now()
    this.askKey()
  }

  close(): void {
    try {
      this.dec.close()
    } catch {
      // already closed
    }
    void this.writer.close().catch(() => undefined)
    this.track.stop()
  }
}
