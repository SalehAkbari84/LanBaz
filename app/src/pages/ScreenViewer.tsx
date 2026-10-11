import { Maximize, Volume2, VolumeX } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'

import { useT } from '../i18n'
import { routeTo } from '../services/audio'
import type { Codec } from '../services/screenQuality'
import { BroadcastReceiver, broadcastSupported } from '../services/broadcastMedia'
import { gathered, tunnelOnly } from '../services/rtc'
import { codecCaps, codecOf, sendScreen, viewerHandlers, type ScreenSignal } from '../stores/screen'
import { useDaemonStore } from '../stores/daemon'
import { usePrefs } from '../stores/prefs'

interface Live {
  width: number
  height: number
  fps: number
  kbps: number
  rttMs: number | null
  mb: number
  codec: string
}

/**
 * Device pixels the picture is shown at: the video box, letterboxed to the
 * stream's shape, times the screen's scaling. The sharer sends no more lines
 * than this, so a small window costs a fraction of a full-screen one.
 */
function shownHeight(v: HTMLVideoElement | null): number {
  const dpr = window.devicePixelRatio || 1
  if (!v) return Math.round(window.innerHeight * dpr)
  const boxW = v.clientWidth || window.innerWidth
  const boxH = v.clientHeight || window.innerHeight
  const aspect = v.videoWidth && v.videoHeight ? v.videoWidth / v.videoHeight : 16 / 9
  return Math.round(Math.min(boxH, boxW / aspect) * dpr)
}

/**
 * The viewer window: one player's shared screen. It runs the receiving end of
 * the WebRTC connection itself (a stream cannot cross windows), asks the
 * sharer to send (watch), and says stop (unwatch) when closed.
 */
export function ScreenViewer() {
  const t = useT()
  const params = new URLSearchParams(window.location.search)
  const roomId = params.get('room') ?? ''
  const peerId = params.get('peer') ?? ''
  const name = params.get('name') ?? ''
  const connected = useDaemonStore((s) => s.connection === 'connected')
  const video = useRef<HTMLVideoElement>(null)
  const pcRef = useRef<RTCPeerConnection | null>(null)
  // The WebRTC stream (video + the shared sound), and the broadcast-mode
  // receiver whose decoded track replaces that video while it runs.
  const rtcStream = useRef<MediaStream | null>(null)
  const rx = useRef<{ r: BroadcastReceiver; codec: string } | null>(null)
  const [state, setState] = useState<'waiting' | 'live' | 'ended'>('waiting')
  const [live, setLive] = useState<Live | null>(null)
  const [muted, setMuted] = useState(false)
  const [volume, setVolume] = useState(1)
  const [smooth, setSmooth] = useState(false)
  const speaker = usePrefs((s) => s.speakerDeviceId)
  const output = usePrefs((s) => s.outputVolume)

  useEffect(() => {
    if (!connected || !roomId || !peerId) return
    let gotOffer = false
    const handle = (room: string, from: string, data: ScreenSignal) => {
      if (room !== roomId || from !== peerId) return
      if (data.t === 'unshare') {
        setState('ended')
        endBroadcast()
        pcRef.current?.close()
        return
      }
      if (data.t === 'bc') {
        if (data.codec) startBroadcast(data.codec)
        else endBroadcast()
        return
      }
      if (data.t !== 'offer') return
      gotOffer = true
      endBroadcast()
      pcRef.current?.close()
      const pc = new RTCPeerConnection({ iceServers: [] })
      pcRef.current = pc
      pc.ondatachannel = (e) => {
        if (e.channel.label !== 'bc') return
        e.channel.binaryType = 'arraybuffer'
        e.channel.onmessage = (m) => rx.current?.r.push(m.data as ArrayBuffer)
      }
      pc.ontrack = (e) => {
        const s = e.streams[0] ?? new MediaStream([e.track])
        rtcStream.current = s
        if (video.current && !rx.current) video.current.srcObject = s
        // Low-latency playout: show frames as soon as they arrive.
        try {
          ;(e.receiver as any).jitterBufferTarget = smoothRef.current ? 80 : 0
        } catch {
          // Older engines ignore it.
        }
        setState('live')
      }
      pc.onconnectionstatechange = () => {
        if (pc.connectionState === 'failed') setState('waiting')
      }
      void (async () => {
        await pc.setRemoteDescription({ type: 'offer', sdp: data.sdp })
        await pc.setLocalDescription(await pc.createAnswer())
        await gathered(pc)
        await sendScreen(roomId, peerId, { t: 'answer', sdp: tunnelOnly(pc.localDescription!.sdp) })
        report()
      })()
    }
    // Broadcast mode: the sharer encodes once for every viewer and sends the
    // frames on the "bc" channel; they are decoded here. Anything that goes
    // wrong falls back to this viewer's own WebRTC video.
    const showRtc = () => {
      if (video.current && rtcStream.current) video.current.srcObject = rtcStream.current
    }
    const endBroadcast = () => {
      rx.current?.r.close()
      rx.current = null
      showRtc()
    }
    const startBroadcast = (codec: Codec) => {
      endBroadcast()
      if (!broadcastSupported()) {
        void sendScreen(roomId, peerId, { t: 'bc-fail' }).catch(() => undefined)
        return
      }
      try {
        const r = new BroadcastReceiver(
          codec,
          () => void sendScreen(roomId, peerId, { t: 'bc-key' }).catch(() => undefined),
          () => {
            endBroadcast()
            void sendScreen(roomId, peerId, { t: 'bc-fail' }).catch(() => undefined)
          },
        )
        rx.current = { r, codec }
        const audio = rtcStream.current?.getAudioTracks() ?? []
        if (video.current) video.current.srcObject = new MediaStream([r.track, ...audio])
        setState('live')
      } catch {
        void sendScreen(roomId, peerId, { t: 'bc-fail' }).catch(() => undefined)
      }
    }
    // Tell the sharer how big the picture is shown, and when it is not shown
    // at all (minimized): it then sends just enough, or nothing.
    let lastView = ''
    const report = () => {
      const h = shownHeight(video.current)
      const hidden = document.visibilityState === 'hidden'
      const key = `${h}|${hidden}`
      if (key === lastView) return
      lastView = key
      void sendScreen(roomId, peerId, { t: 'view', h, hidden }).catch(() => undefined)
    }
    let resizeTimer = 0
    const onResize = () => {
      window.clearTimeout(resizeTimer)
      resizeTimer = window.setTimeout(report, 400)
    }
    window.addEventListener('resize', onResize)
    document.addEventListener('visibilitychange', report)
    document.addEventListener('fullscreenchange', onResize)
    const onMeta = () => report()
    video.current?.addEventListener('loadedmetadata', onMeta)
    viewerHandlers.add(handle)
    const ask = () =>
      void codecCaps('dec')
        .then((dec) => sendScreen(roomId, peerId, { t: 'watch', dec: dec ?? undefined }))
        .catch(() => undefined)
    ask()
    // No offer in 8 s (the sharer's app was busy): ask again.
    const retry = window.setInterval(() => {
      if (!gotOffer) ask()
    }, 8000)
    const bye = () => void sendScreen(roomId, peerId, { t: 'unwatch' }).catch(() => undefined)
    window.addEventListener('beforeunload', bye)
    return () => {
      viewerHandlers.delete(handle)
      window.removeEventListener('resize', onResize)
      document.removeEventListener('visibilitychange', report)
      document.removeEventListener('fullscreenchange', onResize)
      video.current?.removeEventListener('loadedmetadata', onMeta)
      window.clearTimeout(resizeTimer)
      window.clearInterval(retry)
      window.removeEventListener('beforeunload', bye)
      bye()
      endBroadcast()
      pcRef.current?.close()
    }
  }, [connected, roomId, peerId])

  const smoothRef = useRef(smooth)
  useEffect(() => {
    smoothRef.current = smooth
    for (const r of pcRef.current?.getReceivers() ?? []) {
      try {
        ;(r as any).jitterBufferTarget = smooth ? 80 : 0
      } catch {
        // ignore
      }
    }
  }, [smooth])

  // Live stats, once a second.
  useEffect(() => {
    let lastBytes = 0
    let lastAt = Date.now()
    let lastFrames = 0
    const id = window.setInterval(async () => {
      const pc = pcRef.current
      if (!pc || pc.connectionState !== 'connected') return
      const r = await pc.getStats()
      let w = 0,
        h = 0,
        fps = 0,
        bytes = 0,
        rtt: number | null = null,
        codec = ''
      r.forEach((s: any) => {
        if (s.type === 'inbound-rtp') {
          bytes += s.bytesReceived ?? 0
          if (s.kind === 'video') {
            if (s.codecId) codec = codecOf(r.get(s.codecId)?.mimeType) ?? codec
            w = s.frameWidth ?? w
            h = s.frameHeight ?? h
            fps = s.framesPerSecond ?? fps
          }
        } else if (s.type === 'candidate-pair' && s.state === 'succeeded' && typeof s.currentRoundTripTime === 'number') {
          rtt = s.currentRoundTripTime * 1000
        }
      })
      const now = Date.now()
      const b = rx.current
      if (b) {
        // Broadcast mode: the picture comes from the shared encoder.
        bytes += b.r.bytes
        h = b.r.height
        fps = (b.r.frames - lastFrames) / ((now - lastAt) / 1000)
        lastFrames = b.r.frames
        codec = `${b.codec} · broadcast`
      } else lastFrames = 0
      const kbps = lastBytes ? Math.round(((bytes - lastBytes) * 8) / 1000 / ((now - lastAt) / 1000)) : 0
      lastBytes = bytes
      lastAt = now
      setLive({ width: w, height: h, fps: Math.round(fps), kbps, rttMs: rtt, mb: bytes / 1048576, codec })
    }, 1000)
    return () => window.clearInterval(id)
  }, [])

  useEffect(() => {
    if (video.current) {
      video.current.muted = muted
      video.current.volume = Math.max(0, Math.min(1, volume * output))
      void routeTo(video.current, speaker)
    }
  }, [muted, volume, output, speaker, state])

  const fullscreen = () => void video.current?.parentElement?.requestFullscreen?.().catch(() => undefined)

  return (
    <div className="flex h-full flex-col bg-black text-slate-200">
      <div className="relative flex-1" onDoubleClick={fullscreen}>
        <video ref={video} autoPlay playsInline className="absolute inset-0 h-full w-full object-contain" />
        {state !== 'live' ? (
          <div className="absolute inset-0 grid place-items-center text-sm text-slate-400">
            {state === 'ended' ? t('screen.ended', { name }) : t('screen.connecting', { name })}
          </div>
        ) : null}
      </div>
      <div className="ltr flex flex-wrap items-center gap-3 border-t border-white/10 bg-[#0b0e17] px-3 py-2 text-xs">
        <span className="font-medium text-white">{name}</span>
        {live ? (
          <span className="font-mono text-slate-400">
            {live.height ? `${live.height}p` : '—'} · {live.fps} fps{live.codec ? ` · ${live.codec}` : ''} · {live.kbps >= 1000 ? `${(live.kbps / 1000).toFixed(1)} Mbps` : `${live.kbps} kbps`}
            {live.rttMs != null ? ` · ${Math.round(live.rttMs)} ms` : ''} · {live.mb.toFixed(1)} MB
          </span>
        ) : null}
        <span className="flex-1" />
        <label className="flex items-center gap-1 text-slate-400" title={t('screen.smoothHint')}>
          <input type="checkbox" checked={smooth} onChange={(e) => setSmooth(e.target.checked)} /> {t('screen.smooth')}
        </label>
        <button type="button" className="btn btn-ghost btn-sm" onClick={() => setMuted(!muted)} aria-label={muted ? t('voice.unmute') : t('voice.mute')}>
          {muted ? <VolumeX size={14} /> : <Volume2 size={14} />}
        </button>
        <input type="range" min={0} max={1} step={0.05} value={volume} className="w-20" onChange={(e) => setVolume(Number(e.target.value))} aria-label={t('voice.volume')} />
        <button type="button" className="btn btn-ghost btn-sm" onClick={fullscreen} aria-label={t('screen.fullscreen')}>
          <Maximize size={14} />
        </button>
      </div>
    </div>
  )
}
