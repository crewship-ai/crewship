'use client'

import { supportsPageApplications } from '@/lib/pages/runtime-support'
import { useEffect, useRef, useState } from 'react'
import { ArtifactRuntimeChannel, type ArtifactRuntimeIdentity } from '@/lib/pages/artifact-runtime-channel'

export type ArtifactRuntimeLease = { expiresAt: number }
export type ArtifactInteractiveFrameProps = {
  /** Server-admitted separate-site URL. Client checks are not a domain gate. */
  runtimeUrl: string
  identity: ArtifactRuntimeIdentity
  html: string
  accessible: boolean
  /** Must recheck the exact revision and current human session on the host. */
  lease: (identity: ArtifactRuntimeIdentity, signal: AbortSignal) => Promise<ArtifactRuntimeLease>
}

/** Preparation only, deliberately absent from product views. Caller admission
 * does not activate the hard-OFF product feature gate. Chromium only. */
export function ArtifactInteractiveFrame({ runtimeUrl, identity, html, accessible, lease }: ArtifactInteractiveFrameProps) {
  const { artifactId, revision, profile } = identity
  const container = useRef<HTMLDivElement>(null)
  const [attempt, setAttempt] = useState(0)
  const [stopped, setStopped] = useState(false)
  const [status, setStatus] = useState('Stopped')
  useEffect(() => {
    if (!accessible || stopped) { setStatus('Stopped'); return }
    if (!supportsPageApplications(navigator.userAgent)) { setStatus('Unsupported browser'); return }
    try {
      const url = new URL(runtimeUrl)
      if (url.protocol !== 'https:' || url.origin === location.origin || url.username || url.password || url.hash || url.search) throw new Error('Invalid runtime URL')
    } catch { setStatus('Runtime unavailable'); return }
    const abort = new AbortController()
    const frame = document.createElement('iframe')
    frame.title = 'Interactive artifact preparation'
    frame.setAttribute('sandbox', 'allow-scripts')
    frame.setAttribute('credentialless', '')
    frame.referrerPolicy = 'no-referrer'
    const nonce = crypto.randomUUID()
    let channel: ArtifactRuntimeChannel | undefined
    let disposed = false
    let admitted = false
    let ready = false
    let expiry = 0
    let timer: ReturnType<typeof setTimeout> | undefined
    let recheck: ReturnType<typeof setTimeout> | undefined
    const teardown = () => {
      if (disposed) return
      disposed = true
      abort.abort()
      clearTimeout(timer); clearTimeout(recheck)
      window.removeEventListener('message', connect)
      channel?.close()
      frame.remove()
    }
    const fail = (message: string) => { if (!disposed) { setStatus(message); teardown() } }
    const renew = async () => {
      try {
        const result = await lease({ artifactId: artifactId, revision: revision, profile: profile }, abort.signal)
        if (disposed) return false
        const now = Date.now()
        // A finite short lease, never an indefinite promise of permission.
        if (!Number.isFinite(result.expiresAt) || result.expiresAt <= now || result.expiresAt > now + 30_000) throw new Error('Invalid lease')
        expiry = result.expiresAt
        clearTimeout(timer)
        timer = setTimeout(() => fail('Access lease expired'), expiry - now)
        recheck = setTimeout(() => { void renew() }, Math.max(1, Math.min(5000, (expiry - now) / 2)))
        return true
      } catch { fail('Access could not be verified'); return false }
    }
    const connect = (event: MessageEvent) => {
      if (disposed || admitted || event.source !== frame.contentWindow || event.origin !== 'null' || event.data?.type !== 'artifact.ready' || event.data?.nonce !== nonce || Date.now() >= expiry) return
      admitted = true
      window.removeEventListener('message', connect)
      const ports = new MessageChannel()
      channel = new ArtifactRuntimeChannel(ports.port1, event => {
        if (!disposed && event.type === 'ready') { ready = true; setStatus('Running') }
      }, Date.now, () => fail('Runtime channel closed'))
      frame.contentWindow!.postMessage({ type: 'artifact.connect', nonce }, '*', [ports.port2])
      try { channel.load({ artifactId, revision, profile }, html) } catch { fail('Invalid runtime document') }
    }
    timer = setTimeout(() => fail('Access verification timed out'), 5000)
    setStatus('Verifying access')
    void renew().then(allowed => {
      if (!allowed || disposed) return
      setStatus('Starting')
      window.addEventListener('message', connect)
      frame.src = `${runtimeUrl}#${nonce}`
      container.current?.append(frame)
      // Independent startup deadline; lease renewal cannot extend it.
      const deadline = setTimeout(() => { if (!ready) fail('Runtime did not become ready') }, 5000)
      abort.signal.addEventListener('abort', () => clearTimeout(deadline), { once: true })
    })
    return teardown
  }, [runtimeUrl, artifactId, revision, profile, html, accessible, lease, stopped, attempt])
  return <section aria-label="Interactive artifact preparation">
    <p role="status">{accessible ? status : 'Access unavailable'}</p>
    <button type="button" onClick={() => setStopped(true)}>Stop</button>
    <button type="button" disabled={!accessible} onClick={() => { setStopped(false); setAttempt(value => value + 1) }}>Restart</button>
    <div ref={container} />
  </section>
}
