/** Preparation only: no product route accepts an interactive artifact profile. */
export const ARTIFACT_RUNTIME_ENABLED = false as const
export const ARTIFACT_RUNTIME_HTML_LIMIT = 10 * 1024 * 1024
export type ArtifactRuntimeIdentity = { artifactId: string; revision: number; profile: 'interactive-html-v1' }
export type ArtifactRuntimeTelemetry =
  | { type: 'ready' | 'heartbeat' }
  | { type: 'size'; width: number; height: number }
  | { type: 'scroll'; x: number; y: number }

const object = (value: unknown): value is Record<string, unknown> => !!value && typeof value === 'object' && !Array.isArray(value)
const coordinate = (value: unknown) => typeof value === 'number' && Number.isFinite(value) && value >= 0 && value <= 1_000_000
export function parseArtifactRuntimeTelemetry(value: unknown): ArtifactRuntimeTelemetry | null {
  if (!object(value)) return null
  const keys = Object.keys(value).sort().join(',')
  if ((value.type === 'ready' || value.type === 'heartbeat') && keys === 'type') return { type: value.type }
  if (value.type === 'size' && keys === 'height,type,width' && coordinate(value.width) && coordinate(value.height)) return { type: 'size', width: value.width as number, height: value.height as number }
  if (value.type === 'scroll' && keys === 'type,x,y' && coordinate(value.x) && coordinate(value.y)) return { type: 'scroll', x: value.x as number, y: value.y as number }
  return null
}

/** Caller admits the opaque child's exact contentWindow before transferring the
 * port. The port conveys telemetry, never authority or an API dispatcher.
 * Receipt of ready/heartbeat is not evidence of continuing read permission. */
export class ArtifactRuntimeChannel {
  private closed = false
  private transferred = false
  private windowStart = 0
  private count = 0
  constructor(private readonly port: MessagePort, private readonly observe: (event: ArtifactRuntimeTelemetry) => void, private readonly now = Date.now, private readonly onClosed?: () => void) {
    port.onmessage = event => {
      if (this.closed) return
      const current = this.now()
      if (current - this.windowStart >= 1000) { this.windowStart = current; this.count = 0 }
      if (++this.count > 30) { this.close(); return }
      const message = parseArtifactRuntimeTelemetry(event.data)
      if (!message) { this.close(); return }
      this.observe(message)
    }
    port.onmessageerror = () => this.close()
    port.start()
  }
  load(identity: ArtifactRuntimeIdentity, html: string) {
    if (this.closed || this.transferred) throw new Error('Artifact runtime transfer is unavailable.')
    if (!identity.artifactId || identity.artifactId.length > 128 || !Number.isSafeInteger(identity.revision) || identity.revision <= 0 || identity.profile !== 'interactive-html-v1' || new TextEncoder().encode(html).byteLength > ARTIFACT_RUNTIME_HTML_LIMIT) throw new Error('Invalid artifact runtime document.')
    this.transferred = true
    this.port.postMessage({ type: 'load', protocol: 1, identity: { artifactId: identity.artifactId, revision: identity.revision, profile: identity.profile }, html })
  }
  scrollTo(x: number, y: number) {
    if (!this.closed && coordinate(x) && coordinate(y)) this.port.postMessage({ type: 'scrollTo', x, y })
  }
  close() {
    if (this.closed) return
    this.closed = true
    this.port.onmessage = null
    this.port.onmessageerror = null
    this.port.close()
    this.onClosed?.()
  }
}
