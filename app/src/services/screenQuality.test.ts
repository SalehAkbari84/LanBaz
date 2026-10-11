/* Run: npx tsc src/services/screenQuality.test.ts --outDir <tmp> --module es2022 --target es2022 && node <tmp>/screenQuality.test.js */
import { budgetKbps, classifyContent, codecOrder, decide, HEIGHTS, initialState, startKbps, viewRung, withBitrateHints, type AutoContent, type Caps, type CodecCaps, type QState, type Stats } from './screenQuality.js'

let failed = 0
function check(name: string, ok: boolean, info?: unknown): void {
  if (!ok) {
    failed++
    console.error('FAIL', name, info ?? '')
  } else console.log('ok  ', name)
}

const caps = (over: Partial<Caps> = {}): Caps => ({ maxRung: 5, maxFps: 60, dataSaver: false, content: 'motion', gameRunning: false, ...over })
const clean = (now: number, over: Partial<Stats> = {}): Stats => ({ now, rttMs: 20, loss: 0, ...over })

function run(state: QState, c: Caps, seconds: number, stat: (t: number) => Stats, start = 0) {
  let st = state
  let last = decide(st, stat(start), c)
  for (let i = 1; i <= seconds; i++) {
    last = decide(st, stat(start + i * 1000), c)
    st = last.state
  }
  return { st, last }
}

// The user's caps are the target from the first second - no slow climb.
{
  const c = caps({ maxRung: HEIGHTS.indexOf(720), maxFps: 30 })
  const r = decide(initialState(c, 0), clean(0), c)
  check('720p cap sends 720p at once', HEIGHTS[r.decision.rung] === 720 && r.decision.fps === 30, r.decision)
  check('720p30 ceiling is 2.5 Mbps', r.decision.maxBitrateBps === 2_500_000, r.decision)
}

// The bug this replaces: a ceiling tied to the estimate. Whatever the line
// does on a quiet path, the ceiling stays what the quality needs.
{
  const c = caps({ maxRung: HEIGHTS.indexOf(720), maxFps: 30 })
  const { last } = run(initialState(c, 0), c, 60, (t) => clean(t))
  check('stays at 720p on a clean line', HEIGHTS[last.decision.rung] === 720 && last.decision.maxBitrateBps === 2_500_000, last.decision)
}

// Auto: 1080p60 target.
{
  const c = caps()
  const r = decide(initialState(c, 0), clean(0), c)
  check('auto targets 1080p60', HEIGHTS[r.decision.rung] === 1080 && r.decision.fps === 60, r.decision)
  check('motion lets the browser balance', r.decision.degradation === 'balanced')
  check('detail keeps resolution', decide(initialState(c, 0), clean(0), caps({ content: 'detail' })).decision.degradation === 'maintain-resolution')
}

// Mild trouble: the browser gets three seconds before the target drops.
{
  const c = caps()
  const s0 = initialState(c, 0)
  const two = run(s0, c, 2, (t) => clean(t, { loss: 0.07 }))
  check('two lossy seconds: no change', two.st.penalty === 0, two.st)
  const four = run(s0, c, 4, (t) => clean(t, { loss: 0.07 }))
  check('three lossy seconds: one rung down', four.st.penalty === 1 && four.last.decision.reason === '' || four.st.penalty === 1, four.st)
}

// Severe loss reacts at once.
{
  const c = caps()
  const r = decide(initialState(c, 0), clean(0, { loss: 0.3 }), c)
  check('severe loss steps down at once', r.state.penalty === 1 && r.decision.reason === 'loss', r)
}

// Delay that keeps climbing counts; ordinary jitter does not.
{
  const c = caps()
  const s0: QState = { ...initialState(c, 0), rttBase: 20 }
  const jitter = run(s0, c, 10, (t) => clean(t, { rttMs: t % 2000 ? 60 : 30 }))
  check('jitter alone changes nothing', jitter.st.penalty === 0, jitter.st)
  const climbing = run(s0, c, 5, (t) => clean(t, { rttMs: 200 }))
  check('delay far over the baseline steps down', climbing.st.penalty >= 1, climbing.st)
}

// Recovery: back to the target after clean stretches, one rung at a time.
{
  const c = caps()
  const s0: QState = { penalty: 2, badSince: null, goodSince: 0, rttBase: 20 }
  const seven = run(s0, c, 7, (t) => clean(t))
  check('no step up before 8 s', seven.st.penalty === 2, seven.st)
  const back = run(s0, c, 20, (t) => clean(t))
  check('gives the rungs back', back.st.penalty === 0 && HEIGHTS[back.last.decision.rung] === 1080, back.st)
}

// Never steps below 360p on its own.
{
  const c = caps()
  const { last } = run(initialState(c, 0), c, 120, (t) => clean(t, { loss: 0.5 }))
  check('floor is 360p', HEIGHTS[last.decision.rung] === 360, last.decision)
  const low = caps({ maxRung: 0 })
  const r = run(initialState(low, 0), low, 30, (t) => clean(t, { loss: 0.5 }))
  check('a 144p cap is kept', HEIGHTS[r.last.decision.rung] === 144, r.last.decision)
}

// Game running: 75% ceiling.
{
  const c = caps({ maxRung: HEIGHTS.indexOf(720), maxFps: 30, gameRunning: true })
  const r = decide(initialState(c, 0), clean(0), c)
  check('a game leaves 25% of the ceiling', r.decision.maxBitrateBps === 1_875_000, r.decision)
}

// Data saver.
{
  const c = caps({ dataSaver: true })
  const r = decide(initialState(c, 0), clean(0), c)
  check('data saver caps 720p30', HEIGHTS[r.decision.rung] === 720 && r.decision.fps === 30, r.decision)
  check('data saver budget is 60%', budgetKbps(4, 30, true) === Math.round(budgetKbps(4, 30, false) * 0.6))
}

// New caps apply at once.
{
  const c = caps()
  const { st } = run(initialState(c, 0), c, 5, (t) => clean(t))
  const lower = caps({ maxRung: HEIGHTS.indexOf(480), maxFps: 24 })
  const r = decide(st, clean(6000), lower)
  check('new caps obeyed immediately', HEIGHTS[r.decision.rung] === 480 && r.decision.fps === 24, r.decision)
}

// Bitrate hints in the SDP.
{
  const sdp = [
    'v=0',
    'm=audio 9 UDP/TLS/RTP/SAVPF 111',
    'a=rtpmap:111 opus/48000/2',
    'a=fmtp:111 minptime=10;useinbandfec=1',
    'm=video 9 UDP/TLS/RTP/SAVPF 96 97 98',
    'a=rtpmap:96 VP8/90000',
    'a=rtpmap:97 rtx/90000',
    'a=fmtp:97 apt=96',
    'a=rtpmap:98 H264/90000',
    'a=fmtp:98 level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f',
    '',
  ].join('\r\n')
  const out = withBitrateHints(sdp, 1500)
  check('H.264 fmtp gets the hint', out.includes('profile-level-id=42e01f;x-google-start-bitrate=1500;x-google-min-bitrate=250'), out)
  check('VP8 gets an fmtp line', out.includes('a=rtpmap:96 VP8/90000\r\na=fmtp:96 x-google-start-bitrate=1500'), out)
  check('rtx and audio untouched', out.includes('a=fmtp:97 apt=96\r\n') && out.includes('a=fmtp:111 minptime=10;useinbandfec=1\r\n'), out)
  check('applying twice changes nothing', withBitrateHints(out, 1500) === out)
  check('start bitrate is capped at 2 Mbps', startKbps(caps()) === 2000 && startKbps(caps({ maxRung: 1, maxFps: 15 })) === budgetKbps(1, 15, false))
}

// ---- codec choice
{
  const sw = { supported: true, smooth: true, powerEfficient: false }
  const hw = { supported: true, smooth: true, powerEfficient: true }
  const no = { supported: false, smooth: false, powerEfficient: false }
  const allSw: CodecCaps = { AV1: sw, H265: no, VP9: sw, H264: sw, VP8: sw }
  const dec: CodecCaps = { AV1: sw, H265: no, VP9: hw, H264: hw, VP8: sw }
  check('light work: AV1 (most efficient) first', codecOrder(allSw, dec, 'detail', false)[0] === 'AV1', codecOrder(allSw, dec, 'detail', false))
  check('heavy work, no GPU encoder: H.264 (lightest on the CPU)', codecOrder(allSw, dec, 'motion', true)[0] === 'H264', codecOrder(allSw, dec, 'motion', true))
  const gpuH264: CodecCaps = { ...allSw, H264: hw }
  check('heavy work: the GPU encoder wins', codecOrder(gpuH264, dec, 'motion', true)[0] === 'H264')
  const gpuAv1: CodecCaps = { ...allSw, AV1: hw, H264: hw }
  check('heavy work: GPU AV1 beats GPU H.264', codecOrder(gpuAv1, dec, 'motion', true)[0] === 'AV1')
  const gpuHevc: CodecCaps = { ...allSw, H265: hw, H264: hw }
  const decHevc: CodecCaps = { ...dec, H265: hw }
  check('heavy work: GPU HEVC beats GPU H.264', codecOrder(gpuHevc, decHevc, 'motion', true)[0] === 'H265')
  check('HEVC skipped when the viewer cannot play it', codecOrder(gpuHevc, dec, 'motion', true)[0] === 'H264')
  const slowAv1Dec: CodecCaps = { ...dec, AV1: { supported: true, smooth: false, powerEfficient: false } }
  check('AV1 skipped when the viewer cannot keep up', codecOrder(allSw, slowAv1Dec, 'detail', false)[0] !== 'AV1')
  check('AV1 not assumed for an unknown viewer', codecOrder(allSw, null, 'detail', false)[0] === 'VP9', codecOrder(allSw, null, 'detail', false))
  check('unknown encoder: safe order', codecOrder(null, dec, 'motion', true).join() === 'H264,VP9,VP8')
  check('fallbacks stay in the list', codecOrder(gpuAv1, dec, 'motion', true).includes('VP8'))
}

// ---- the viewer's size
{
  check('1080 shown -> 1080p', HEIGHTS[viewRung(1080)] === 1080)
  check('a 500 px window -> 480p', HEIGHTS[viewRung(500)] === 480, HEIGHTS[viewRung(500)])
  check('a 700 px window -> 720p', HEIGHTS[viewRung(700)] === 720)
  check('unknown -> no limit', HEIGHTS[viewRung(null)] === 1080)
  const c = caps({ viewHeight: 500 })
  const r = decide(initialState(c, 0), clean(0), c)
  check('decide honours the viewer size', HEIGHTS[r.decision.rung] === 480, r.decision)
  check('start bitrate follows the viewer size', startKbps(c) === budgetKbps(3, 60, false))
}

// ---- bits per codec
{
  const h264 = decide(initialState(caps(), 0), clean(0), caps({ codec: 'H264' })).decision.maxBitrateBps
  const av1 = decide(initialState(caps(), 0), clean(0), caps({ codec: 'AV1' })).decision.maxBitrateBps
  check('AV1 needs about a third fewer bits', Math.abs(av1 / h264 - 0.65) < 0.01, { h264, av1 })
}

// ---- Auto content
{
  let a: AutoContent = { kind: 'motion', streak: 0 }
  for (let i = 0; i < 5; i++) a = classifyContent(a, 2)
  check('five calm seconds: still motion', a.kind === 'motion')
  a = classifyContent(a, 2)
  check('six calm seconds: detail', a.kind === 'detail')
  a = classifyContent(a, 40)
  check('one busy second: still detail', a.kind === 'detail')
  a = classifyContent(a, 40)
  check('two busy seconds: motion', a.kind === 'motion')
  let b: AutoContent = { kind: 'motion', streak: 0 }
  for (let i = 0; i < 20; i++) b = classifyContent(b, i % 3 ? 3 : 25)
  check('a scroll now and then keeps motion', b.kind === 'motion')
  const d = decide(initialState(caps(), 0), clean(0), caps({ content: 'detail' }))
  check('text never gets more than 30 fps', d.decision.fps === 30, d.decision)
}

if (failed) throw new Error(`${failed} failed`)
console.log('all passed')
