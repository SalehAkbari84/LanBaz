/**
 * WebRTC helpers shared by voice chat and screen share. Both connect the two
 * webviews directly over the room's own 10.200 addresses, so media rides the
 * LanBaz tunnel; candidates travel inside the offer/answer, never trickled.
 */

/** Keeps only candidates on the room's own addresses, so audio uses the tunnel. */
export function tunnelOnly(sdp: string): string {
  return sdp
    .split('\r\n')
    .filter((line) => {
      if (!line.startsWith('a=candidate:')) return true
      const addr = line.split(' ')[4] ?? ''
      return addr.startsWith('10.200.') || addr.endsWith('.local')
    })
    .join('\r\n')
}

/** Waits until every candidate is in the local description (or 3 s). */
export function gathered(pc: RTCPeerConnection): Promise<void> {
  if (pc.iceGatheringState === 'complete') return Promise.resolve()
  return new Promise((resolve) => {
    const done = () => {
      if (pc.iceGatheringState !== 'complete') return
      pc.removeEventListener('icegatheringstatechange', done)
      resolve()
    }
    pc.addEventListener('icegatheringstatechange', done)
    setTimeout(resolve, 3000)
  })
}
