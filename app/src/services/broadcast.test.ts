/* Run: npx tsc src/services/broadcast.test.ts --outDir <tmp> --module es2022 --target es2022 && node <tmp>/broadcast.test.js */
import { Chain, encoderKbps, fragment, LayerRefs, nextFlow, QUEUE_HIGH, QUEUE_STALL, Reassembler, shouldSend, type Frame, type FrameMeta, type ViewerFlow } from './broadcast.js'

let failed = 0
function check(name: string, ok: boolean, info?: unknown): void {
  if (!ok) {
    failed++
    console.error('FAIL', name, info ?? '')
  } else console.log('ok  ', name)
}

const bytes = (n: number, seed = 1) => {
  const b = new Uint8Array(n)
  for (let i = 0; i < n; i++) b[i] = (i * 31 + seed) & 0xff
  return b
}
const same = (a: Uint8Array, b: Uint8Array) => a.length === b.length && a.every((x, i) => x === b[i])

// ---- fragmentation and reassembly
{
  const f: Frame = { id: 7, ref: 3, key: false, layer: 2, ts: 123456.5, data: bytes(40_000) }
  const parts = fragment(f, 15_000)
  check('a 40 kB frame is three messages', parts.length === 3, parts.length)
  const r = new Reassembler()
  // Out of order.
  check('incomplete: nothing yet', r.push(parts[2]!) === null && r.push(parts[0]!) === null)
  const got = r.push(parts[1]!)
  check('complete frame comes back intact', got !== null && same(got.data, f.data) && got.id === 7 && got.ref === 3 && got.layer === 2 && !got.key && got.ts === 123456.5, got)
  check('a duplicate piece is ignored', r.push(parts[1]!) === null)
  const empty = fragment({ ...f, id: 8, data: new Uint8Array(0) })
  check('an empty frame is one message', empty.length === 1 && r.push(empty[0]!)?.data.length === 0)
}

// A lost piece costs only its frame.
{
  const r = new Reassembler()
  const a = fragment({ id: 1, ref: -1, key: true, layer: 0, ts: 0, data: bytes(30_000) }, 15_000)
  const b = fragment({ id: 2, ref: 1, key: false, layer: 2, ts: 1, data: bytes(10_000) }, 15_000)
  r.push(a[0]!) // a[1] lost
  const got = r.push(b[0]!)
  check('the next frame still arrives', got?.id === 2)
  check('the broken one is counted as dropped', r.dropped === 1)
  check('late pieces of a dropped frame are ignored', r.push(a[1]!) === null)
  check('garbage is ignored', r.push(new ArrayBuffer(10)) === null && r.push(new Uint8Array(30).buffer) === null)
}

// ---- L1T3 references
{
  const refs = new LayerRefs()
  const pattern = [0, 2, 1, 2, 0, 2, 1, 2]
  const got = pattern.map((layer, id) => refs.next(id, layer, id === 0))
  // 0:key, 1(T2)->0, 2(T1)->0, 3(T2)->2, 4(T0)->0, 5(T2)->4, 6(T1)->4, 7(T2)->6
  check('L1T3 references', got.join() === '-1,0,0,2,0,4,4,6', got)
}

// ---- decode chain
const metas = (n: number): FrameMeta[] => {
  const refs = new LayerRefs()
  const pattern = [0, 2, 1, 2]
  return Array.from({ length: n }, (_, id) => {
    const layer = pattern[id % 4]!
    const key = id === 0
    return { id, layer, key, ref: refs.next(id, layer, key), ts: id }
  })
}
{
  const fs = metas(12)
  const c = new Chain()
  check('nothing decodes before a keyframe', !c.canTake(fs[1]!))
  let decoded = 0
  for (const f of fs) {
    if (f.id === 2) {
      c.miss(f) // a lost T1
      continue
    }
    if (c.canTake(f)) {
      c.take(f)
      decoded++
    } else c.miss(f)
  }
  // Lost T1 (2) makes T2 (3) undecodable; everything else decodes.
  check('a lost T1 costs only the frame after it', decoded === 10, decoded)

  const c2 = new Chain()
  let after = 0
  for (const f of fs) {
    if (f.id === 4) {
      c2.miss(f) // a lost T0
      continue
    }
    if (c2.canTake(f)) {
      c2.take(f)
      if (f.id > 4) after++
    } else c2.miss(f)
  }
  check('a lost T0 stops decoding until the next keyframe', after === 0 && c2.broken, after)
  const key: FrameMeta = { id: 20, ref: -1, key: true, layer: 0, ts: 20 }
  check('a keyframe repairs it', c2.canTake(key))
}

// ---- per-viewer flow
{
  let f: ViewerFlow = { maxLayer: 2, calmSince: 0 }
  f = nextFlow(f, QUEUE_HIGH + 1, 1000)
  check('a full queue drops a layer at once', f.maxLayer === 1)
  f = nextFlow(f, QUEUE_HIGH + 1, 1100)
  check('and another', f.maxLayer === 0)
  f = nextFlow(f, QUEUE_HIGH + 1, 1200)
  check('never below the base layer', f.maxLayer === 0)
  f = nextFlow(f, 0, 3000)
  check('no step up before 3 s calm', f.maxLayer === 0)
  f = nextFlow(f, 0, 4300)
  check('a layer back after 3 s calm', f.maxLayer === 1)

  const fs = metas(8)
  const slow: ViewerFlow = { maxLayer: 0, calmSince: 0 }
  const sent = new Chain()
  const got: number[] = []
  for (const m of fs) {
    if (shouldSend(m, slow, 0, sent)) {
      sent.take(m)
      got.push(m.id)
    }
  }
  check('a base-layer viewer gets a quarter of the frames', got.join() === '0,4', got)

  const mid: ViewerFlow = { maxLayer: 1, calmSince: 0 }
  const sent2 = new Chain()
  const got2: number[] = []
  for (const m of fs) if (shouldSend(m, mid, 0, sent2)) (sent2.take(m), got2.push(m.id))
  check('T0+T1 is half the frames', got2.join() === '0,2,4,6', got2)

  const stalled = new Chain()
  stalled.take(fs[0]!)
  check('a stalled queue gets keyframes only', !shouldSend(fs[4]!, { maxLayer: 2, calmSince: 0 }, QUEUE_STALL + 1, stalled) && shouldSend({ ...fs[4]!, key: true, ref: -1 }, { maxLayer: 2, calmSince: 0 }, QUEUE_STALL + 1, stalled))
}

// ---- the shared encoder rate
{
  const all = (n: number): ViewerFlow[] => [{ maxLayer: n, calmSince: 0 }]
  check('everyone fine: full budget', encoderKbps(4000, [...all(2), ...all(2), ...all(0)]) === 4000)
  check('the median viewer struggling: less', encoderKbps(4000, [...all(1), ...all(1), ...all(2)]) === 2800)
  check('one slow viewer does not drag the group down', encoderKbps(4000, [...all(0), ...all(2), ...all(2)]) === 4000)
}

if (failed) throw new Error(`${failed} failed`)
console.log('all passed')
