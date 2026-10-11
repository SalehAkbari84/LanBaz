/**
 * Broadcast mode for screen share: one encode for every viewer.
 *
 * With WebRTC each viewer gets its own peer connection and its own encoder,
 * so three viewers cost three encodes. Here the sharer encodes once with
 * WebCodecs (on the GPU where it can), in three temporal layers (L1T3):
 *
 *   frame:  0   1   2   3   4   5   6   7 ...
 *   layer:  T0  T2  T1  T2  T0  T2  T1  T2
 *
 * and sends the same encoded frames to every viewer over its own unordered,
 * unreliable data channel. A viewer on a slow line is sent fewer layers
 * (T0+T1 = half the frames, T0 = a quarter) - the others are not affected,
 * and nothing is encoded twice. Every frame names the frame it was predicted
 * from, so a viewer only ever decodes frames whose reference it has, and asks
 * for a keyframe when the base layer breaks.
 *
 * This file is pure (no browser APIs) and unit-tested.
 */

// ------------------------------------------------------------------ wire --

/** Bytes of payload per data-channel message: well under SCTP's limits. */
export const CHUNK = 15_000
const MAGIC = 0x4c // 'L'
const VERSION = 1
export const HEADER = 24

export interface FrameMeta {
  /** Increasing per encoded frame. */
  id: number
  /** The frame it was predicted from; -1 for a keyframe. */
  ref: number
  key: boolean
  /** Temporal layer: 0 (base), 1, 2. */
  layer: number
  /** Presentation time, microseconds. */
  ts: number
}

export interface Frame extends FrameMeta {
  data: Uint8Array
}

/**
 * Splits an encoded frame into data-channel messages.
 * Header: magic u8, version u8, flags u8 (bit 0 key), layer u8, id u32,
 * ref i32, index u16, count u16, ts f64.
 */
export function fragment(f: Frame, chunk = CHUNK): ArrayBuffer[] {
  const count = Math.max(1, Math.ceil(f.data.length / chunk))
  if (count > 0xffff) throw new Error('frame too large')
  const out: ArrayBuffer[] = []
  for (let i = 0; i < count; i++) {
    const part = f.data.subarray(i * chunk, Math.min(f.data.length, (i + 1) * chunk))
    const buf = new ArrayBuffer(HEADER + part.length)
    const v = new DataView(buf)
    v.setUint8(0, MAGIC)
    v.setUint8(1, VERSION)
    v.setUint8(2, f.key ? 1 : 0)
    v.setUint8(3, f.layer)
    v.setUint32(4, f.id >>> 0)
    v.setInt32(8, f.ref)
    v.setUint16(12, i)
    v.setUint16(14, count)
    v.setFloat64(16, f.ts)
    new Uint8Array(buf, HEADER).set(part)
    out.push(buf)
  }
  return out
}

interface Partial {
  meta: FrameMeta
  parts: (Uint8Array | null)[]
  have: number
  bytes: number
}

/**
 * Puts frames back together. Pieces may arrive out of order or not at all;
 * a frame missing a piece is dropped once a newer frame is complete, so a
 * lost piece costs one frame, never a stall.
 */
export class Reassembler {
  private open = new Map<number, Partial>()
  private newest = -1
  dropped = 0

  push(buf: ArrayBuffer): Frame | null {
    if (buf.byteLength < HEADER) return null
    const v = new DataView(buf)
    if (v.getUint8(0) !== MAGIC || v.getUint8(1) !== VERSION) return null
    const meta: FrameMeta = { key: (v.getUint8(2) & 1) === 1, layer: v.getUint8(3), id: v.getUint32(4), ref: v.getInt32(8), ts: v.getFloat64(16) }
    const index = v.getUint16(12)
    const count = v.getUint16(14)
    if (count === 0 || index >= count || meta.id <= this.newest) return null
    let p = this.open.get(meta.id)
    if (!p) {
      p = { meta, parts: new Array(count).fill(null), have: 0, bytes: 0 }
      this.open.set(meta.id, p)
    }
    if (p.parts.length !== count || p.parts[index]) return null
    const data = new Uint8Array(buf, HEADER)
    p.parts[index] = data
    p.have++
    p.bytes += data.length
    if (p.have < count) return null
    const out = new Uint8Array(p.bytes)
    let at = 0
    for (const part of p.parts) {
      out.set(part!, at)
      at += part!.length
    }
    this.open.delete(meta.id)
    this.newest = meta.id
    // Older incomplete frames will never be useful now.
    for (const id of [...this.open.keys()]) {
      if (id < meta.id) {
        this.open.delete(id)
        this.dropped++
      }
    }
    return { ...p.meta, data: out }
  }
}

// ---------------------------------------------------------- dependencies --

/**
 * Tracks which frames a decoder has, so it is only ever given frames it can
 * decode. Used on both sides: the sharer per viewer (what it sent), the
 * viewer (what it decoded).
 */
export class Chain {
  private have = new Set<number>()
  private order: number[] = []
  /** Waiting for a keyframe: the base layer broke. */
  broken = true

  /** Whether f can be decoded given what came before. */
  canTake(f: FrameMeta): boolean {
    if (f.key) return true
    return !this.broken && this.have.has(f.ref)
  }

  /** Records a frame as decoded (or sent). */
  take(f: FrameMeta): void {
    if (f.key) {
      this.have.clear()
      this.order = []
      this.broken = false
    }
    this.have.add(f.id)
    this.order.push(f.id)
    if (this.order.length > 64) this.have.delete(this.order.shift()!)
  }

  /**
   * A frame that cannot be used. Losing a base-layer frame breaks the chain:
   * nothing until the next keyframe.
   */
  miss(f: FrameMeta): void {
    if (f.layer === 0) this.broken = true
  }
}

// --------------------------------------------------------- the encoder side --

/**
 * Which frame each new frame refers to, for the L1T3 pattern (T0 T2 T1 T2):
 * T0 → previous T0, T1 → the T0 of its group, T2 → the frame just before it.
 */
export class LayerRefs {
  private lastT0 = -1
  private last = -1

  next(id: number, layer: number, key: boolean): number {
    let ref: number
    if (key) ref = -1
    else if (layer === 0) ref = this.lastT0
    else if (layer === 1) ref = this.lastT0
    else ref = this.last
    if (key || layer === 0) this.lastT0 = id
    this.last = id
    return ref
  }
}

// ---------------------------------------------------- per-viewer congestion --

export interface ViewerFlow {
  /** Highest layer this viewer is sent: 2 = all frames, 1 = half, 0 = a quarter. */
  maxLayer: number
  /** Since when the queue has been short (for stepping back up). */
  calmSince: number
}

/** Bytes waiting in a viewer's channel that mean the line is falling behind. */
export const QUEUE_HIGH = 256 * 1024
const QUEUE_LOW = 32 * 1024
/** Over this, even base frames wait: send keyframes only until it drains. */
export const QUEUE_STALL = 1024 * 1024
const UP_AFTER_MS = 3000

/**
 * One viewer's layer, from how much is still queued in its channel. A full
 * queue means the line cannot carry what we send: drop a layer at once. A
 * short queue for a few seconds: take one back.
 */
export function nextFlow(prev: ViewerFlow, queued: number, now: number): ViewerFlow {
  if (queued > QUEUE_HIGH) return { maxLayer: Math.max(0, prev.maxLayer - 1), calmSince: now }
  if (queued > QUEUE_LOW) return { ...prev, calmSince: now }
  if (prev.maxLayer < 2 && now - prev.calmSince >= UP_AFTER_MS) return { maxLayer: prev.maxLayer + 1, calmSince: now }
  return prev
}

/** Whether a frame goes to a viewer, given its flow, its queue and what it has. */
export function shouldSend(f: FrameMeta, flow: ViewerFlow, queued: number, sent: Chain): boolean {
  if (queued > QUEUE_STALL && !f.key) return false
  if (f.layer > flow.maxLayer && !f.key) return false
  return sent.canTake(f)
}

/**
 * The encoder's bitrate for everyone: the full budget while most viewers take
 * every layer; less when the median viewer is being cut to fewer layers -
 * then the frames themselves are too big for the group's lines.
 */
export function encoderKbps(budgetKbps: number, flows: ViewerFlow[]): number {
  if (!flows.length) return budgetKbps
  const layers = flows.map((f) => f.maxLayer).sort((a, b) => a - b)
  const median = layers[Math.floor((layers.length - 1) / 2)]!
  const scale = median >= 2 ? 1 : median === 1 ? 0.7 : 0.45
  return Math.round(budgetKbps * scale)
}

/** Broadcast mode from this many viewers on; fewer keep the WebRTC path. */
export const BROADCAST_FROM = 3
