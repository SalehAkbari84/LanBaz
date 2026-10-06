/**
 * Voice chat. Audio goes webview to webview over WebRTC, on the room's own
 * 10.200.x addresses: the LanBaz tunnel already crosses NAT, the VPN and the
 * relay, so no STUN, TURN or server is involved and only candidates on the
 * virtual LAN are offered. The daemon carries the signalling (voice.signal).
 *
 * Who offers: when two players are both in voice, the one with the smaller
 * peer id makes the offer, so two people joining at once never collide.
 */

import { invoke } from '@tauri-apps/api/core'
import { listen } from '@tauri-apps/api/event'
import { create } from 'zustand'

import { isTauri } from '../services/tauri'
import { daemonClient } from './daemon'
import { usePrefs } from './prefs'
import { useRoomsStore } from './rooms'
import { toast } from './toasts'
import { translate } from '../i18n'

type Signal =
  | { t: 'hello'; reply?: boolean }
  | { t: 'bye' }
  | { t: 'offer' | 'answer'; sdp: string }
  | { t: 'ice'; c: RTCIceCandidateInit }

export interface VoicePeer {
  speaking: boolean
  volume: number // 0..2
  muted: boolean
}

interface VoiceState {
  roomId: string | null
  joining: boolean
  /** Microphone muted (open-mic mode) or not held (push-to-talk). */
  muted: boolean
  pttHeld: boolean
  speaking: boolean
  peers: Record<string, VoicePeer>
  join: (roomId: string) => Promise<void>
  leave: () => void
  toggleMute: () => void
  setPeerVolume: (peerId: string, volume: number) => void
  togglePeerMute: (peerId: string) => void
  onSignal: (roomId: string, from: string, data: Signal) => void
}

// Live objects that do not belong in React state.
const links = new Map<string, { pc: RTCPeerConnection; audio: HTMLAudioElement; meter?: () => void }>()
let mic: MediaStream | null = null
let ctx: AudioContext | null = null
let stopLocalMeter: (() => void) | null = null
let unlistenPtt: (() => void) | null = null

const SPEAKING_RMS = 0.04

function selfId(roomId: string): string {
  const room = useRoomsStore.getState().rooms[roomId]
  return room?.peers?.find((p) => p.is_self)?.peer_id ?? ''
}

async function send(roomId: string, to: string, data: Signal): Promise<void> {
  await daemonClient()?.request('voice.signal', { room_id: roomId, to, data })
}

/** Only the room's own addresses: everything else would leave the tunnel. */
function onTunnel(c: RTCIceCandidate): boolean {
  const addr = c.address ?? c.candidate.split(' ')[4] ?? ''
  return addr.startsWith('10.200.') || addr.endsWith('.local')
}

function meter(stream: MediaStream, onLevel: (speaking: boolean) => void): () => void {
  ctx ??= new AudioContext()
  const src = ctx.createMediaStreamSource(stream)
  const an = ctx.createAnalyser()
  an.fftSize = 512
  src.connect(an)
  const buf = new Float32Array(an.fftSize)
  let last = false
  const id = window.setInterval(() => {
    an.getFloatTimeDomainData(buf)
    let sum = 0
    for (const v of buf) sum += v * v
    const now = Math.sqrt(sum / buf.length) > SPEAKING_RMS
    if (now !== last) onLevel((last = now))
  }, 100)
  return () => {
    window.clearInterval(id)
    src.disconnect()
  }
}

function applyMicState(): void {
  const { muted, pttHeld } = useVoice.getState()
  const ptt = usePrefs.getState().voicePtt
  const live = ptt ? pttHeld : !muted
  mic?.getAudioTracks().forEach((t) => (t.enabled = live))
}

function closeLink(peerId: string): void {
  const l = links.get(peerId)
  if (!l) return
  l.meter?.()
  l.pc.close()
  l.audio.srcObject = null
  links.delete(peerId)
  useVoice.setState((s) => {
    const peers = { ...s.peers }
    delete peers[peerId]
    return { peers }
  })
}

function link(roomId: string, peerId: string): RTCPeerConnection {
  const existing = links.get(peerId)
  if (existing) return existing.pc
  const pc = new RTCPeerConnection({ iceServers: [] })
  const audio = new Audio()
  audio.autoplay = true
  const entry: { pc: RTCPeerConnection; audio: HTMLAudioElement; meter?: () => void } = { pc, audio }
  links.set(peerId, entry)
  mic?.getTracks().forEach((t) => pc.addTrack(t, mic!))
  pc.onicecandidate = (e) => {
    if (e.candidate && onTunnel(e.candidate)) void send(roomId, peerId, { t: 'ice', c: e.candidate.toJSON() })
  }
  pc.ontrack = (e) => {
    const stream = e.streams[0] ?? new MediaStream([e.track])
    audio.srcObject = stream
    const p = useVoice.getState().peers[peerId]
    audio.volume = Math.min(1, p?.volume ?? 1)
    audio.muted = p?.muted ?? false
    entry.meter?.()
    entry.meter = meter(stream, (speaking) =>
      useVoice.setState((s) => ({ peers: { ...s.peers, [peerId]: { ...(s.peers[peerId] ?? { volume: 1, muted: false }), speaking } } })),
    )
  }
  pc.onconnectionstatechange = () => {
    if (pc.connectionState === 'failed' || pc.connectionState === 'closed') closeLink(peerId)
  }
  useVoice.setState((s) => ({ peers: { ...s.peers, [peerId]: s.peers[peerId] ?? { speaking: false, volume: 1, muted: false } } }))
  return pc
}

async function offer(roomId: string, peerId: string): Promise<void> {
  const pc = link(roomId, peerId)
  await pc.setLocalDescription(await pc.createOffer())
  await send(roomId, peerId, { t: 'offer', sdp: pc.localDescription!.sdp })
}

export const useVoice = create<VoiceState>((set, get) => ({
  roomId: null,
  joining: false,
  muted: false,
  pttHeld: false,
  speaking: false,
  peers: {},

  join: async (roomId) => {
    if (get().roomId) get().leave()
    set({ joining: true })
    try {
      mic = await navigator.mediaDevices.getUserMedia({
        audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true },
      })
    } catch (e) {
      set({ joining: false })
      toast({ tone: 'error', title: translate(usePrefs.getState().language, 'voice.noMic'), body: String(e) }, 8000)
      return
    }
    stopLocalMeter = meter(mic, (speaking) => set({ speaking }))
    set({ roomId, joining: false, peers: {} })
    applyMicState()
    if (isTauri()) {
      await invoke('ptt_set_key', { vk: usePrefs.getState().voicePtt ? usePrefs.getState().voicePttKey : 0 })
      unlistenPtt = await listen<boolean>('ptt', (e) => {
        set({ pttHeld: e.payload })
        applyMicState()
      })
    }
    await send(roomId, '', { t: 'hello' })
  },

  leave: () => {
    const roomId = get().roomId
    if (roomId) void send(roomId, '', { t: 'bye' }).catch(() => {})
    for (const id of [...links.keys()]) closeLink(id)
    stopLocalMeter?.()
    stopLocalMeter = null
    mic?.getTracks().forEach((t) => t.stop())
    mic = null
    unlistenPtt?.()
    unlistenPtt = null
    if (isTauri()) void invoke('ptt_set_key', { vk: 0 })
    set({ roomId: null, peers: {}, speaking: false, pttHeld: false })
  },

  toggleMute: () => {
    set({ muted: !get().muted })
    applyMicState()
  },

  setPeerVolume: (peerId, volume) => {
    const a = links.get(peerId)?.audio
    if (a) a.volume = Math.min(1, volume)
    set((s) => ({ peers: { ...s.peers, [peerId]: { ...(s.peers[peerId] ?? { speaking: false, muted: false }), volume } } }))
  },

  togglePeerMute: (peerId) => {
    const muted = !get().peers[peerId]?.muted
    const a = links.get(peerId)?.audio
    if (a) a.muted = muted
    set((s) => ({ peers: { ...s.peers, [peerId]: { ...(s.peers[peerId] ?? { speaking: false, volume: 1 }), muted } } }))
  },

  onSignal: (roomId, from, data) => {
    if (get().roomId !== roomId || !from || !mic) return
    const me = selfId(roomId)
    switch (data.t) {
      case 'hello':
        if (me && me < from) void offer(roomId, from)
        else if (!data.reply) void send(roomId, from, { t: 'hello', reply: true })
        break
      case 'offer': {
        const pc = link(roomId, from)
        void (async () => {
          await pc.setRemoteDescription({ type: 'offer', sdp: data.sdp })
          await pc.setLocalDescription(await pc.createAnswer())
          await send(roomId, from, { t: 'answer', sdp: pc.localDescription!.sdp })
        })()
        break
      }
      case 'answer':
        void links.get(from)?.pc.setRemoteDescription({ type: 'answer', sdp: data.sdp })
        break
      case 'ice':
        void links.get(from)?.pc.addIceCandidate(data.c).catch(() => {})
        break
      case 'bye':
        closeLink(from)
        break
    }
  },
}))

/** Push-to-talk settings changed while in voice: apply them now. */
export function applyVoicePrefs(): void {
  if (!useVoice.getState().roomId) return
  const p = usePrefs.getState()
  if (isTauri()) void invoke('ptt_set_key', { vk: p.voicePtt ? p.voicePttKey : 0 })
  applyMicState()
}
