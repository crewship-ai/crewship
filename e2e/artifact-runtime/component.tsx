import React, { useCallback, useEffect, useRef, useState } from 'react'
import { createRoot } from 'react-dom/client'
import { ArtifactInteractiveFrame } from '../../components/features/pages/artifact-interactive-frame'
import type { ArtifactRuntimeIdentity } from '../../lib/pages/artifact-runtime-channel'

// Test-only lease adapter. This fixture establishes no human/session authority.
function App() {
  const [revision, setRevision] = useState(1)
  const [html, setHTML] = useState('<h1>Exact revision 1</h1>')
  const mode = useRef('valid')
  const [accessible, setAccessible] = useState(true)
  const state = useRef({ ticks: 0, aborted: 0, leases: [] as { revision: number; mode: string }[] })
  useEffect(() => {
    const timer = setInterval(() => state.current.ticks++, 20)
    Object.assign(window, { runtimeComponent: state.current })
    return () => clearInterval(timer)
  }, [])
  const lease = useCallback(async (identity: ArtifactRuntimeIdentity, signal: AbortSignal) => {
    state.current.leases.push({ revision: identity.revision, mode: mode.current })
    signal.addEventListener('abort', () => state.current.aborted++, { once: true })
    if (mode.current === 'denied') throw new Error('Test-only access denial')
    if (mode.current === 'hung') return new Promise<{ expiresAt: number }>(() => {})
    return { expiresAt: Date.now() + (mode.current === 'expired' ? -1 : 3000) }
  }, [])
  return <>
    <p>Isolated preparation fixture; simulated lease, no product activation.</p>
    {['valid', 'denied', 'expired', 'hung'].map(value => <button key={value} onClick={() => { mode.current = value }}>Lease {value}</button>)}
    <button onClick={() => setAccessible(false)}>Revoke access</button>
    <button onClick={() => { setRevision(2); setHTML('<h1>Exact revision 2</h1>') }}>Load revision 2</button>
    <button onClick={() => { setRevision(3); setHTML('<h1>Loop revision 3</h1><script>setTimeout(()=>{console.log("component-loop-started");for(;;){}},200)</script>') }}>Run infinite artifact</button>
    <ArtifactInteractiveFrame runtimeUrl="https://runtime.artifacts.test/component-bootstrap" identity={{ artifactId: 'component-page', revision, profile: 'interactive-html-v1' }} html={html} accessible={accessible} lease={lease} />
  </>
}
createRoot(document.getElementById('root')!).render(<App />)
declare global { interface Window { runtimeComponent: { ticks: number; aborted: number; leases: { revision: number; mode: string }[] } } }
